package main

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
)

func TestResultResizeHandlesAtEveryDPIAndNegativeMonitor(t *testing.T) {
	bounds := rect{-1600, -300, -600, 500}
	for _, dpi := range []int{0, 96, 120, 144, 192, 288} {
		inset := resultResizeInset(dpi)
		x, y := bounds.L+(bounds.R-bounds.L)/2, bounds.T+(bounds.B-bounds.T)/2
		for _, fixture := range []struct {
			cursor point
			want   uintptr
		}{
			{point{bounds.L + 1, y}, frameHitLeft},
			{point{bounds.R - 1, y}, frameHitRight},
			{point{x, bounds.T + 1}, frameHitTop},
			{point{x, bounds.B - 1}, frameHitBottom},
			{point{bounds.L + 1, bounds.T + 1}, frameHitTopLeft},
			{point{bounds.R - 1, bounds.T + 1}, frameHitTopRight},
			{point{bounds.L + 1, bounds.B - 1}, frameHitBottomLeft},
			{point{bounds.R - 1, bounds.B - 1}, frameHitBottomRight},
			{point{x, y}, frameHitClient},
			{point{bounds.L + inset, bounds.T + inset}, frameHitClient},
		} {
			if got := resultFrameHitTest(bounds, fixture.cursor, inset, false); got != fixture.want {
				t.Fatalf("DPI %d %v: got %d want %d", dpi, fixture.cursor, got, fixture.want)
			}
			if got := resultFrameHitTest(bounds, fixture.cursor, inset, true); got != frameHitClient {
				t.Fatal("maximized windows must not expose resize handles")
			}
		}
	}
	for _, cursor := range []point{{bounds.L - 1, 0}, {bounds.R, 0}, {-1200, bounds.T - 1}, {-1200, bounds.B}} {
		if got := resultFrameHitTest(bounds, cursor, 6, false); got != frameHitClient {
			t.Fatal("outside coordinates must not become a resize handle")
		}
	}
}

func TestResultClientAndMaximizeRespectTaskbarOnAnyMonitorEdge(t *testing.T) {
	monitor := rect{-1600, -900, 0, 0}
	for _, work := range []rect{{-1600, -860, 0, 0}, {-1550, -900, 0, 0}, {-1600, -900, -60, 0}, {-1600, -900, 0, -50}} {
		position, size := resultMaximizeGeometry(monitor, work)
		got := rect{monitor.L + position.X, monitor.T + position.Y, monitor.L + position.X + size.X, monitor.T + position.Y + size.Y}
		if got != work {
			t.Fatalf("maximized outer window %v covers a taskbar beyond %v", got, work)
		}
		proposed := rect{monitor.L - 12, monitor.T - 12, monitor.R + 12, monitor.B + 12}
		if got := resultClientBounds(proposed, work, true, 12); got != work {
			t.Fatalf("maximized client %v != work %v", got, work)
		}
	}
	proposed := rect{-1400, -700, -400, -100}
	if got := resultClientBounds(proposed, monitor, false, 9); got != (rect{-1391, -691, -409, -109}) {
		t.Fatalf("normal DPI border misplaced: %v", got)
	}
}

func TestResultFrameNeverChangesSettingsOrCaptionStyle(t *testing.T) {
	for _, window := range []*webWindow{nil, {kind: "settings"}} {
		for _, message := range []uint32{0x83, 0x84, 0x85, 0x86} {
			if _, handled := handleResultFrame(window, message, 0, 0, "dark"); handled {
				t.Fatal("settings/native windows must retain default frame handling")
			}
		}
	}
	if resultWindowStyle&0x00c00000 != 0 || resultWindowStyle&0x00040000 == 0 || resultWindowStyle&0x00080000 == 0 || resultWindowStyle&0x00020000 == 0 || resultWindowStyle&0x00010000 == 0 {
		t.Fatal("result frame must hide the caption while retaining resize, system menu and min/max capabilities")
	}
}

func TestResultNonClientCalculationHandlesBothWin32Forms(t *testing.T) {
	w := &webWindow{kind: "result"}
	for _, calculateFull := range []uintptr{0, 1} {
		params := [3]rect{{-400, 10, 500, 600}, {10, 20, 30, 40}, {50, 60, 70, 80}}
		before := params
		if value, handled := handleResultFrame(w, 0x83, calculateFull, uintptr(unsafe.Pointer(&params[0])), "dark"); !handled || value != 0 {
			t.Fatal("custom non-client calculation was not handled")
		}
		if params[0] != resultClientBounds(before[0], before[0], false, 6) {
			t.Fatal("caption removal used the wrong RECT pointer form")
		}
		if params[1] != before[1] || params[2] != before[2] {
			t.Fatal("non-client calculation overwrote previous window/client rectangles")
		}
	}
}

