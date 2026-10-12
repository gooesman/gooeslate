package main

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
)

const (
	resultWindowStyle   = uintptr(0x80000000 | 0x00040000 | 0x00080000 | 0x00020000 | 0x00010000 | 0x02000000)
	frameHitClient      = uintptr(1)
	frameHitLeft        = uintptr(10)
	frameHitRight       = uintptr(11)
	frameHitTop         = uintptr(12)
	frameHitTopLeft     = uintptr(13)
	frameHitTopRight    = uintptr(14)
	frameHitBottom      = uintptr(15)
	frameHitBottomLeft  = uintptr(16)
	frameHitBottomRight = uintptr(17)
)

func resultResizeInset(dpi int) int32 {
	if dpi <= 0 {
		dpi = 96
	}
	return int32(max(4, (6*dpi+48)/96))
}

// A small real non-client border keeps all eight resize handles reachable.
// The WebView child windows do not cover these handles or interfere with
// ordinary selection and image mouse events.
func resultClientBounds(proposed, work rect, maximized bool, inset int32) rect {
	if maximized {
		return rect{max(proposed.L, work.L), max(proposed.T, work.T), min(proposed.R, work.R), min(proposed.B, work.B)}
	}
	return rect{proposed.L + inset, proposed.T + inset, proposed.R - inset, proposed.B - inset}
}

func resultFrameHitTest(bounds rect, cursor point, inset int32, maximized bool) uintptr {
	if maximized || cursor.X < bounds.L || cursor.X >= bounds.R || cursor.Y < bounds.T || cursor.Y >= bounds.B {
		return frameHitClient
	}
	left, right := cursor.X < bounds.L+inset, cursor.X >= bounds.R-inset
	top, bottom := cursor.Y < bounds.T+inset, cursor.Y >= bounds.B-inset
	switch {
	case top && left:
		return frameHitTopLeft
	case top && right:
		return frameHitTopRight
	case bottom && left:
		return frameHitBottomLeft
	case bottom && right:
		return frameHitBottomRight
	case left:
		return frameHitLeft
	case right:
		return frameHitRight
	case top:
		return frameHitTop
	case bottom:
		return frameHitBottom
	default:
		return frameHitClient
	}
}

func resultMonitorAreas(bounds rect) (rect, rect) {
	var info struct {
		Size          uint32
		Monitor, Work rect
		Flags         uint32
	}
	info.Size = uint32(unsafe.Sizeof(info))
	monitor := call(user, "MonitorFromRect", uintptr(unsafe.Pointer(&bounds)), 2)
	if monitor != 0 && call(user, "GetMonitorInfoW", monitor, uintptr(unsafe.Pointer(&info))) != 0 {
		return info.Monitor, info.Work
	}
	return bounds, bounds
}

func resultMaximizeGeometry(monitor, work rect) (point, point) {
	return point{work.L - monitor.L, work.T - monitor.T}, point{work.R - work.L, work.B - work.T}
}

