package main

import (
	"fmt"
	"image"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	selectionButtonTimer  = 1
	selectionMouseMessage = 0x8000 + 74
)

type selectionMouseInput struct {
	Point             point
	Data, Flags, Time uint32
	Extra             uintptr
}

type selectionQuery struct {
	target     uintptr
	anchor     image.Point
	generation uint64
	translate  bool
}

type selectionMouseEvent struct {
	message uint32
	input   selectionMouseInput
}

// SelectionController owns an optional, passive mouse-selection helper. All
// public methods and posted callbacks run on the application's UI thread.
// The only background work is an accessibility read; translating requires an
// explicit click and never runs from a mouse hook or from merely selecting text.
type SelectionController struct {
	owner, hwnd, hook, hookProc uintptr
	processID                   uint32
	post                        func(func())
	onTranslate                 func(string, uintptr)
	foreground                  func() uintptr
	readSelection               func(uintptr, bool) (string, error)
	enabled, showButton, closed bool
	visible, pressed            bool
	buttonBounds                image.Rectangle
	selectedText                string
	selectedTarget              uintptr
	expires                     time.Time
	textFont                    uintptr
	fontSize                    int
	gesture                     selectionGesture
	generation                  atomic.Uint64
	queries                     chan selectionQuery
	mouseEvents                 chan selectionMouseEvent
	mouseOverflow               atomic.Bool
	wakeMouse                   func() bool
	done                        chan struct{}
	lastError                   error
}

var selectionRegistration sync.Once
var selectionRegistrationError error
var selectionWindows = map[uintptr]*SelectionController{}
var creatingSelection *SelectionController

func NewSelectionController(owner uintptr, post func(func()), onTranslate func(text string, target uintptr)) (*SelectionController, error) {
	if post == nil || onTranslate == nil {
		return nil, fmt.Errorf("划词助手未能连接到翻译窗口")
	}
	selectionRegistration.Do(func() {
		selectionRegistrationError = register("GooesTranslateSelectionButton", syscall.NewCallback(selectionButtonProc), 32649)
	})
	if selectionRegistrationError != nil {
		return nil, selectionRegistrationError
	}
	c := &SelectionController{owner: owner, post: post, onTranslate: onTranslate, processID: uint32(call(kernel, "GetCurrentProcessId")), queries: make(chan selectionQuery, 1), mouseEvents: make(chan selectionMouseEvent, 32), done: make(chan struct{}), foreground: func() uintptr { return call(user, "GetForegroundWindow") }, readSelection: readSelectedText}
	// The tool window is deliberately unowned: minimizing the settings window
	// must not suppress a selection button in another foreground application.
	creatingSelection = c
	c.hwnd = create("GooesTranslateSelectionButton", "划词翻译", 0x80000000, 0x8|0x80|0x08000000, 0, 0, 32, 32, 0, 0)
	creatingSelection = nil
	if c.hwnd == 0 {
		return nil, fmt.Errorf("无法创建划词翻译按钮")
	}
	selectionWindows[c.hwnd] = c
	c.wakeMouse = func() bool { return call(user, "PostMessageW", c.hwnd, selectionMouseMessage, 0, 0) != 0 }
	c.hookProc = syscall.NewCallback(func(code, msg, data uintptr) uintptr {
		if int32(code) >= 0 && data != 0 {
			switch uint32(msg) {
			case 0x0201, 0x0202, 0x0204, 0x0207, 0x020a, 0x020b, 0x020e:
				var input selectionMouseInput
				call(ntdll, "RtlMoveMemory", uintptr(unsafe.Pointer(&input)), data, unsafe.Sizeof(input))
				if input.Flags&1 == 0 {
					// A bounded channel plus PostMessage cannot block when the app's
					// normal UI callback queue is full. UIA and HTTP never run here.
					c.queueMouse(selectionMouseEvent{message: uint32(msg), input: input})
				}
			}
		}
		return call(user, "CallNextHookEx", 0, code, msg, data)
	})
	go c.queryWorker()
	return c, nil
}

func (c *SelectionController) queueMouse(event selectionMouseEvent) {
	select {
	case c.mouseEvents <- event:
	default:
		c.mouseOverflow.Store(true)
		c.generation.Add(1)
	}
	if !c.wakeMouse() {
		c.mouseOverflow.Store(true)
		c.generation.Add(1)
	}
}

func (c *SelectionController) processMouseQueue() {
	overflow := c.mouseOverflow.Swap(false)
	if overflow {
		c.Hide()
	}
	for {
		select {
		case event := <-c.mouseEvents:
			if !overflow {
				c.handleMouse(event.message, event.input)
			}
		default:
			return
		}
	}
}

func (c *SelectionController) LastError() error { return c.lastError }

