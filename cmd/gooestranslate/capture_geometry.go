package main

import (
	"image"
	"image/draw"
)

// Selection coordinates remain local to the frozen virtual desktop. A monitor
// to the left or above the primary monitor can have a negative screen origin.
func captureLocalPoint(screen image.Point, desktopBounds image.Rectangle) image.Point {
	return screen.Sub(desktopBounds.Min)
}

func captureSelection(start, end image.Point, bounds image.Rectangle) image.Rectangle {
	return image.Rect(min(start.X, end.X), min(start.Y, end.Y), max(start.X, end.X), max(start.Y, end.Y)).Intersect(bounds)
}

func captureCrop(snapshot *image.RGBA, selected image.Rectangle) *image.RGBA {
	if snapshot == nil {
		return nil
	}
	selected = selected.Intersect(snapshot.Bounds())
	if selected.Dx() < 3 || selected.Dy() < 3 {
		return nil
	}
	img := image.NewRGBA(image.Rect(0, 0, selected.Dx(), selected.Dy()))
	draw.Draw(img, img.Bounds(), snapshot, selected.Min, draw.Src)
	return img
}

type captureAction struct {
	Bounds image.Rectangle
	Label  string
	Mode   string
}

func captureToolbar(selected, screen image.Rectangle, scale float64) []captureAction {
	if selected.Empty() || screen.Empty() {
		return nil
	}
	size := func(n int) int { return max(1, int(float64(n)*scale+.5)) }
	margin, gap, height := size(8), size(2), size(42)
	widths := []int{size(106), size(106), size(120), size(62)}
	full := margin*2 + gap*3
	for _, w := range widths {
		full += w
	}
	x := min(selected.Max.X-full, screen.Max.X-full-size(8))
	x = max(screen.Min.X+size(8), x)
	y := selected.Max.Y + size(10)
	if y+height > screen.Max.Y-size(8) {
		y = selected.Min.Y - height - size(10)
	}
	y = max(screen.Min.Y+size(8), min(y, screen.Max.Y-height-size(8)))
	actions := make([]captureAction, 0, 4)
	labels := []string{"文字识别  1", "翻译文字  2", "原图翻译  3", "取消"}
	modes := []string{"ocr", "translate", "image", "cancel"}
	for i, w := range widths {
		actions = append(actions, captureAction{image.Rect(x+margin, y+size(5), x+margin+w, y+height-size(5)), labels[i], modes[i]})
		x += w + gap
	}
	return actions
}

func captureActionAt(actions []captureAction, p image.Point) string {
	for _, action := range actions {
		if p.In(action.Bounds) {
			return action.Mode
		}
	}
	return ""
}

func captureShortcut(key uintptr, ready bool) string {
	if key == 27 {
		return "cancel"
	}
	if !ready {
		return ""
	}
	switch key {
	case '1':
		return "ocr"
	case '2':
		return "translate"
	case '3', 13:
		return "image"
	}
	return ""
}
