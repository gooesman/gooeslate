package main

import (
	"fmt"
	"github.com/gooesman/gooestranslate/internal/engine"
	"image"
	"math"
	"unsafe"
)

func winRect(r engine.Rect) rect {
	return rect{int32(math.Round(r.X)), int32(math.Round(r.Y)), int32(math.Round(r.X + r.W)), int32(math.Round(r.Y + r.H))}
}
func (a *appState) render(dc uintptr, area engine.Rect, boxes bool) {
	if a.img == nil {
		return
	}
	b := a.img.Bounds()
	v := engine.Fit(b.Dx(), b.Dy(), area)
	if boxes {
		a.view = v
	}
	paintPixels(dc, a.pixels, b.Dx(), b.Dy(), winRect(v.ToScreen(engine.Rect{W: float64(b.Dx()), H: float64(b.Dy())})))
	if check(a.controls[209]) {
		a.renderTranslations(dc, v)
	}
	if boxes && check(a.controls[208]) {
		for i, b := range a.blocks {
			r := winRect(v.ToScreen(b.Bounds()))
			color := rgb(67, 142, 153)
			if a.selected[i] {
				color = rgb(238, 115, 55)
				frame(dc, rect{r.L - 1, r.T - 1, r.R + 1, r.B + 1}, color)
			}
			frame(dc, r, color)
		}
	}
	if boxes && a.drag {
		frame(dc, winRect(engine.FromPoints(a.dragStart, a.dragEnd)), rgb(238, 115, 55))
	}
}
func (a *appState) renderTranslations(dc uintptr, v engine.Viewport) {
	for _, l := range a.lines {
		s, ok := a.translations[l.ID]
		if !ok {
			continue
		}
		r := winRect(v.ToScreen(l.Bounds))
		if r.R <= r.L || r.B <= r.T {
			continue
		}
		// OCR rectangles may exclude antialiased glyph edges. Cover a small margin.
		pad := int32(math.Max(2, 6*v.Scale))
		fill(dc, rect{r.L - pad, r.T - pad, r.R + pad, r.B + pad}, rgb(255, 255, 255))
		size := int(float64(r.B-r.T) * .82)
		if size < 6 {
			size = 6
		}
		var f uintptr
		for attempt := 0; attempt < 40; attempt++ {
			if f != 0 {
				call(gdi, "DeleteObject", f)
			}
			f = font(size, false)
			old := call(gdi, "SelectObject", dc, f)
			measure := rect{0, 0, r.R - r.L, 0}
			call(user, "DrawTextW", dc, uintptr(unsafe.Pointer(str(s))), ^uintptr(0), uintptr(unsafe.Pointer(&measure)), 0x4000|0x10|0x800)
			call(gdi, "SelectObject", dc, old)
			if measure.B <= r.B-r.T && measure.R <= r.R-r.L || size <= 5 {
				break
			}
			size--
		}
		state := call(gdi, "SaveDC", dc)
		call(gdi, "IntersectClipRect", dc, u(int(r.L)), u(int(r.T)), u(int(r.R)), u(int(r.B)))
		drawText(dc, s, r, f, rgb(30, 34, 40), 0x10)
		call(gdi, "RestoreDC", dc, state)
		call(gdi, "DeleteObject", f)
	}
}
func (a *appState) composite() (*image.RGBA, error) {
	if a.img == nil {
		return nil, fmt.Errorf("没有图片")
	}
	w, h := a.img.Bounds().Dx(), a.img.Bounds().Dy()
	dc := call(gdi, "CreateCompatibleDC", 0)
	if dc == 0 {
		return nil, fmt.Errorf("无法创建导出画布")
	}
	defer call(gdi, "DeleteDC", dc)
	info := bitmapinfo{Header: bitmapheader{Size: 40, Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}}
	var bits unsafe.Pointer
	bmp := call(gdi, "CreateDIBSection", dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return nil, fmt.Errorf("无法创建导出图片")
	}
	defer call(gdi, "DeleteObject", bmp)
	old := call(gdi, "SelectObject", dc, bmp)
	defer call(gdi, "SelectObject", dc, old)
	a.render(dc, engine.Rect{W: float64(w), H: float64(h)}, false)
	call(gdi, "GdiFlush")
	return pixelsImage(unsafe.Slice((*byte)(bits), w*h*4), w, h), nil
}
func (a *appState) selection() {
	if a.view.Scale <= 0 {
		return
	}
	p, q := a.view.ToImage(a.dragStart), a.view.ToImage(a.dragEnd)
	area := engine.FromPoints(p, q)
	pointHit := math.Abs(a.dragEnd.X-a.dragStart.X) < 4 && math.Abs(a.dragEnd.Y-a.dragStart.Y) < 4
	if int16(call(user, "GetKeyState", 0x11)) >= 0 {
		a.selected = map[int]bool{}
	}
	for i, b := range a.blocks {
		hit := b.Bounds().Intersects(area)
		if pointHit {
			hit = b.Hit(q)
		}
		if hit {
			if a.selected[i] {
				delete(a.selected, i)
			} else {
				a.selected[i] = true
			}
		}
	}
	var selected []engine.Block
	for i, b := range a.blocks {
		if a.selected[i] {
			selected = append(selected, b)
		}
	}
	if len(selected) > 0 {
		setText(a.controls[210], engine.Text(selected))
		a.status(fmt.Sprintf("已选 %d 个文字块。点击“复制文字”或按 Ctrl+C。", len(selected)))
	} else {
		setText(a.controls[210], engine.Text(a.blocks))
		a.status("未选中文字，右侧显示全部识别结果")
	}
	invalidate(a.canvas)
}
func canvasProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case 0x14:
		return 1
	case 0xf:
		var ps paintstruct
		dc := call(user, "BeginPaint", hwnd, uintptr(unsafe.Pointer(&ps)))
		r := clientRect(hwnd)
		mem := call(gdi, "CreateCompatibleDC", dc)
		bmp := call(gdi, "CreateCompatibleBitmap", dc, u(int(r.R)), u(int(r.B)))
		old := call(gdi, "SelectObject", mem, bmp)
		fill(mem, r, rgb(234, 238, 243))
		if app.img == nil {
			drawText(mem, "把图片中的文字，留在图片里。", rect{20, r.B/2 - 45, r.R - 20, r.B / 2}, app.heading, rgb(83, 94, 111), 1|4|0x20)
			drawText(mem, "点击“截图”框选区域，或打开一张 PNG / JPEG 图片", rect{20, r.B/2 + 5, r.R - 20, r.B/2 + 50}, app.regular, rgb(121, 130, 144), 1|4|0x20)
		} else {
			pad := float64(app.d(16))
			app.render(mem, engine.Rect{X: pad, Y: pad, W: float64(r.R) - pad*2, H: float64(r.B) - pad*2}, true)
		}
		call(gdi, "BitBlt", dc, 0, 0, u(int(r.R)), u(int(r.B)), mem, 0, 0, 0x00cc0020)
		call(gdi, "SelectObject", mem, old)
		call(gdi, "DeleteObject", bmp)
		call(gdi, "DeleteDC", mem)
		call(user, "EndPaint", hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0
	case 0x201:
		if app.img == nil {
			return 0
		}
		call(user, "SetFocus", hwnd)
		p := cursorPoint(lp)
		app.drag = true
		app.dragStart = engine.Point{X: float64(p.X), Y: float64(p.Y)}
		app.dragEnd = app.dragStart
		call(user, "SetCapture", hwnd)
		return 0
	case 0x200:
		if app.drag {
			p := cursorPoint(lp)
			app.dragEnd = engine.Point{X: float64(p.X), Y: float64(p.Y)}
			invalidate(hwnd)
		}
		return 0
	case 0x202:
		if app.drag {
			p := cursorPoint(lp)
			app.dragEnd = engine.Point{X: float64(p.X), Y: float64(p.Y)}
			app.drag = false
			call(user, "ReleaseCapture")
			app.selection()
		}
		return 0
	case 0x215:
		app.drag = false
		invalidate(hwnd)
		return 0
	case 0x100:
		if wp == 'C' && int16(call(user, "GetKeyState", 0x11)) < 0 {
			app.copyText()
			return 0
		}
		if wp == 'A' && int16(call(user, "GetKeyState", 0x11)) < 0 {
			for i := range app.blocks {
				app.selected[i] = true
			}
			setText(app.controls[210], engine.Text(app.blocks))
			invalidate(hwnd)
			return 0
		}
	}
	return call(user, "DefWindowProcW", hwnd, uintptr(msg), wp, lp)
}
func (a *appState) startCapture() {
	if a.busy || a.capturing {
		return
	}
	a.capturing = true
	call(user, "ShowWindow", a.hwnd, 0)
	call(user, "SetTimer", a.hwnd, 1, 180, 0)
}
func (a *appState) finishCaptureStart() {
	img, bounds, err := desktop()
	if err != nil {
		a.capturing = false
		call(user, "ShowWindow", a.hwnd, 5)
		a.status(err.Error())
		return
	}
	a.capture = img
	a.capturePixels = imagePixels(img)
	a.captureRect = bounds
	a.captureStart = point{}
	a.captureEnd = point{}
	a.drag = false
	a.screen = create("GooesTranslateCapture", "gooestranslate 截图", 0x80000000, 0x8|0x80, int(bounds.L), int(bounds.T), int(bounds.R-bounds.L), int(bounds.B-bounds.T), 0, 0)
	if a.screen == 0 {
		a.endCapture(false)
		a.status("无法打开截图框选窗口")
		return
	}
	call(user, "ShowWindow", a.screen, 5)
	call(user, "SetForegroundWindow", a.screen)
	call(user, "SetFocus", a.screen)
}
func (a *appState) endCapture(accept bool) {
	if a.screen != 0 {
		call(user, "DestroyWindow", a.screen)
		a.screen = 0
	}
	call(user, "ReleaseCapture")
	call(user, "ShowWindow", a.hwnd, 5)
	call(user, "SetForegroundWindow", a.hwnd)
	a.capturing = false
	a.drag = false
	if accept && a.capture != nil {
		p, q := a.captureStart, a.captureEnd
		b := image.Rect(min(int(p.X), int(q.X)), min(int(p.Y), int(q.Y)), max(int(p.X), int(q.X)), max(int(p.Y), int(q.Y))).Intersect(a.capture.Bounds())
		if b.Dx() >= 4 && b.Dy() >= 4 {
			a.setImage(a.capture.SubImage(b))
			a.capture = nil
			a.capturePixels = nil
			a.recognize()
			return
		}
	}
	a.capture = nil
	a.capturePixels = nil
	a.status("截图已取消")
}
func captureProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case 0x14:
		return 1
	case 0xf:
		var ps paintstruct
		dc := call(user, "BeginPaint", hwnd, uintptr(unsafe.Pointer(&ps)))
		r := clientRect(hwnd)
		if app.capture != nil {
			b := app.capture.Bounds()
			paintPixels(dc, app.capturePixels, b.Dx(), b.Dy(), r)
		}
		bar := rect{r.R/2 - 230, 20, r.R/2 + 230, 64}
		fill(dc, bar, rgb(31, 39, 53))
		drawText(dc, "拖动框选截图区域  ·  Esc 取消", bar, app.regular, rgb(255, 255, 255), 1|4|0x20)
		if app.drag {
			p, q := app.captureStart, app.captureEnd
			box := rect{min(p.X, q.X), min(p.Y, q.Y), max(p.X, q.X), max(p.Y, q.Y)}
			for i := int32(0); i < 3; i++ {
				frame(dc, rect{box.L - i, box.T - i, box.R + i, box.B + i}, rgb(250, 131, 62))
			}
		}
		call(user, "EndPaint", hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0
	case 0x201:
		app.drag = true
		app.captureStart = cursorPoint(lp)
		app.captureEnd = app.captureStart
		call(user, "SetCapture", hwnd)
		invalidate(hwnd)
		return 0
	case 0x200:
		if app.drag {
			app.captureEnd = cursorPoint(lp)
			invalidate(hwnd)
		}
		return 0
	case 0x202:
		if app.drag {
			app.captureEnd = cursorPoint(lp)
			app.endCapture(true)
		}
		return 0
	case 0x100:
		if wp == 27 {
			app.endCapture(false)
		}
		return 0
	case 0x10:
		app.endCapture(false)
		return 0
	}
	return call(user, "DefWindowProcW", hwnd, uintptr(msg), wp, lp)
}