func TestResultDragRegionCapabilityFallbackAndReferenceRelease(t *testing.T) {
	if enableResultDragRegions(nil) {
		t.Fatal("nil runtime settings cannot support native dragging")
	}
	for _, fixture := range []struct {
		name                  string
		queryHR, putHR, getHR uintptr
		enabled               int32
		want                  bool
		wantRelease           int
	}{
		{"modern runtime", 0, 0, 0, 1, true, 1},
		{"old runtime", 0x80004002, 0, 0, 0, false, 0},
		{"enable rejected", 0, 0x80004005, 0, 0, false, 1},
		{"read rejected", 0, 0, 0x80004005, 0, false, 1},
		{"not actually enabled", 0, 0, 0, 0, false, 1},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var base [3]uintptr
			var methods [39]uintptr
			object := struct{ vtable *[3]uintptr }{&base}
			extended := struct{ vtable *[39]uintptr }{&methods}
			var pin runtime.Pinner
			pin.Pin(&object)
			pin.Pin(&extended)
			pin.Pin(&base)
			pin.Pin(&methods)
			defer pin.Unpin()
			released, put := 0, false
			base[0] = syscall.NewCallback(func(this uintptr, iid *edge.GUID, out *unsafe.Pointer) uintptr {
				if *iid != *edge.NewGUID("{0528A73B-E92D-49F4-927A-E547DDDAA37D}") {
					t.Error("wrong Settings9 IID")
				}
				if fixture.queryHR == 0 {
					*out = unsafe.Pointer(&extended)
				}
				return fixture.queryHR
			})
			methods[2] = syscall.NewCallback(func(this uintptr) uintptr { released++; return 0 })
			methods[38] = syscall.NewCallback(func(this uintptr, on int32) uintptr {
				put = true
				if on != 1 {
					t.Error("non-client support must be enabled")
				}
				return fixture.putHR
			})
			methods[37] = syscall.NewCallback(func(this uintptr, out *int32) uintptr {
				if !put {
					t.Error("capability read before enable")
				}
				*out = fixture.enabled
				return fixture.getHR
			})
			if got := enableResultDragRegions((*edge.ICoreWebViewSettings)(unsafe.Pointer(&object))); got != fixture.want {
				t.Fatalf("native dragging supported=%v want %v", got, fixture.want)
			}
			if released != fixture.wantRelease {
				t.Fatalf("COM reference releases=%d want %d", released, fixture.wantRelease)
			}
			runtime.KeepAlive(object)
			runtime.KeepAlive(extended)
		})
	}
}

func TestResultSystemMenuTracksNormalMaximizedAndMinimized(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class := "GooesTranslateFrameMenuTest"
	w := &webWindow{kind: "result"}
	proc := syscall.NewCallback(func(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
		if hwnd == w.hwnd {
			if value, handled := handleResultFrame(w, msg, wp, lp, "dark"); handled {
				return value
			}
		}
		return call(user, "DefWindowProcW", hwnd, uintptr(msg), wp, lp)
	})
	if err := register(class, proc, 32512); err != nil {
		t.Fatal(err)
	}
	defer call(user, "UnregisterClassW", uintptr(unsafe.Pointer(str(class))), call(kernel, "GetModuleHandleW", 0))
	w.hwnd = create(class, "TEST owned hidden result", resultWindowStyle, 0, 120, 90, 840, 700, 0, 0)
	if w.hwnd == 0 {
		t.Fatal("could not create owned hidden test window")
	}
	defer call(user, "DestroyWindow", w.hwnd)
	base := call(user, "GetWindowLongPtrW", w.hwnd, ^uintptr(15))
	for _, fixture := range []struct {
		name                                    string
		state                                   uintptr
		restore, move, size, minimize, maximize bool
	}{
		{"normal", 0, false, true, true, true, true},
		{"maximized", 0x01000000, true, false, false, true, false},
		{"minimized", 0x20000000, true, false, false, false, true},
	} {
		// Keep all HWND calls on the creating UI thread; t.Run executes its
		// body on another goroutine and can deadlock synchronous Win32 messages.
		{
			call(user, "SetWindowLongPtrW", w.hwnd, ^uintptr(15), (base&^uintptr(0x21000000))|fixture.state)
			w.syncResultSystemMenu()
			menu := call(user, "GetSystemMenu", w.hwnd, 0)
			for _, item := range []struct {
				id      uintptr
				enabled bool
			}{{0xf120, fixture.restore}, {0xf010, fixture.move}, {0xf000, fixture.size}, {0xf020, fixture.minimize}, {0xf030, fixture.maximize}, {0xf060, true}} {
				flags := call(user, "GetMenuState", menu, item.id, 0)
				if got := flags&3 == 0; got != item.enabled {
					t.Fatalf("system menu %X enabled=%v want %v (flags=%X)", item.id, got, item.enabled, flags)
				}
			}
			if call(user, "IsWindowVisible", w.hwnd) != 0 {
				t.Fatal("menu regression must never show a user-facing test window")
			}
		}
	}
	call(user, "SetWindowLongPtrW", w.hwnd, ^uintptr(15), base)
}

func TestResultMaximizeWritesActualMINMAXINFOFields(t *testing.T) {
	monitor, work := rect{-1600, -900, 0, 0}, rect{-1550, -860, 0, -50}
	info := [5]point{{11, 12}, {21, 22}, {31, 32}, {41, 42}, {51, 52}}
	before := info
	applyResultMaximizeGeometry(&info, monitor, work)
	if info[1] != (point{1550, 810}) {
		t.Fatalf("MINMAXINFO ptMaxSize contains a position: %v", info[1])
	}
	if info[2] != (point{50, 40}) {
		t.Fatalf("MINMAXINFO ptMaxPosition contains a size: %v", info[2])
	}
	for _, i := range []int{0, 3, 4} {
		if info[i] != before[i] {
			t.Fatalf("MINMAXINFO field %d changed unexpectedly", i)
		}
	}
}