func handleResultFrame(w *webWindow, msg uint32, wp, lp uintptr, theme string) (uintptr, bool) {
	if w == nil || w.kind != "result" {
		return 0, false
	}
	hwnd := w.hwnd
	switch msg {
	case 0x83: // WM_NCCALCSIZE: both parameter forms start with a RECT.
		if lp == 0 {
			return 0, false
		}
		var proposed rect
		call(ntdll, "RtlMoveMemory", uintptr(unsafe.Pointer(&proposed)), lp, unsafe.Sizeof(proposed))
		_, work := resultMonitorAreas(proposed)
		bounds := resultClientBounds(proposed, work, call(user, "IsZoomed", hwnd) != 0, resultResizeInset(int(call(user, "GetDpiForWindow", hwnd))))
		call(ntdll, "RtlMoveMemory", lp, uintptr(unsafe.Pointer(&bounds)), unsafe.Sizeof(bounds))
		return 0, true
	case 0x84: // WM_NCHITTEST: preserve signed coordinates on negative monitors.
		var bounds rect
		call(user, "GetWindowRect", hwnd, uintptr(unsafe.Pointer(&bounds)))
		return resultFrameHitTest(bounds, cursorPoint(lp), resultResizeInset(int(call(user, "GetDpiForWindow", hwnd))), call(user, "IsZoomed", hwnd) != 0), true
	case 0x85: // WM_NCPAINT: only the resize rim; DWM retains the shadow/corners.
		if call(user, "IsZoomed", hwnd) != 0 || call(user, "IsIconic", hwnd) != 0 {
			return 0, true
		}
		dc := call(user, "GetWindowDC", hwnd)
		if dc != 0 {
			var bounds rect
			call(user, "GetWindowRect", hwnd, uintptr(unsafe.Pointer(&bounds)))
			width, height := bounds.R-bounds.L, bounds.B-bounds.T
			inset := resultResizeInset(int(call(user, "GetDpiForWindow", hwnd)))
			shade := rgb(247, 248, 250)
			if appUsesDarkAppearance(theme) {
				shade = rgb(23, 25, 29)
			}
			for _, edge := range []rect{{0, 0, width, inset}, {0, height - inset, width, height}, {0, inset, inset, height - inset}, {width - inset, inset, width, height - inset}} {
				fill(dc, edge, shade)
			}
			call(user, "ReleaseDC", hwnd, dc)
		}
		return 0, true
	case 0x86: // WM_NCACTIVATE: suppress default frame painting.
		return call(user, "DefWindowProcW", hwnd, uintptr(msg), wp, ^uintptr(0)), true
	}
	return 0, false
}

// SDK 1.0.2420.47 WebView2.h: ICoreWebView2Settings9 inherits 37 slots,
// then get/put_IsNonClientRegionSupportEnabled. Older runtimes use an HTML
// pointer fallback; capability must be enabled before navigating to the page.
func enableResultDragRegions(settings *edge.ICoreWebViewSettings) bool {
	if settings == nil {
		return false
	}
	iid := edge.NewGUID("{0528A73B-E92D-49F4-927A-E547DDDAA37D}")
	base := *(**[3]uintptr)(unsafe.Pointer(settings))
	var settings9 unsafe.Pointer
	hr, _, _ := syscall.SyscallN(base[0], uintptr(unsafe.Pointer(settings)), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&settings9)))
	if int32(hr) < 0 || settings9 == nil {
		return false
	}
	methods := *(**[39]uintptr)(settings9)
	defer syscall.SyscallN(methods[2], uintptr(settings9))
	hr, _, _ = syscall.SyscallN(methods[38], uintptr(settings9), 1)
	if int32(hr) < 0 {
		return false
	}
	var enabled int32
	hr, _, _ = syscall.SyscallN(methods[37], uintptr(settings9), uintptr(unsafe.Pointer(&enabled)))
	runtime.KeepAlive(settings)
	return int32(hr) >= 0 && enabled != 0
}

func (w *webWindow) resultWindowState() map[string]any {
	return map[string]any{"maximized": call(user, "IsZoomed", w.hwnd) != 0, "native_drag": w.dragRegions}
}

func (w *webWindow) pushWindowState() {
	if w != nil && w.kind == "result" && w.ready && w.browser != nil {
		w.browser.Eval("window.setWindowState&&window.setWindowState(" + jsonString(w.resultWindowState()) + ")")
	}
}

func (w *webWindow) toggleMaximized() {
	command := uintptr(3)
	if call(user, "IsZoomed", w.hwnd) != 0 {
		command = 9
	}
	call(user, "ShowWindow", w.hwnd, command)
	w.syncVisibility()
	w.pushWindowState()
}

func (w *webWindow) moveResultWindow() error {
	if call(user, "GetForegroundWindow") != w.hwnd || call(user, "GetAsyncKeyState", 1)&0x8000 == 0 {
		return nil
	}
	var cursor point
	if call(user, "GetCursorPos", uintptr(unsafe.Pointer(&cursor))) == 0 {
		return fmt.Errorf("无法移动窗口，请重试")
	}
	call(user, "ReleaseCapture")
	position := uintptr(uint32(uint16(cursor.X)) | uint32(uint16(cursor.Y))<<16)
	send(w.hwnd, 0xa1, 2, position) // Native move/snap loop: WM_NCLBUTTONDOWN / HTCAPTION.
	return nil
}

