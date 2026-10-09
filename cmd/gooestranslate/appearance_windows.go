package main

import (
	"runtime"
	"syscall"
	"unsafe"
)

var appearanceDWM = syscall.NewLazyDLL("dwmapi.dll")
var appearanceRegistry = syscall.NewLazyDLL("advapi32.dll")

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

type appIconInfo struct {
	Icon               int32
	XHotspot, YHotspot uint32
	Mask, Color        uintptr
}

// createAppIcon returns a caller-owned HICON. Share it between both windows and
// the notification icon, then DestroyIcon once after removing the notification
// icon and destroying the windows. All temporary DCs, bitmaps and pens are freed.
func createAppIcon() uintptr {
	const size, supersample = 64, 4
	const large = size * supersample
	dc := call(gdi, "CreateCompatibleDC", 0)
	if dc == 0 {
		return 0
	}
	defer call(gdi, "DeleteDC", dc)
	info := bitmapinfo{Header: bitmapheader{Size: 40, Width: large, Height: -large, Planes: 1, BitCount: 32}}
	var bits unsafe.Pointer
	largeBitmap := call(gdi, "CreateDIBSection", dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if largeBitmap == 0 || bits == nil {
		return 0
	}
	defer call(gdi, "DeleteObject", largeBitmap)
	previous := call(gdi, "SelectObject", dc, largeBitmap)
	defer call(gdi, "SelectObject", dc, previous)
	pixels := unsafe.Slice((*byte)(bits), large*large*4)
	inside := make([]bool, large*large)
	// A rounded orange tile, rendered at four times the final size. The final
	// downsample preserves smooth transparent corners and sharp small glyphs.
	for y := 0; y < large; y++ {
		for x := 0; x < large; x++ {
			fx, fy := (float64(x)+.5)/supersample, (float64(y)+.5)/supersample
			if fx < 2 || fx >= 62 || fy < 2 || fy >= 62 {
				continue
			}
			cx, cy := max(16., min(48., fx)), max(16., min(48., fy))
			if (fx-cx)*(fx-cx)+(fy-cy)*(fy-cy) > 14*14 {
				continue
			}
			i := (y*large + x) * 4
			progress := (fx + fy - 4) / 120
			pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = byte(118-49*progress), byte(183-40*progress), 255, 255
			inside[y*large+x] = true
		}
	}
	// This is the same translation mark used by the HTML UI: a language stroke
	// at the left and a small A at the right, drawn with vector strokes.
	pen := call(gdi, "CreatePen", 0, 2*supersample, rgb(42, 30, 23))
	if pen == 0 {
		return 0
	}
	defer call(gdi, "DeleteObject", pen)
	oldPen := call(gdi, "SelectObject", dc, pen)
	defer call(gdi, "SelectObject", dc, oldPen)
	path := func(points ...[2]float64) {
		p := make([]point, len(points))
		for i, xy := range points {
			p[i] = point{int32((9+xy[0]*1.9)*supersample + .5), int32((9+xy[1]*1.9)*supersample + .5)}
		}
		call(gdi, "Polyline", dc, uintptr(unsafe.Pointer(&p[0])), uintptr(len(p)))
		runtime.KeepAlive(p)
	}
	path([2]float64{4, 5}, [2]float64{13, 5})
	path([2]float64{8.5, 3}, [2]float64{8.5, 5}, [2]float64{8.2, 9}, [2]float64{7, 12}, [2]float64{5, 14}, [2]float64{3.5, 15})
	path([2]float64{5, 9}, [2]float64{6.2, 11.5}, [2]float64{8.5, 13.5}, [2]float64{12, 15})
	path([2]float64{13, 21}, [2]float64{17, 11}, [2]float64{21, 21})
	path([2]float64{14.5, 17}, [2]float64{19.5, 17})
	call(gdi, "GdiFlush")
	// GDI does not maintain alpha during drawing. The orange tile is opaque;
	// restore its alpha before producing the premultiplied antialiased icon.
	for i, on := range inside {
		if on {
			pixels[i*4+3] = 255
		}
	}
	final := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var sums [4]uint32
			for yy := 0; yy < supersample; yy++ {
				for xx := 0; xx < supersample; xx++ {
					i := ((y*supersample+yy)*large + x*supersample + xx) * 4
					for k := range sums {
						sums[k] += uint32(pixels[i+k])
					}
				}
			}
			for k, sum := range sums {
				final[(y*size+x)*4+k] = byte((sum + 8) / 16)
			}
		}
	}
	finalInfo := bitmapinfo{Header: bitmapheader{Size: 40, Width: size, Height: -size, Planes: 1, BitCount: 32}}
	var finalBits unsafe.Pointer
	colorBitmap := call(gdi, "CreateDIBSection", dc, uintptr(unsafe.Pointer(&finalInfo)), 0, uintptr(unsafe.Pointer(&finalBits)), 0, 0)
	if colorBitmap == 0 || finalBits == nil {
		return 0
	}
	defer call(gdi, "DeleteObject", colorBitmap)
	copy(unsafe.Slice((*byte)(finalBits), len(final)), final)
	maskPixels := make([]byte, size*size/8)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if final[(y*size+x)*4+3] == 0 {
				maskPixels[y*size/8+x/8] |= 0x80 >> (x % 8)
			}
		}
	}
	mask := call(gdi, "CreateBitmap", size, size, 1, 1, uintptr(unsafe.Pointer(&maskPixels[0])))
	runtime.KeepAlive(maskPixels)
	if mask == 0 {
		return 0
	}
	defer call(gdi, "DeleteObject", mask)
	icon := appIconInfo{Icon: 1, Mask: mask, Color: colorBitmap}
	return call(user, "CreateIconIndirect", uintptr(unsafe.Pointer(&icon)))
}

func setWindowAppIcon(hwnd, icon uintptr) {
	if hwnd != 0 && icon != 0 {
		send(hwnd, 0x80, 0, icon) // WM_SETICON / ICON_SMALL
		send(hwnd, 0x80, 1, icon) // WM_SETICON / ICON_BIG
	}
}