func (c *SelectionController) SetEnabled(enabled, showButton bool) {
	if c.closed {
		return
	}
	c.enabled, c.showButton = enabled, showButton
	if !enabled || !showButton {
		c.Hide()
		if c.hook != 0 {
			call(user, "UnhookWindowsHookEx", c.hook)
			c.hook = 0
		}
		return
	}
	if c.hook == 0 {
		c.hook = call(user, "SetWindowsHookExW", 14, c.hookProc, call(kernel, "GetModuleHandleW", 0), 0)
		if c.hook == 0 {
			c.lastError = fmt.Errorf("无法启用鼠标划词按钮，可使用划词翻译快捷键")
			return
		}
	}
	c.lastError = nil
}

func (c *SelectionController) Hide() {
	c.gesture = selectionGesture{}
	c.hideButton()
}

func (c *SelectionController) hideButton() {
	c.generation.Add(1)
	c.visible, c.pressed = false, false
	c.selectedText, c.selectedTarget = "", 0
	c.buttonBounds = image.Rectangle{}
	if c.hwnd != 0 {
		call(user, "KillTimer", c.hwnd, selectionButtonTimer)
		if call(user, "GetCapture") == c.hwnd {
			call(user, "ReleaseCapture")
		}
		call(user, "ShowWindow", c.hwnd, 0)
	}
}

func (c *SelectionController) Close() {
	if c.closed {
		return
	}
	c.closed = true
	c.Hide()
	if c.hook != 0 {
		call(user, "UnhookWindowsHookEx", c.hook)
		c.hook = 0
	}
	close(c.done)
	if c.hwnd != 0 {
		call(user, "DestroyWindow", c.hwnd)
		delete(selectionWindows, c.hwnd)
		c.hwnd = 0
	}
	if c.textFont != 0 {
		call(gdi, "DeleteObject", c.textFont)
		c.textFont = 0
	}
}

func (c *SelectionController) ownWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	var process uint32
	call(user, "GetWindowThreadProcessId", hwnd, uintptr(unsafe.Pointer(&process)))
	return process == c.processID
}

func (c *SelectionController) handleMouse(msg uint32, input selectionMouseInput) {
	if c.closed || !c.enabled || !c.showButton || c.hook == 0 {
		return
	}
	p := image.Pt(int(input.Point.X), int(input.Point.Y))
	// A click on the NOACTIVATE button keeps the original foreground selection.
	// Its own window procedure handles that click without generating a new query.
	if c.visible && p.In(c.buttonBounds) && (msg == 0x0201 || msg == 0x0202) {
		return
	}
	if msg != 0x0201 && msg != 0x0202 {
		c.Hide()
		return
	}
	foreground := call(user, "GetForegroundWindow")
	if msg == 0x0201 {
		c.hideButton()
		if c.ownWindow(foreground) {
			c.gesture = selectionGesture{}
			return
		}
		c.gesture.begin(p, foreground)
		return
	}
	if c.ownWindow(foreground) {
		c.Hide()
		return
	}
	drag := image.Pt(max(2, int(call(user, "GetSystemMetrics", 68))/2), max(2, int(call(user, "GetSystemMetrics", 69))/2))
	double := image.Pt(max(2, int(call(user, "GetSystemMetrics", 36))/2), max(2, int(call(user, "GetSystemMetrics", 37))/2))
	if !c.gesture.end(p, foreground, input.Time, uint32(call(user, "GetDoubleClickTime")), drag, double) {
		return
	}
	request := selectionQuery{target: foreground, anchor: p, generation: c.generation.Add(1)}
	c.enqueueQuery(request)
}

func (c *SelectionController) enqueueQuery(request selectionQuery) {
	// Keep only the newest pending selection while one accessibility query is
	// running. This bounds work even if an application's UIA provider is slow.
	select {
	case c.queries <- request:
	default:
		select {
		case <-c.queries:
		default:
		}
		select {
		case c.queries <- request:
		default:
		}
	}
}

