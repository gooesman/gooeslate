package main

import (
	"syscall"
	"unsafe"
)

var appearanceDWM = syscall.NewLazyDLL("dwmapi.dll")
var appearanceRegistry = syscall.NewLazyDLL("advapi32.dll")

const dwmColorNone = uint32(0xfffffffe)

func appUsesDarkAppearance(theme string) bool {
	switch theme {
	case "dark":
		return true
	case "light":
		return false
	}
	var light uint32 = 1
	size := uint32(unsafe.Sizeof(light))
	// HKEY_CURRENT_USER is a sign-extended predefined handle on 64-bit Windows.
	if call(appearanceRegistry, "RegGetValueW", ^uintptr(0x7ffffffe), uintptr(unsafe.Pointer(str("Software\\Microsoft\\Windows\\CurrentVersion\\Themes\\Personalize"))), uintptr(unsafe.Pointer(str("AppsUseLightTheme"))), 0x10, 0, uintptr(unsafe.Pointer(&light)), uintptr(unsafe.Pointer(&size))) != 0 {
		return false
	}
	return light == 0
}

// applyWindowAppearance is safe on Windows 10 and 11. Unsupported DWM
// attributes are ignored by older Windows versions. Call again after theme
// changes or WM_SETTINGCHANGE / WM_THEMECHANGED when theme is "system".
func applyWindowAppearance(hwnd uintptr, theme string) {
	if hwnd == 0 {
		return
	}
	dark := appUsesDarkAppearance(theme)
	darkValue := uint32(0)
	caption, foreground, border := uint32(rgb(247, 248, 250)), uint32(rgb(37, 43, 53)), uint32(rgb(224, 228, 233))
	if dark {
		darkValue = 1
		caption, foreground, border = uint32(rgb(17, 18, 20)), uint32(rgb(240, 241, 243)), uint32(rgb(48, 50, 56))
	}
	// Result windows already draw their own uniform surface. Windows 11's
	// contrasting one-pixel outline would look like another nested frame.
	style := call(user, "GetWindowLongPtrW", hwnd, ^uintptr(15))
	if isCustomResultFrame(style) {
		caption = uint32(resultSurfaceColor(dark))
		border = dwmColorNone
	}
	attribute := func(id uint32, value *uint32) uintptr {
		return call(appearanceDWM, "DwmSetWindowAttribute", hwnd, uintptr(id), uintptr(unsafe.Pointer(value)), unsafe.Sizeof(*value))
	}
	if int32(attribute(20, &darkValue)) < 0 {
		attribute(19, &darkValue)
	}
	attribute(35, &caption)
	attribute(36, &foreground)
	attribute(34, &border)
	corner := uint32(2) // DWMWCP_ROUND
	attribute(33, &corner)
	call(user, "SetWindowPos", hwnd, 0, 0, 0, 0, 0, 0x37)
	call(user, "RedrawWindow", hwnd, 0, 0, 0x401)
}

// createAppIcon loads the embedded multi-size Windows resource. The returned
// HICON belongs to the caller; both windows and the tray share it until shutdown.
func createAppIcon() uintptr {
	instance := call(kernel, "GetModuleHandleW", 0)
	return call(user, "LoadImageW", instance, 1, 1, 64, 64, 0)
}

func setWindowAppIcon(hwnd, icon uintptr) {
	if hwnd != 0 && icon != 0 {
		send(hwnd, 0x80, 0, icon) // WM_SETICON / ICON_SMALL
		send(hwnd, 0x80, 1, icon) // WM_SETICON / ICON_BIG
	}
}
