package engine

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

type Point struct{ X, Y float64 }
type Rect struct{ X, Y, W, H float64 }
type Block struct {
	Text string        `json:"text"`
	Box  [4][2]float64 `json:"box"`
}
type Line struct {
	ID     int    `json:"id"`
	Text   string `json:"text"`
	Bounds Rect   `json:"-"`
}
type Translation struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}
type Config struct {
	OCRURL          string           `json:"ocr_url"`
	BaseURL         string           `json:"base_url"`
	Model           string           `json:"model"`
	APIKey          string           `json:"-"`
	EncryptedKey    string           `json:"encrypted_key,omitempty"`
	Language        string           `json:"language"`
	DisableThinking bool             `json:"disable_thinking"`
	Theme           string           `json:"theme"`
	DefaultAction   string           `json:"default_action"`
	CloseToTray     bool             `json:"close_to_tray"`
	AutoCopyOCR     bool             `json:"auto_copy_ocr"`
	Hotkeys         Hotkeys          `json:"hotkeys"`
	Dictionary      DictionaryConfig `json:"dictionary"`
	TTS             TTSConfig        `json:"tts"`
}
type Hotkeys struct {
	Capture   string `json:"capture"`
	OCR       string `json:"ocr"`
	Translate string `json:"translate"`
	Image     string `json:"image"`
}
type DictionaryConfig struct {
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider"`
	LocalPath string `json:"local_path"`
}
type TTSConfig struct {
	Provider string `json:"provider"`
	Voice    string `json:"voice"`
	Rate     int    `json:"rate"`
	AutoRead bool   `json:"auto_read"`
}

func DefaultConfig() Config {
	return Config{Language: "简体中文", DisableThinking: true, Theme: "dark", DefaultAction: "choose", CloseToTray: true, Hotkeys: Hotkeys{"Ctrl+Shift+S", "Ctrl+Alt+O", "Ctrl+Alt+T", "Ctrl+Alt+I"}, Dictionary: DictionaryConfig{Enabled: true, Provider: "openai"}, TTS: TTSConfig{Provider: "system"}}
}
func (b Block) Bounds() Rect {
	x, y, r, d := b.Box[0][0], b.Box[0][1], b.Box[0][0], b.Box[0][1]
	for _, p := range b.Box {
		x = math.Min(x, p[0])
		y = math.Min(y, p[1])
		r = math.Max(r, p[0])
		d = math.Max(d, p[1])
	}
	return Rect{x, y, r - x, d - y}
}
func (r Rect) Contains(p Point) bool {
	return p.X >= r.X && p.X <= r.X+r.W && p.Y >= r.Y && p.Y <= r.Y+r.H
}
func (r Rect) Intersects(s Rect) bool {
	return r.X < s.X+s.W && s.X < r.X+r.W && r.Y < s.Y+s.H && s.Y < r.Y+r.H
}

// Hit uses the actual four-sided polygon, not its bounding rectangle.
func (b Block) Hit(p Point) bool {
	inside := false
	for i, j := 0, 3; i < 4; j, i = i, i+1 {
		a, c := b.Box[i], b.Box[j]
		if (a[1] > p.Y) != (c[1] > p.Y) && p.X < (c[0]-a[0])*(p.Y-a[1])/(c[1]-a[1])+a[0] {
			inside = !inside
		}
	}
	return inside
}
func FromPoints(a, b Point) Rect {
	return Rect{math.Min(a.X, b.X), math.Min(a.Y, b.Y), math.Abs(a.X - b.X), math.Abs(a.Y - b.Y)}
}
func GroupLines(blocks []Block) []Line {
	items := append([]Block(nil), blocks...)
	sort.SliceStable(items, func(i, j int) bool { a, b := items[i].Bounds(), items[j].Bounds(); return a.Y+a.H/2 < b.Y+b.H/2 })
	var rows [][]Block
	for _, b := range items {
		r := b.Bounds()
		if len(rows) == 0 {
			rows = append(rows, []Block{b})
			continue
		}
		ref := rows[len(rows)-1][0].Bounds()
		if math.Abs(r.Y+r.H/2-ref.Y-ref.H/2) > math.Min(r.H, ref.H)*.5 {
			rows = append(rows, []Block{b})
		} else {
			rows[len(rows)-1] = append(rows[len(rows)-1], b)
		}
	}
	var out []Line
	word := func(r rune) bool { return r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) }
	for _, row := range rows {
		sort.SliceStable(row, func(i, j int) bool { return row[i].Bounds().X < row[j].Bounds().X })
		l := Line{Text: row[0].Text, Bounds: row[0].Bounds()}
		for _, b := range row[1:] {
			r := b.Bounds()
			a := l.Bounds
			if r.X-a.X-a.W > 2*math.Max(a.H, r.H) {
				l.ID = len(out)
				out = append(out, l)
				l = Line{Text: b.Text, Bounds: r}
				continue
			}
			left, right := []rune(l.Text), []rune(b.Text)
			if len(left) > 0 && len(right) > 0 && word(left[len(left)-1]) && word(right[0]) {
				l.Text += " "
			}
			l.Text += b.Text
			x, y := math.Min(a.X, r.X), math.Min(a.Y, r.Y)
			l.Bounds = Rect{x, y, math.Max(a.X+a.W, r.X+r.W) - x, math.Max(a.Y+a.H, r.Y+r.H) - y}
		}
		l.ID = len(out)
		out = append(out, l)
	}
	return out
}
func Text(blocks []Block) string {
	var s []string
	for _, l := range GroupLines(blocks) {
		s = append(s, l.Text)
	}
	return strings.Join(s, "\r\n")
}

type Viewport struct{ X, Y, Scale float64 }

func Fit(width, height int, area Rect) Viewport {
	if width <= 0 || height <= 0 {
		return Viewport{}
	}
	s := math.Min(area.W/float64(width), area.H/float64(height))
	return Viewport{area.X + (area.W-float64(width)*s)/2, area.Y + (area.H-float64(height)*s)/2, s}
}
func (v Viewport) ToImage(p Point) Point { return Point{(p.X - v.X) / v.Scale, (p.Y - v.Y) / v.Scale} }
func (v Viewport) ToScreen(r Rect) Rect {
	return Rect{v.X + r.X*v.Scale, v.Y + r.Y*v.Scale, r.W * v.Scale, r.H * v.Scale}
}
