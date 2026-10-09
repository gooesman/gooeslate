package main

import (
	"fmt"
	"image"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

const captureTimerID = 7001

type captureBitmap struct {
	dc, bitmap, previous uintptr
}

func (b *captureBitmap) close() {
	if b.dc != 0 && b.previous != 0 {
		call(gdi, "SelectObject", b.dc, b.previous)
	}
	if b.bitmap != 0 {
		call(gdi, "DeleteObject", b.bitmap)
	}
	if b.dc != 0 {
		call(gdi, "DeleteDC", b.dc)
	}
	*b = captureBitmap{}
}

// Each bitmap is uploaded once. Mouse motion repaints with BitBlt rather than
// converting or resizing all virtual-desktop pixels on every mouse event.
func newCaptureBitmap(width, height int, pixels []byte) (captureBitmap, error) {
	b := captureBitmap{dc: call(gdi, "CreateCompatibleDC", 0)}
	if b.dc == 0 {
		return b, fmt.Errorf("无法创建截图缓存")
	}
	info := bitmapinfo{Header: bitmapheader{Size: 40, Width: int32(width), Height: -int32(height), Planes: 1, BitCount: 32}}
	var bits unsafe.Pointer
	b.bitmap = call(gdi, "CreateDIBSection", b.dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if b.bitmap == 0 || bits == nil {
		b.close()
		return b, fmt.Errorf("无法创建截图画布")
	}
	b.previous = call(gdi, "SelectObject", b.dc, b.bitmap)
	if len(pixels) > 0 {
		copy(unsafe.Slice((*byte)(bits), width*height*4), pixels)
		runtime.KeepAlive(pixels)
	}
	return b, nil
}

type CaptureController struct {
	owner, hwnd         uintptr
	onCrop              func(*image.RGBA, string)
	onCancel            func()
	mode                string
	active, waiting     bool
	ownerWasVisible     bool
	ownerWasMinimized   bool
	snapshot            *image.RGBA
	bounds              image.Rectangle
	start, end          image.Point
	dragging, ready     bool
	pressedAction       string
	hoverAction         string
	scale               float64
	textFont, hintFont  uintptr
	original, dim, back captureBitmap
	lastError           error
}

var captureRegistration sync.Once
var captureRegistrationError error
var captureWindows = map[uintptr]*CaptureController{}
var creatingCapture *CaptureController

func NewCaptureController(owner uintptr, onCrop func(*image.RGBA, string), onCancel func()) *CaptureController {
	captureRegistration.Do(func() {
		captureRegistrationError = register("GooesTranslateCaptureV2", syscall.NewCallback(captureWindowProc), 32515)
	})
	return &CaptureController{owner: owner, onCrop: onCrop, onCancel: onCancel, scale: 1}
}

func (c *CaptureController) Active() bool     { return c.active }
func (c *CaptureController) LastError() error { return c.lastError }

func (c *CaptureController) Start(mode string) error {
	if c.active {
		return nil
	}
	if captureRegistrationError != nil {
		return captureRegistrationError
	}
	switch mode {
	case "choose", "ocr", "translate", "image":
	default:
		return fmt.Errorf("截图操作不支持")
	}
	c.lastError = nil
	c.mode, c.active, c.waiting = mode, true, true
	c.ownerWasVisible = call(user, "IsWindowVisible", c.owner) != 0
	c.ownerWasMinimized = call(user, "IsIconic", c.owner) != 0
	if c.ownerWasVisible {
		call(user, "ShowWindow", c.owner, 0)
	}
	// Wait one compositor frame after hiding settings/results so our own window
	// is not included in the frozen screenshot.
	if call(user, "SetTimer", c.owner, captureTimerID, 100, 0) == 0 {
		c.release(true)
		return fmt.Errorf("无法启动截图")
	}
	return nil
}

func (c *CaptureController) HandleTimer() {
	call(user, "KillTimer", c.owner, captureTimerID)
	if !c.active || !c.waiting {
		return
	}
	c.waiting = false
	snapshot, virtual, err := desktop()
	if err == nil {
		err = c.prepare(snapshot, image.Rect(int(virtual.L), int(virtual.T), int(virtual.R), int(virtual.B)))
	}
	if err != nil {
		c.fail(err)
		return
	}
	creatingCapture = c
	c.hwnd = create("GooesTranslateCaptureV2", "gooestranslate · 框选截图", 0x80000000, 0x8|0x80, c.bounds.Min.X, c.bounds.Min.Y, c.bounds.Dx(), c.bounds.Dy(), 0, 0)
	creatingCapture = nil
	if c.hwnd == 0 {
		c.fail(fmt.Errorf("无法打开截图框选窗口"))
		return
	}
	captureWindows[c.hwnd] = c
	dpi := call(user, "GetDpiForWindow", c.hwnd)
	if dpi > 0 {
		c.scale = float64(dpi) / 96
	}
	c.textFont, c.hintFont = font(c.size(14), false), font(c.size(12), false)
	call(user, "ShowWindow", c.hwnd, 5)
	call(user, "SetForegroundWindow", c.hwnd)
	call(user, "SetFocus", c.hwnd)
	call(user, "UpdateWindow", c.hwnd)
}

func (c *CaptureController) prepare(snapshot *image.RGBA, bounds image.Rectangle) error {
	if snapshot == nil || snapshot.Bounds().Empty() || snapshot.Bounds().Dx() != bounds.Dx() || snapshot.Bounds().Dy() != bounds.Dy() {
		return fmt.Errorf("截图尺寸不正确")
	}
	c.snapshot, c.bounds = snapshot, bounds
	c.start, c.end = image.Point{}, image.Point{}
	c.dragging, c.ready = false, false
	c.pressedAction, c.hoverAction = "", ""
	c.scale = 1
	w, h := snapshot.Bounds().Dx(), snapshot.Bounds().Dy()
	pixels := make([]byte, w*h*4)
	dimmed := make([]byte, len(pixels))
	for y := 0; y < h; y++ {
		source := snapshot.Pix[y*snapshot.Stride:]
		for x := 0; x < w; x++ {
			i, j := (y*w+x)*4, x*4
			pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = source[j+2], source[j+1], source[j], 255
			dimmed[i], dimmed[i+1], dimmed[i+2], dimmed[i+3] = byte(int(pixels[i])*11/20), byte(int(pixels[i+1])*11/20), byte(int(pixels[i+2])*11/20), 255
		}
	}
	var err error
	c.original, err = newCaptureBitmap(w, h, pixels)
	if err == nil {
		c.dim, err = newCaptureBitmap(w, h, dimmed)
	}
	if err == nil {
		c.back, err = newCaptureBitmap(w, h, nil)
	}
	if err != nil {
		c.original.close()
		c.dim.close()
		c.back.close()
	}
	return err
}

func (c *CaptureController) size(n int) int { return max(1, int(float64(n)*c.scale+.5)) }
func (c *CaptureController) selected() image.Rectangle {
	if c.snapshot == nil {
		return image.Rectangle{}
	}
	return captureSelection(c.start, c.end, c.snapshot.Bounds())
}
func (c *CaptureController) actions() []captureAction {
	if !c.ready || c.mode != "choose" {
		return nil
	}
	return captureToolbar(c.selected(), c.snapshot.Bounds(), c.scale)
}
func (c *CaptureController) pointer(lp uintptr) image.Point {
	// GetCursorPos handles desktops wider than WM_MOUSEMOVE's signed 16-bit
	// packed coordinates and monitors with a negative origin.
	var p point
	if call(user, "GetCursorPos", uintptr(unsafe.Pointer(&p))) != 0 && call(user, "ScreenToClient", c.hwnd, uintptr(unsafe.Pointer(&p))) != 0 {
		return image.Pt(int(p.X), int(p.Y))
	}
	p = cursorPoint(lp)
	return image.Pt(int(p.X), int(p.Y))
}

func captureWinRect(r image.Rectangle) rect {
	return rect{int32(r.Min.X), int32(r.Min.Y), int32(r.Max.X), int32(r.Max.Y)}
}

func (c *CaptureController) dimensionsRect() image.Rectangle {
	r := c.selected()
	if r.Empty() {
		return image.Rectangle{}
	}
	w, h := c.size(132), c.size(27)
	x := min(r.Min.X, c.snapshot.Bounds().Max.X-w-4)
	y := r.Min.Y - h - c.size(6)
	if y < 4 {
		y = r.Min.Y + c.size(5)
	}
	return image.Rect(max(4, x), max(4, y), max(4, x)+w, max(4, y)+h)
}
func (c *CaptureController) dirty() image.Rectangle {
	r := c.selected().Inset(-c.size(4))
	r = r.Union(c.dimensionsRect())
	for _, a := range c.actions() {
		r = r.Union(a.Bounds.Inset(-c.size(8)))
	}
	return r
}
func (c *CaptureController) refresh(old image.Rectangle) {
	r := old.Union(c.dirty()).Intersect(c.snapshot.Bounds())
	wr := captureWinRect(r)
	call(user, "InvalidateRect", c.hwnd, uintptr(unsafe.Pointer(&wr)), 0)
}

func (c *CaptureController) paint(dc uintptr, invalid image.Rectangle) {
	if c.snapshot == nil || c.back.dc == 0 {
		return
	}
	state := call(gdi, "SaveDC", c.back.dc)
	defer call(gdi, "RestoreDC", c.back.dc, state)
	call(gdi, "IntersectClipRect", c.back.dc, u(invalid.Min.X), u(invalid.Min.Y), u(invalid.Max.X), u(invalid.Max.Y))
	call(gdi, "BitBlt", c.back.dc, u(invalid.Min.X), u(invalid.Min.Y), u(invalid.Dx()), u(invalid.Dy()), c.dim.dc, u(invalid.Min.X), u(invalid.Min.Y), 0x00cc0020)
	r := c.selected()
	if !r.Empty() {
		call(gdi, "BitBlt", c.back.dc, u(r.Min.X), u(r.Min.Y), u(r.Dx()), u(r.Dy()), c.original.dc, u(r.Min.X), u(r.Min.Y), 0x00cc0020)
		frame(c.back.dc, captureWinRect(r.Inset(-1)), rgb(255, 155, 89))
		frame(c.back.dc, captureWinRect(r.Inset(-2)), rgb(255, 155, 89))
		label := c.dimensionsRect()
		fill(c.back.dc, captureWinRect(label), rgb(27, 31, 42))
		drawText(c.back.dc, fmt.Sprintf("%d × %d", r.Dx(), r.Dy()), captureWinRect(label), c.hintFont, rgb(240, 244, 255), 1|4|0x20)
	}
	actions := c.actions()
	if len(actions) > 0 {
		background := actions[0].Bounds.Union(actions[len(actions)-1].Bounds).Inset(-c.size(6))
		fill(c.back.dc, captureWinRect(background), rgb(25, 29, 39))
		frame(c.back.dc, captureWinRect(background), rgb(65, 73, 91))
		for _, action := range actions {
			color := rgb(25, 29, 39)
			if action.Mode == "image" {
				color = rgb(255, 155, 89)
			}
			if action.Mode == c.hoverAction {
				color = rgb(64, 55, 48)
				if action.Mode == "image" {
					color = rgb(255, 173, 118)
				}
			}
			fill(c.back.dc, captureWinRect(action.Bounds), color)
			textColor := rgb(242, 245, 252)
			if action.Mode == "image" {
				textColor = rgb(40, 26, 16)
			}
			drawText(c.back.dc, action.Label, captureWinRect(action.Bounds), c.textFont, textColor, 1|4|0x20)
		}
	}
	if !c.dragging && !c.ready {
		// Place the hint on the monitor containing the pointer, rather than on
		// the far-left monitor of the entire virtual desktop.
		var pointer point
		call(user, "GetCursorPos", uintptr(unsafe.Pointer(&pointer)))
		monitor := call(user, "MonitorFromPoint", uintptr(uint64(uint32(pointer.X))|uint64(uint32(pointer.Y))<<32), 2)
		var info struct {
			Size          uint32
			Monitor, Work rect
			Flags         uint32
		}
		info.Size = uint32(unsafe.Sizeof(info))
		area := c.snapshot.Bounds()
		if call(user, "GetMonitorInfoW", monitor, uintptr(unsafe.Pointer(&info))) != 0 {
			area = image.Rect(int(info.Work.L)-c.bounds.Min.X, int(info.Work.T)-c.bounds.Min.Y, int(info.Work.R)-c.bounds.Min.X, int(info.Work.B)-c.bounds.Min.Y)
		}
		hint := image.Rect(area.Min.X+c.size(24), area.Min.Y+c.size(24), area.Min.X+c.size(580), area.Min.Y+c.size(62)).Intersect(c.snapshot.Bounds())
		fill(c.back.dc, captureWinRect(hint), rgb(27, 31, 42))
		instruction := "拖动框选区域   ·   松开鼠标后选择操作   ·   Esc 取消"
		switch c.mode {
		case "ocr":
			instruction = "拖动框选区域   ·   松开鼠标后识别文字   ·   Esc 取消"
		case "translate":
			instruction = "拖动框选区域   ·   松开鼠标后翻译文字   ·   Esc 取消"
		case "image":
			instruction = "拖动框选区域   ·   松开鼠标后原图翻译   ·   Esc 取消"
		}
		drawText(c.back.dc, instruction, captureWinRect(hint), c.textFont, rgb(245, 247, 255), 1|4|0x20)
	}
	call(gdi, "BitBlt", dc, u(invalid.Min.X), u(invalid.Min.Y), u(invalid.Dx()), u(invalid.Dy()), c.back.dc, u(invalid.Min.X), u(invalid.Min.Y), 0x00cc0020)
}

func (c *CaptureController) fail(err error) {
	c.lastError = err
	c.release(true)
	call(user, "MessageBoxW", c.owner, uintptr(unsafe.Pointer(str(err.Error()))), uintptr(unsafe.Pointer(str("gooestranslate · 截图"))), 0x10)
	if c.onCancel != nil {
		c.onCancel()
	}
}

func (c *CaptureController) accept(mode string) {
	crop := captureCrop(c.snapshot, c.selected())
	if crop == nil {
		return
	}
	c.release(false)
	if c.onCrop != nil {
		c.onCrop(crop, mode)
	}
}

func (c *CaptureController) cancel() {
	if !c.active {
		return
	}
	c.release(true)
	if c.onCancel != nil {
		c.onCancel()
	}
}

func (c *CaptureController) release(restore bool) {
	call(user, "KillTimer", c.owner, captureTimerID)
	c.active, c.waiting, c.dragging, c.ready = false, false, false, false
	call(user, "ReleaseCapture")
	if c.hwnd != 0 {
		h := c.hwnd
		c.hwnd = 0
		delete(captureWindows, h)
		call(user, "DestroyWindow", h)
	}
	for _, f := range []uintptr{c.textFont, c.hintFont} {
		if f != 0 {
			call(gdi, "DeleteObject", f)
		}
	}
	c.textFont, c.hintFont = 0, 0
	c.original.close()
	c.dim.close()
	c.back.close()
	c.snapshot = nil
	if restore && c.ownerWasVisible {
		if c.ownerWasMinimized {
			// SW_SHOWMINNOACTIVE restores the minimized taskbar state without
			// opening settings or moving focus away from the user's application.
			call(user, "ShowWindow", c.owner, 7)
		} else {
			call(user, "ShowWindow", c.owner, 5)
			call(user, "SetForegroundWindow", c.owner)
		}
	}
	c.ownerWasVisible = false
	c.ownerWasMinimized = false
}

func (c *CaptureController) Close() {
	c.release(false)
}

func captureWindowProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	c := captureWindows[hwnd]
	if c == nil {
		c = creatingCapture
	}
	if c == nil {
		return call(user, "DefWindowProcW", hwnd, uintptr(msg), wp, lp)
	}
	switch msg {
	case 0x14:
		return 1
	case 0xf:
		var ps paintstruct
		dc := call(user, "BeginPaint", hwnd, uintptr(unsafe.Pointer(&ps)))
		c.paint(dc, image.Rect(int(ps.Paint.L), int(ps.Paint.T), int(ps.Paint.R), int(ps.Paint.B)))
		call(user, "EndPaint", hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0
	case 0x201:
		p := c.pointer(lp)
		c.pressedAction = captureActionAt(c.actions(), p)
		if c.pressedAction != "" {
			return 0
		}
		c.start, c.end = p, p
		c.ready, c.dragging = false, true
		c.hoverAction = ""
		call(user, "SetCapture", hwnd)
		// Remove the initial hint and any prior selection with a single repaint.
		invalidate(hwnd)
		return 0
	case 0x200:
		p := c.pointer(lp)
		if c.dragging {
			old := c.dirty()
			c.end = p
			c.refresh(old)
		} else {
			hover := captureActionAt(c.actions(), p)
			if hover != c.hoverAction {
				c.hoverAction = hover
				c.refresh(c.dirty())
			}
		}
		return 0
	case 0x202:
		p := c.pointer(lp)
		if c.pressedAction != "" {
			mode := captureActionAt(c.actions(), p)
			pressed := c.pressedAction
			c.pressedAction = ""
			if mode == pressed {
				if mode == "cancel" {
					c.cancel()
				} else {
					c.accept(mode)
				}
			}
			return 0
		}
		if c.dragging {
			old := c.dirty()
			c.end, c.dragging = p, false
			call(user, "ReleaseCapture")
			r := c.selected()
			c.ready = r.Dx() >= 3 && r.Dy() >= 3
			if c.ready && c.mode != "choose" {
				c.accept(c.mode)
			} else {
				c.refresh(old)
			}
		}
		return 0
	case 0x205, 0x10:
		c.cancel()
		return 0
	case 0x100:
		mode := captureShortcut(wp, c.ready)
		if mode == "cancel" {
			c.cancel()
		} else if mode != "" {
			if c.mode != "choose" {
				mode = c.mode
			}
			c.accept(mode)
		}
		return 0
	case 0x215:
		// ReleaseCapture after mouse-up is normal; losing capture while dragging
		// leaves the current selection available for another drag.
		if c.dragging {
			c.dragging = false
			c.ready = c.selected().Dx() >= 3 && c.selected().Dy() >= 3
			invalidate(hwnd)
		}
		return 0
	}
	return call(user, "DefWindowProcW", hwnd, uintptr(msg), wp, lp)
}