func (c *SelectionController) queryWorker() {
	for {
		var request selectionQuery
		select {
		case <-c.done:
			return
		case request = <-c.queries:
		}
		if !request.translate {
			// Let the application's mouse-up selection update before reading it.
			timer := time.NewTimer(140 * time.Millisecond)
			select {
			case <-c.done:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		selected, ok := c.readQuery(request)
		if !ok {
			continue
		}
		select {
		case <-c.done:
			return
		default:
		}
		c.post(func() {
			if selectionResultCurrent(c.enabled && c.showButton, c.closed, c.generation.Load(), request.generation, request.target, call(user, "GetForegroundWindow")) && !c.ownWindow(request.target) {
				if request.translate {
					c.onTranslate(selected, request.target)
				} else {
					c.show(request, selected)
				}
			}
		})
	}
}

func (c *SelectionController) readQuery(request selectionQuery) (string, bool) {
	if request.target == 0 || c.generation.Load() != request.generation || c.foreground() != request.target {
		return "", false
	}
	selected, err := c.readSelection(request.target, false)
	if err != nil || strings.TrimSpace(selected) == "" {
		return "", false
	}
	return selected, true
}

func selectionMonitorWorkArea(anchor image.Point) image.Rectangle {
	area := rect{int32(anchor.X), int32(anchor.Y), int32(anchor.X + 1), int32(anchor.Y + 1)}
	monitor := call(user, "MonitorFromRect", uintptr(unsafe.Pointer(&area)), 2)
	info := struct {
		Size          uint32
		Monitor, Work rect
		Flags         uint32
	}{}
	info.Size = uint32(unsafe.Sizeof(info))
	if monitor == 0 || call(user, "GetMonitorInfoW", monitor, uintptr(unsafe.Pointer(&info))) == 0 {
		return image.Rectangle{}
	}
	return image.Rect(int(info.Work.L), int(info.Work.T), int(info.Work.R), int(info.Work.B))
}

func (c *SelectionController) show(request selectionQuery, selected string) {
	work := selectionMonitorWorkArea(request.anchor)
	if work.Empty() {
		return
	}
	// Position first so per-monitor DPI is computed for the selected monitor.
	call(user, "SetWindowPos", c.hwnd, ^uintptr(0), u(request.anchor.X), u(request.anchor.Y), 0, 0, 0x1|0x10)
	dpi := int(call(user, "GetDpiForWindow", c.hwnd))
	if dpi < 96 {
		dpi = 96
	}
	size, gap := max(32, 32*dpi/96), max(10, 10*dpi/96)
	bounds := selectionButtonBounds(request.anchor, work, size, gap)
	if bounds.Empty() {
		return
	}
	fontSize := 18 * dpi / 96
	if fontSize != c.fontSize {
		if c.textFont != 0 {
			call(gdi, "DeleteObject", c.textFont)
		}
		c.textFont, c.fontSize = font(fontSize, true), fontSize
	}
	c.buttonBounds, c.selectedText, c.selectedTarget = bounds, selected, request.target
	c.visible, c.pressed, c.expires = true, false, time.Now().Add(9*time.Second)
	call(user, "SetWindowPos", c.hwnd, ^uintptr(0), u(bounds.Min.X), u(bounds.Min.Y), u(bounds.Dx()), u(bounds.Dy()), 0x10|0x40)
	call(user, "ShowWindow", c.hwnd, 4) // SW_SHOWNOACTIVATE
	invalidate(c.hwnd)
	if call(user, "SetTimer", c.hwnd, selectionButtonTimer, 100, 0) == 0 {
		c.Hide()
	}
}

func selectionButtonProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	c := selectionWindows[hwnd]
	if c == nil && msg == 0x0081 && creatingSelection != nil {
		c = creatingSelection
		selectionWindows[hwnd] = c
	}
	if c == nil {
		return call(user, "DefWindowProcW", hwnd, uintptr(msg), wp, lp)
	}
	switch msg {
	case selectionMouseMessage:
		c.processMouseQueue()
		return 0
	case 0x0021: // MA_NOACTIVATE: preserve foreground text and selection.
		return 3
	case 0x0201:
		if c.visible && c.selectedTarget == call(user, "GetForegroundWindow") {
			c.pressed = true
			call(user, "SetCapture", hwnd)
			invalidate(hwnd)
		}
		return 0
	case 0x0202:
		p := cursorPoint(lp)
		r := clientRect(hwnd)
		activate := c.pressed && c.visible && image.Pt(int(p.X), int(p.Y)).In(image.Rect(0, 0, int(r.R), int(r.B))) && c.selectedTarget == call(user, "GetForegroundWindow")
		target := c.selectedTarget
		c.Hide()
		if activate {
			// The selection can have changed with Shift+arrows or Ctrl+A inside
			// the same foreground window. Never translate the cached old text.
			c.enqueueQuery(selectionQuery{target: target, generation: c.generation.Load(), translate: true})
		}
		return 0
	case 0x0215: // capture changed
		c.pressed = false
		invalidate(hwnd)
		return 0
	case 0x0113:
		if c.visible && (call(user, "GetForegroundWindow") != c.selectedTarget || call(user, "GetAsyncKeyState", 27)&0x8001 != 0 || time.Now().After(c.expires)) {
			c.Hide()
		}
		return 0
	case 0x0014:
		return 1
	case 0x000f:
		var paint paintstruct
		dc := call(user, "BeginPaint", hwnd, uintptr(unsafe.Pointer(&paint)))
		if dc != 0 {
			r := clientRect(hwnd)
			color := rgb(255, 152, 72)
			if c.pressed {
				color = rgb(225, 122, 43)
			}
			fill(dc, r, color)
			frame(dc, r, rgb(187, 100, 40))
			drawText(dc, "译", r, c.textFont, rgb(35, 24, 15), 0x1|0x4|0x20)
			call(user, "EndPaint", hwnd, uintptr(unsafe.Pointer(&paint)))
		}
		return 0
	case 0x0082:
		delete(selectionWindows, hwnd)
	}
	return call(user, "DefWindowProcW", hwnd, uintptr(msg), wp, lp)
}
