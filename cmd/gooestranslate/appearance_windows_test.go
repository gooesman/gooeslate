package main

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

func TestResultAppearanceHasOneSurfaceAcrossThemeChanges(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	class := "GooesTranslateSurfaceTest"
	windows := make(map[uintptr]*webWindow)
	proc := syscall.NewCallback(func(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
		if w := windows[hwnd]; w != nil {
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
	for _, fixture := range []struct {
		kind  string
		style uintptr
	}{
		{"result", resultWindowStyle},
		{"settings", 0xcf0000 | 0x02000000},
	} {
		w := &webWindow{kind: fixture.kind}
		w.hwnd = create(class, "TEST owned hidden appearance", fixture.style, 0, 100, 100, 840, 700, 0, 0)
		if w.hwnd == 0 {
			t.Fatal("could not create hidden appearance test window")
		}
		windows[w.hwnd] = w
		for _, theme := range []string{"dark", "light", "dark"} {
			applyWindowAppearance(w.hwnd, theme)
			var border, caption uint32
			supported := int32(call(appearanceDWM, "DwmGetWindowAttribute", w.hwnd, 34, uintptr(unsafe.Pointer(&border)), unsafe.Sizeof(border))) >= 0
			if supported {
				if fixture.kind == "result" {
					if border != dwmColorNone {
						t.Fatalf("%s result has a second DWM outline: %X", theme, border)
					}
					if int32(call(appearanceDWM, "DwmGetWindowAttribute", w.hwnd, 35, uintptr(unsafe.Pointer(&caption)), unsafe.Sizeof(caption))) < 0 {
						t.Fatal("could not read the result frame surface")
					}
					if caption != uint32(resultSurfaceColor(theme == "dark")) {
						t.Fatalf("%s result native surface differs from HTML background: %X", theme, caption)
					}
				} else if border == dwmColorNone {
					t.Fatal("settings must retain its standard native frame")
				}
			}
			if fixture.kind == "result" {
				if _, err := resultFrameSnapshot(w); err != nil {
					t.Fatalf("result frame failed after %s theme change: %v", theme, err)
				}
			} else if call(user, "GetWindowLongPtrW", w.hwnd, ^uintptr(15))&0x00c00000 == 0 {
				t.Fatal("settings system caption was removed")
			}
			if call(user, "IsWindowVisible", w.hwnd) != 0 {
				t.Fatal("appearance test must not show a user-facing window")
			}
		}
		call(user, "DestroyWindow", w.hwnd)
		delete(windows, w.hwnd)
	}
}