func (w *webWindow) showResultSystemMenu() {
	var cursor point
	if call(user, "GetCursorPos", uintptr(unsafe.Pointer(&cursor))) == 0 {
		return
	}
	menu := call(user, "GetSystemMenu", w.hwnd, 0)
	if menu == 0 {
		return
	}
	call(user, "SetForegroundWindow", w.hwnd)
	command := call(user, "TrackPopupMenu", menu, 0x100|2, u(int(cursor.X)), u(int(cursor.Y)), 0, w.hwnd, 0)
	if command != 0 {
		call(user, "PostMessageW", w.hwnd, 0x112, command, 0)
	}
}

func (w *webWindow) syncResultSystemMenu() {
	menu := call(user, "GetSystemMenu", w.hwnd, 0)
	if menu == 0 {
		return
	}
	zoomed, iconic := call(user, "IsZoomed", w.hwnd) != 0, call(user, "IsIconic", w.hwnd) != 0
	restore, move, maximize, minimize := uintptr(1), uintptr(0), uintptr(0), uintptr(0)
	if zoomed || iconic {
		restore, move = 0, 1
	}
	if zoomed {
		maximize = 1
	}
	if iconic {
		minimize = 1
	}
	call(user, "EnableMenuItem", menu, 0xf120, restore)
	call(user, "EnableMenuItem", menu, 0xf010, move)
	call(user, "EnableMenuItem", menu, 0xf000, move)
	call(user, "EnableMenuItem", menu, 0xf030, maximize)
	call(user, "EnableMenuItem", menu, 0xf020, minimize)
}

// resultFrameSnapshot verifies only the application's own HWND. The render
// smoke test can call this on its UI thread for normal and maximized phases.
func resultFrameSnapshot(w *webWindow) (map[string]any, error) {
	if w == nil || w.kind != "result" || w.hwnd == 0 {
		return nil, fmt.Errorf("翻译窗口未就绪")
	}
	style := call(user, "GetWindowLongPtrW", w.hwnd, ^uintptr(15))
	if style&0x00c00000 != 0 {
		return nil, fmt.Errorf("翻译窗口仍有系统标题栏")
	}
	var outer rect
	if call(user, "GetWindowRect", w.hwnd, uintptr(unsafe.Pointer(&outer))) == 0 {
		return nil, fmt.Errorf("无法读取窗口边界")
	}
	client := clientRect(w.hwnd)
	origin := point{}
	call(user, "ClientToScreen", w.hwnd, uintptr(unsafe.Pointer(&origin)))
	actual := rect{origin.X, origin.Y, origin.X + client.R - client.L, origin.Y + client.B - client.T}
	dpi := int(call(user, "GetDpiForWindow", w.hwnd))
	inset := resultResizeInset(dpi)
	maximized := call(user, "IsZoomed", w.hwnd) != 0
	_, work := resultMonitorAreas(outer)
	expected := resultClientBounds(outer, work, maximized, inset)
	if actual != expected {
		return nil, fmt.Errorf("无标题栏窗口客户区不正确：%v，期望 %v", actual, expected)
	}
	if maximized && actual != work {
		return nil, fmt.Errorf("最大化窗口未适配屏幕工作区")
	}
	// The inactive title area has no native caption; only the web header drags.
	borderHit := frameHitClient
	if !maximized {
		position := point{outer.L + 1, outer.T + (outer.B-outer.T)/2}
		packed := uintptr(uint32(uint16(position.X)) | uint32(uint16(position.Y))<<16)
		borderHit = send(w.hwnd, 0x84, 0, packed)
		if borderHit != frameHitLeft {
			return nil, fmt.Errorf("翻译窗口边缘无法缩放")
		}
	}
	return map[string]any{"caption_removed": true, "dpi": dpi, "resize_inset": inset, "maximized": maximized, "client": actual, "window": outer, "work_area": work, "native_drag_regions": w.dragRegions, "border_hit": borderHit}, nil
}

// MINMAXINFO layout: reserved, ptMaxSize, ptMaxPosition, minTrack, maxTrack.
func applyResultMaximizeGeometry(info *[5]point, monitor, work rect) {
	position, size := resultMaximizeGeometry(monitor, work)
	info[1], info[2] = size, position
}
