package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gooesman/gooestranslate/internal/engine"
	"image"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const version = "0.1.0"
const child = 0x40000000 | 0x10000000
const workMessage = 0x8001

type appState struct {
	hwnd, canvas, screen           uintptr
	controls                       map[int]uintptr
	labels                         map[int]uintptr
	regular, heading, small, brush uintptr
	scale                          float64
	cfg                            engine.Config
	img                            *image.RGBA
	pixels                         []byte
	blocks                         []engine.Block
	lines                          []engine.Line
	translations                   map[int]string
	selected                       map[int]bool
	view                           engine.Viewport
	drag                           bool
	dragStart, dragEnd             engine.Point
	capture                        *image.RGBA
	capturePixels                  []byte
	captureRect                    rect
	captureStart, captureEnd       point
	capturing                      bool
	busy                           bool
	cancel                         context.CancelFunc
	generation                     uint64
	queue                          chan func()
	startupError                   string
	hotkey                         bool
}

var app *appState

func main() {
	runtime.LockOSThread()
	if len(os.Args) > 1 && os.Args[1] == "--configure-stdin" {
		var c struct {
			OCRURL          string `json:"ocr_url"`
			BaseURL         string `json:"base_url"`
			Model           string `json:"model"`
			APIKey          string `json:"api_key"`
			Language        string `json:"language"`
			DisableThinking bool   `json:"disable_thinking"`
		}
		err := json.NewDecoder(io.LimitReader(os.Stdin, 65536)).Decode(&c)
		if err == nil {
			err = saveConfig(engine.Config{OCRURL: c.OCRURL, BaseURL: c.BaseURL, Model: c.Model, APIKey: c.APIKey, Language: c.Language, DisableThinking: c.DisableThinking})
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "配置保存失败")
			os.Exit(1)
		}
		fmt.Println("配置已加密保存")
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "--smoke-test" || os.Args[1] == "--live-test") {
		if err := smokeTest(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	call(user, "SetProcessDpiAwarenessContext", ^uintptr(3))
	cfg, err := loadConfig()
	app = &appState{cfg: cfg, controls: map[int]uintptr{}, labels: map[int]uintptr{}, scale: 1, queue: make(chan func(), 32), selected: map[int]bool{}, translations: map[int]string{}}
	if err != nil {
		app.startupError = err.Error()
	}
	for _, c := range []struct {
		name   string
		proc   uintptr
		cursor uintptr
	}{{"GooesTranslateMain", syscall.NewCallback(mainProc), 32512}, {"GooesTranslateCanvas", syscall.NewCallback(canvasProc), 32512}, {"GooesTranslateCapture", syscall.NewCallback(captureProc), 32515}} {
		if err = register(c.name, c.proc, c.cursor); err != nil {
			fatal(err)
			return
		}
	}
	app.hwnd = create("GooesTranslateMain", "gooestranslate · 截图与原图翻译", 0xcf0000|0x02000000, 0x40000, 100, 80, 1240, 850, 0, 0)
	if app.hwnd == 0 {
		fatal(fmt.Errorf("无法创建主窗口"))
		return
	}
	app.scale = float64(call(user, "GetDpiForWindow", app.hwnd)) / 96
	app.fitScale()
	work := workArea(app.hwnd)
	width := min(app.d(1240), int(work.R-work.L)-32)
	height := min(app.d(850), int(work.B-work.T)-32)
	call(user, "SetWindowPos", app.hwnd, 0, u(int(work.L)+(int(work.R-work.L)-width)/2), u(int(work.T)+(int(work.B-work.T)-height)/2), u(width), u(height), 0x14)
	app.createUI()
	app.layout()
	app.hotkey = call(user, "RegisterHotKey", app.hwnd, 1, 0x4000|2|4, 'S') != 0
	call(user, "ShowWindow", app.hwnd, 5)
	call(user, "UpdateWindow", app.hwnd)
	if app.startupError != "" {
		app.status(app.startupError)
	} else if !app.hotkey {
		app.status("截图快捷键被占用，可点击“截图”按钮。")
	} else {
		app.status("Ctrl+Shift+S 截图 · 打开图片后点击识别 · 可在原图中点击或框选文字")
	}
	if len(os.Args) > 2 && os.Args[1] == "--open" {
		app.openImage(os.Args[2])
	}
	var m message
	for {
		r := int32(call(user, "GetMessageW", uintptr(unsafe.Pointer(&m)), 0, 0, 0))
		if r <= 0 {
			break
		}
		if call(user, "IsDialogMessageW", app.hwnd, uintptr(unsafe.Pointer(&m))) == 0 {
			call(user, "TranslateMessage", uintptr(unsafe.Pointer(&m)))
			call(user, "DispatchMessageW", uintptr(unsafe.Pointer(&m)))
		}
	}
}
func fatal(e error) {
	call(user, "MessageBoxW", 0, uintptr(unsafe.Pointer(str(e.Error()))), uintptr(unsafe.Pointer(str("gooestranslate"))), 0x10)
}
func (a *appState) d(n int) int { return int(float64(n)*a.scale + .5) }
func workArea(h uintptr) rect {
	var m struct {
		Size          uint32
		Monitor, Work rect
		Flags         uint32
	}
	m.Size = uint32(unsafe.Sizeof(m))
	monitor := call(user, "MonitorFromWindow", h, 2)
	if call(user, "GetMonitorInfoW", monitor, uintptr(unsafe.Pointer(&m))) != 0 {
		return m.Work
	}
	return rect{0, 0, int32(call(user, "GetSystemMetrics", 0)), int32(call(user, "GetSystemMetrics", 1))}
}
func (a *appState) fitScale() {
	if a.scale <= 0 {
		a.scale = 1
	}
	r := workArea(a.hwnd)
	a.scale = min(a.scale, float64(r.R-r.L-32)/1100, float64(r.B-r.T-32)/780)
}
func (a *appState) refreshFonts() {
	old := []uintptr{a.regular, a.small, a.heading}
	a.regular = font(a.d(15), false)
	a.small = font(a.d(13), false)
	a.heading = font(a.d(24), true)
	for _, h := range a.controls {
		send(h, 0x30, a.regular, 1)
	}
	for _, h := range a.labels {
		send(h, 0x30, a.small, 1)
	}
	for _, h := range old {
		if h != 0 {
			call(gdi, "DeleteObject", h)
		}
	}
}
func (a *appState) control(id int, class, s string, style uintptr) uintptr {
	h := create(class, s, child|style, 0, 0, 0, 1, 1, a.hwnd, uintptr(id))
	a.controls[id] = h
	send(h, 0x30, a.regular, 1)
	return h
}
func (a *appState) createUI() {
	a.regular = font(a.d(15), false)
	a.small = font(a.d(13), false)
	a.heading = font(a.d(24), true)
	a.brush = call(gdi, "CreateSolidBrush", rgb(250, 250, 252))
	for _, b := range []struct {
		id   int
		text string
	}{{101, "截图  Ctrl+Shift+S"}, {102, "打开图片"}, {103, "识别文字"}, {104, "原图翻译"}, {105, "导出 PNG"}, {106, "复制文字"}, {107, "取消请求"}, {207, "保存设置"}} {
		a.control(b.id, "BUTTON", b.text, 0x10000)
	}
	for _, f := range []struct {
		id    int
		label string
		value string
	}{{201, "OCR 表单接口", a.cfg.OCRURL}, {202, "翻译 API 基础地址", a.cfg.BaseURL}, {203, "模型名称", a.cfg.Model}, {204, "API 密钥（本机加密保存）", a.cfg.APIKey}} {
		style := uintptr(0x10000 | 0x800000 | 0x80)
		if f.id == 204 {
			style |= 0x20
		}
		h := a.control(f.id, "EDIT", f.value, style)
		send(h, 0xc5, 8192, 0)
		l := create("STATIC", f.label, child, 0, 0, 0, 1, 1, a.hwnd, 0)
		send(l, 0x30, a.small, 1)
		a.labels[f.id] = l
	}
	a.control(205, "COMBOBOX", "", 3|0x10000|0x200000)
	for _, s := range []string{"简体中文", "繁體中文", "English", "日本語"} {
		send(a.controls[205], 0x143, 0, uintptr(unsafe.Pointer(str(s))))
	}
	idx := 0
	for i, s := range []string{"简体中文", "繁體中文", "English", "日本語"} {
		if s == a.cfg.Language {
			idx = i
		}
	}
	send(a.controls[205], 0x14e, u(idx), 0)
	a.control(206, "BUTTON", "关闭模型思考", 3|0x10000)
	setCheck(a.controls[206], a.cfg.DisableThinking)
	a.control(208, "BUTTON", "显示文字框", 3|0x10000)
	setCheck(a.controls[208], true)
	a.control(209, "BUTTON", "显示译文", 3|0x10000)
	setCheck(a.controls[209], true)
	a.control(210, "EDIT", "", 0x10000|0x800000|4|0x800|0x40|0x200000)
	send(a.controls[210], 0xc5, 1000000, 0)
	a.control(211, "STATIC", "", 0)
	enabled(a.controls[107], false)
	a.canvas = create("GooesTranslateCanvas", "", child|0x10000, 0, 0, 0, 1, 1, a.hwnd, 0)
}
func (a *appState) layout() {
	if a.canvas == 0 {
		return
	}
	r := clientRect(a.hwnd)
	w, h := int(float64(r.R)/a.scale), int(float64(r.B)/a.scale)
	pos := func(handle uintptr, x, y, width, height int) { move(handle, a.d(x), a.d(y), a.d(width), a.d(height)) }
	x := 20
	for _, b := range []struct{ id, w int }{{101, 174}, {102, 94}, {103, 96}, {104, 102}, {105, 94}, {106, 96}, {107, 92}} {
		pos(a.controls[b.id], x, 76, b.w, 34)
		x += b.w + 8
	}
	side := w - 320
	canvasW := side - 40
	if canvasW < 250 {
		canvasW = 250
	}
	pos(a.canvas, 20, 126, canvasW, h-211)
	pos(a.controls[208], 20, h-72, 132, 28)
	pos(a.controls[209], 160, h-72, 116, 28)
	y := 140
	for _, id := range []int{201, 202, 203, 204} {
		pos(a.labels[id], side, y, 292, 21)
		pos(a.controls[id], side, y+25, 292, 29)
		y += 65
	}
	pos(a.controls[205], side, 410, 292, 200)
	pos(a.controls[206], side, 450, 165, 25)
	pos(a.controls[207], side+176, 445, 116, 34)
	pos(a.controls[210], side, 518, 292, h-560)
	pos(a.controls[211], 20, h-34, w-40, 24)
	invalidate(a.hwnd)
	invalidate(a.canvas)
}
func (a *appState) config() engine.Config {
	c := a.cfg
	c.OCRURL = strings.TrimSpace(text(a.controls[201]))
	c.BaseURL = strings.TrimSpace(text(a.controls[202]))
	c.Model = strings.TrimSpace(text(a.controls[203]))
	c.APIKey = strings.TrimSpace(text(a.controls[204]))
	c.Language = text(a.controls[205])
	c.DisableThinking = check(a.controls[206])
	return c
}
func (a *appState) status(s string) { setText(a.controls[211], s) }
func (a *appState) setBusy(on bool) {
	a.busy = on
	for _, id := range []int{101, 102, 103, 104, 105} {
		enabled(a.controls[id], !on)
	}
	enabled(a.controls[107], on)
}
func (a *appState) post(f func()) {
	a.queue <- f
	call(user, "PostMessageW", a.hwnd, workMessage, 0, 0)
}
func (a *appState) newTask() (context.Context, uint64) {
	a.generation++
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	a.cancel = cancel
	a.setBusy(true)
	return ctx, a.generation
}
func (a *appState) stop() {
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	a.generation++
	a.setBusy(false)
	a.status("已取消请求")
}
func (a *appState) recognize() {
	if a.busy {
		return
	}
	if a.img == nil {
		a.status("请先截图或打开图片")
		return
	}
	cfg := a.config()
	if err := engine.ValidateURL(cfg.OCRURL); err != nil {
		a.status("请填写 OCR 表单接口地址")
		return
	}
	ctx, generation := a.newTask()
	img := a.img
	a.status("正在识别：截图将上传到填写的 OCR 地址…")
	go func() {
		blocks, err := engine.Recognize(ctx, engine.HTTPClient(), cfg.OCRURL, img)
		a.post(func() {
			if generation != a.generation {
				return
			}
			a.cancel()
			a.cancel = nil
			a.setBusy(false)
			if err != nil {
				a.status(err.Error())
				return
			}
			a.blocks = blocks
			a.lines = engine.GroupLines(blocks)
			a.translations = map[int]string{}
			a.selected = map[int]bool{}
			setText(a.controls[210], engine.Text(blocks))
			a.status(fmt.Sprintf("识别完成：%d 个文字框。点击或拖动选择文字，点击“原图翻译”生成译文。", len(blocks)))
			invalidate(a.canvas)
		})
	}()
}
func (a *appState) translate() {
	if a.busy {
		return
	}
	if len(a.lines) == 0 {
		a.status("请先识别文字")
		return
	}
	cfg := a.config()
	ctx, generation := a.newTask()
	lines := append([]engine.Line(nil), a.lines...)
	a.status("正在翻译识别文字，图片不会发往翻译接口…")
	go func() {
		values, err := engine.Translate(ctx, engine.HTTPClient(), cfg, lines)
		a.post(func() {
			if generation != a.generation {
				return
			}
			a.cancel()
			a.cancel = nil
			a.setBusy(false)
			if err != nil {
				a.status(err.Error())
				return
			}
			a.translations = map[int]string{}
			var texts []string
			for _, v := range values {
				a.translations[v.ID] = v.Text
				texts = append(texts, v.Text)
			}
			setText(a.controls[210], strings.Join(texts, "\r\n"))
			setCheck(a.controls[209], true)
			a.status("翻译完成。取消“显示译文”对照原图，可导出 PNG。")
			invalidate(a.canvas)
		})
	}()
}
func (a *appState) setImage(img image.Image) {
	b := img.Bounds()
	a.img = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(a.img, a.img.Bounds(), img, b.Min, draw.Src)
	a.pixels = imagePixels(a.img)
	a.blocks = nil
	a.lines = nil
	a.translations = map[int]string{}
	a.selected = map[int]bool{}
	setText(a.controls[210], "")
	a.status(fmt.Sprintf("图片已载入：%d × %d。点击“识别文字”。", b.Dx(), b.Dy()))
	invalidate(a.canvas)
}
func (a *appState) openImage(file string) {
	f, e := os.Open(file)
	if e != nil {
		a.status("图片打开失败")
		return
	}
	defer f.Close()
	cfg, _, e := image.DecodeConfig(f)
	if e != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40000000 {
		a.status("请使用不超过 4000 万像素的 PNG 或 JPEG 图片")
		return
	}
	f.Seek(0, 0)
	img, _, e := image.Decode(f)
	if e != nil {
		a.status("图片格式不支持或文件损坏")
		return
	}
	a.setImage(img)
}
func (a *appState) copyText() {
	s := text(a.controls[210])
	if strings.TrimSpace(s) == "" {
		a.status("没有可复制的文字")
		return
	}
	if e := clipboard(a.hwnd, s); e != nil {
		a.status(e.Error())
	} else {
		a.status("文字已复制")
	}
}
func (a *appState) export() {
	if a.img == nil {
		a.status("请先打开图片")
		return
	}
	file := fileDialog(a.hwnd, true)
	if file == "" {
		return
	}
	img, err := a.composite()
	if err == nil {
		var f *os.File
		f, err = os.Create(file)
		if err == nil {
			err = png.Encode(f, img)
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err != nil {
		a.status("导出图片失败")
	} else {
		a.status("PNG 已保存：" + file)
	}
}
func mainProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	if app == nil {
		return call(user, "DefWindowProcW", hwnd, uintptr(msg), wp, lp)
	}
	switch msg {
	case 5:
		if wp == 1 {
			return 0
		}
		app.layout()
		return 0
	case 0x24: // MINMAXINFO: five POINTs
		var p [5]point
		call(ntdll, "RtlMoveMemory", uintptr(unsafe.Pointer(&p)), lp, unsafe.Sizeof(p))
		p[3] = point{int32(app.d(1060)), int32(app.d(720))}
		call(ntdll, "RtlMoveMemory", lp, uintptr(unsafe.Pointer(&p)), unsafe.Sizeof(p))
		return 0
	case 0x2e0:
		app.scale = float64(wp&0xffff) / 96
		var r rect
		call(ntdll, "RtlMoveMemory", uintptr(unsafe.Pointer(&r)), lp, unsafe.Sizeof(r))
		call(user, "SetWindowPos", hwnd, 0, u(int(r.L)), u(int(r.T)), u(int(r.R-r.L)), u(int(r.B-r.T)), 0x14)
		app.fitScale()
		if app.canvas != 0 {
			app.refreshFonts()
		}
		app.layout()
		return 0
	case workMessage:
		for {
			select {
			case f := <-app.queue:
				f()
			default:
				return 0
			}
		}
	case 0x312:
		if wp == 1 && !app.busy {
			app.startCapture()
		}
		return 0
	case 0x113:
		if wp == 1 {
			call(user, "KillTimer", hwnd, 1)
			app.finishCaptureStart()
		}
		return 0
	case 0x111:
		id := int(wp & 0xffff)
		switch id {
		case 101:
			app.startCapture()
		case 102:
			if !app.busy {
				if f := fileDialog(hwnd, false); f != "" {
					app.openImage(f)
				}
			}
		case 103:
			app.recognize()
		case 104:
			app.translate()
		case 105:
			app.export()
		case 106:
			app.copyText()
		case 107:
			app.stop()
		case 207:
			c := app.config()
			if err := saveConfig(c); err != nil {
				app.status(err.Error())
			} else {
				app.cfg = c
				app.status("设置已保存，API 密钥由 Windows 加密")
			}
		case 208, 209:
			invalidate(app.canvas)
		}
		return 0
	case 0x138:
		call(gdi, "SetBkMode", wp, 1)
		return app.brush
	case 0x14:
		return 1
	case 0xf:
		var ps paintstruct
		dc := call(user, "BeginPaint", hwnd, uintptr(unsafe.Pointer(&ps)))
		r := clientRect(hwnd)
		fill(dc, r, rgb(250, 250, 252))
		drawText(dc, "gooestranslate", rect{int32(app.d(20)), int32(app.d(15)), int32(app.d(430)), int32(app.d(54))}, app.heading, rgb(28, 33, 42), 0x24)
		drawText(dc, "截图 · 识别 · 原图翻译", rect{int32(app.d(270)), int32(app.d(24)), int32(app.d(620)), int32(app.d(52))}, app.regular, rgb(105, 111, 123), 0x24)
		side := r.R - int32(app.d(320))
		drawText(dc, "服务设置", rect{side, int32(app.d(113)), r.R, int32(app.d(138))}, app.regular, rgb(41, 48, 59), 0)
		drawText(dc, "识别 / 译文 / 已选文字", rect{side, int32(app.d(487)), r.R, int32(app.d(513))}, app.regular, rgb(41, 48, 59), 0)
		call(user, "EndPaint", hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0
	case 0x10:
		if app.cancel != nil {
			app.cancel()
		}
		if app.screen != 0 {
			call(user, "DestroyWindow", app.screen)
		}
		call(user, "DestroyWindow", hwnd)
		return 0
	case 2:
		if app.hotkey {
			call(user, "UnregisterHotKey", hwnd, 1)
		}
		for _, h := range []uintptr{app.regular, app.small, app.heading, app.brush} {
			if h != 0 {
				call(gdi, "DeleteObject", h)
			}
		}
		call(user, "PostQuitMessage", 0)
		return 0
	}
	return call(user, "DefWindowProcW", hwnd, uintptr(msg), wp, lp)
}
