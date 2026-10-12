package main

import (
	"fmt"
	"image"
	"runtime"
	"syscall"
	"unsafe"
)

var user = syscall.NewLazyDLL("user32.dll")
var gdi = syscall.NewLazyDLL("gdi32.dll")
var kernel = syscall.NewLazyDLL("kernel32.dll")
var dialogs = syscall.NewLazyDLL("comdlg32.dll")
var crypt = syscall.NewLazyDLL("crypt32.dll")
var ntdll = syscall.NewLazyDLL("ntdll.dll")

// Keep Go memory passed through uintptr alive across the complete native call.
//
//go:uintptrescapes
func call(d *syscall.LazyDLL, name string, args ...uintptr) uintptr {
	r, _, _ := d.NewProc(name).Call(args...)
	return r
}
func u(v int) uintptr      { return uintptr(v) }
func str(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

type point struct{ X, Y int32 }
type rect struct{ L, T, R, B int32 }
type wndclass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WndExtra               int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}
type message struct {
	Hwnd    uintptr
	ID      uint32
	W, L    uintptr
	Time    uint32
	Pt      point
	Private uint32
}
type paintstruct struct {
	DC                 uintptr
	Erase              int32
	Paint              rect
	Restore, IncUpdate int32
	Reserved           [32]byte
}
type bitmapheader struct {
	Size                   uint32
	Width, Height          int32
	Planes, BitCount       uint16
	Compression, SizeImage uint32
	XPels, YPels           int32
	Used, Important        uint32
}
type bitmapinfo struct {
	Header bitmapheader
	Colors [3]uint32
}
type openfilename struct {
	Size                      uint32
	Owner, Instance           uintptr
	Filter, CustomFilter      *uint16
	MaxCustom, FilterIndex    uint32
	File                      *uint16
	MaxFile                   uint32
	FileTitle                 *uint16
	MaxTitle                  uint32
	InitialDir, Title         *uint16
	Flags                     uint32
	FileOffset, FileExtension uint16
	DefaultExt                *uint16
	Custom, Hook              uintptr
	Template                  *uint16
	Reserved                  uintptr
	Reserved2, FlagsEx        uint32
}

func register(name string, proc uintptr, cursor uintptr) error {
	c := wndclass{Proc: proc, Instance: call(kernel, "GetModuleHandleW", 0), Cursor: call(user, "LoadCursorW", 0, cursor), Name: str(name)}
	c.Size = uint32(unsafe.Sizeof(c))
	if call(user, "RegisterClassExW", uintptr(unsafe.Pointer(&c))) == 0 {
		return fmt.Errorf("注册窗口失败")
	}
	return nil
}
func create(class, title string, style, ex uintptr, x, y, w, h int, parent, id uintptr) uintptr {
	return call(user, "CreateWindowExW", ex, uintptr(unsafe.Pointer(str(class))), uintptr(unsafe.Pointer(str(title))), style, u(x), u(y), u(w), u(h), parent, id, call(kernel, "GetModuleHandleW", 0), 0)
}
func text(h uintptr) string {
	n := call(user, "GetWindowTextLengthW", h) + 1
	b := make([]uint16, n)
	call(user, "GetWindowTextW", h, uintptr(unsafe.Pointer(&b[0])), n)
	return syscall.UTF16ToString(b)
}
func setText(h uintptr, s string) { call(user, "SetWindowTextW", h, uintptr(unsafe.Pointer(str(s)))) }

//go:uintptrescapes
func send(h uintptr, msg uint32, w, l uintptr) uintptr {
	return call(user, "SendMessageW", h, uintptr(msg), w, l)
}
func move(h uintptr, x, y, w, height int) {
	if h != 0 {
		call(user, "MoveWindow", h, u(x), u(y), u(w), u(height), 1)
	}
}
func invalidate(h uintptr) { call(user, "InvalidateRect", h, 0, 0) }
func clientRect(h uintptr) rect {
	var r rect
	call(user, "GetClientRect", h, uintptr(unsafe.Pointer(&r)))
	return r
}
func cursorPoint(lp uintptr) point {
	return point{int32(int16(lp & 0xffff)), int32(int16((lp >> 16) & 0xffff))}
}
func rgb(r, g, b byte) uintptr { return uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16 }
func fill(dc uintptr, r rect, color uintptr) {
	brush := call(gdi, "CreateSolidBrush", color)
	call(user, "FillRect", dc, uintptr(unsafe.Pointer(&r)), brush)
	call(gdi, "DeleteObject", brush)
}
func frame(dc uintptr, r rect, color uintptr) {
	brush := call(gdi, "CreateSolidBrush", color)
	call(user, "FrameRect", dc, uintptr(unsafe.Pointer(&r)), brush)
	call(gdi, "DeleteObject", brush)
}
func font(size int, bold bool) uintptr {
	weight := 400
	if bold {
		weight = 600
	}
	return call(gdi, "CreateFontW", u(-size), 0, 0, 0, u(weight), 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(str("Microsoft YaHei UI"))))
}
func drawText(dc uintptr, s string, r rect, f uintptr, color uintptr, flags uintptr) {
	old := call(gdi, "SelectObject", dc, f)
	call(gdi, "SetBkMode", dc, 1)
	call(gdi, "SetTextColor", dc, color)
	call(user, "DrawTextW", dc, uintptr(unsafe.Pointer(str(s))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), flags|0x800)
	call(gdi, "SelectObject", dc, old)
}
func check(h uintptr) bool { return send(h, 0xf0, 0, 0) == 1 }
func setCheck(h uintptr, yes bool) {
	v := uintptr(0)
	if yes {
		v = 1
	}
	send(h, 0xf1, v, 0)
}
func enabled(h uintptr, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	call(user, "EnableWindow", h, v)
}
func imagePixels(img image.Image) []byte {
	b := img.Bounds()
	data := make([]byte, b.Dx()*b.Dy()*4)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bv, _ := img.At(x+b.Min.X, y+b.Min.Y).RGBA()
			i := (y*b.Dx() + x) * 4
			data[i] = byte(bv >> 8)
			data[i+1] = byte(g >> 8)
			data[i+2] = byte(r >> 8)
			data[i+3] = 255
		}
	}
	return data
}
func pixelsImage(data []byte, w, h int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(data); i += 4 {
		out.Pix[i] = data[i+2]
		out.Pix[i+1] = data[i+1]
		out.Pix[i+2] = data[i]
		out.Pix[i+3] = 255
	}
	return out
}
func paintPixels(dc uintptr, pixels []byte, w, h int, dest rect) {
	if len(pixels) == 0 || dest.R <= dest.L || dest.B <= dest.T {
		return
	}
	info := bitmapinfo{Header: bitmapheader{Size: 40, Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}}
	call(gdi, "SetStretchBltMode", dc, 4)
	call(gdi, "SetBrushOrgEx", dc, 0, 0, 0)
	call(gdi, "StretchDIBits", dc, u(int(dest.L)), u(int(dest.T)), u(int(dest.R-dest.L)), u(int(dest.B-dest.T)), 0, 0, u(w), u(h), uintptr(unsafe.Pointer(&pixels[0])), uintptr(unsafe.Pointer(&info)), 0, 0x00cc0020)
	runtime.KeepAlive(pixels)
}
func desktop() (*image.RGBA, rect, error) {
	x := int(int32(call(user, "GetSystemMetrics", 76)))
	y := int(int32(call(user, "GetSystemMetrics", 77)))
	w := int(call(user, "GetSystemMetrics", 78))
	h := int(call(user, "GetSystemMetrics", 79))
	if _, err := capturePixelBytes(w, h); err != nil {
		return nil, rect{}, fmt.Errorf("屏幕尺寸不支持")
	}
	// Synchronize our pending compositor changes after hiding the app. This is
	// best effort: DwmFlush does not synchronize other applications' rendering.
	if flush := appearanceDWM.NewProc("DwmFlush"); flush.Find() == nil {
		flush.Call()
	}
	dc := call(user, "GetDC", 0)
	if dc == 0 {
		return nil, rect{}, fmt.Errorf("无法访问屏幕画布")
	}
	defer call(user, "ReleaseDC", 0, dc)
	img, err := captureDCImage(dc, x, y, w, h)
	if err != nil {
		return nil, rect{}, err
	}
	return img, rect{int32(x), int32(y), int32(x + w), int32(y + h)}, nil
}
func clipboard(owner uintptr, s string) error {
	if call(user, "OpenClipboard", owner) == 0 {
		return fmt.Errorf("剪贴板被其他程序占用，请重试")
	}
	defer call(user, "CloseClipboard")
	data, _ := syscall.UTF16FromString(s)
	mem := call(kernel, "GlobalAlloc", 0x42, u(len(data)*2))
	if mem == 0 {
		return fmt.Errorf("剪贴板分配失败")
	}
	p := call(kernel, "GlobalLock", mem)
	if p == 0 {
		call(kernel, "GlobalFree", mem)
		return fmt.Errorf("剪贴板写入失败")
	}
	call(ntdll, "RtlMoveMemory", p, uintptr(unsafe.Pointer(&data[0])), u(len(data)*2))
	call(kernel, "GlobalUnlock", mem)
	call(user, "EmptyClipboard")
	if call(user, "SetClipboardData", 13, mem) == 0 {
		call(kernel, "GlobalFree", mem)
		return fmt.Errorf("剪贴板写入失败")
	}
	return nil
}
func fileDialog(owner uintptr, save bool) string {
	buf := make([]uint16, 32768)
	if save {
		copy(buf, syscall.StringToUTF16("translated.png"))
	}
	var filter []uint16
	for _, s := range []string{"图片 (PNG / JPEG)", "*.png;*.jpg;*.jpeg", "所有文件", "*.*", ""} {
		filter = append(filter, syscall.StringToUTF16(s)...)
	}
	title := "打开图片"
	flags := uint32(0x80000 | 0x800 | 0x1000 | 8)
	proc := "GetOpenFileNameW"
	ext := ""
	if save {
		title = "导出原图翻译"
		flags = 0x80000 | 0x800 | 2 | 8
		proc = "GetSaveFileNameW"
		ext = "png"
		filter = nil
		for _, s := range []string{"PNG 图片", "*.png", ""} {
			filter = append(filter, syscall.StringToUTF16(s)...)
		}
	}
	o := openfilename{Owner: owner, File: &buf[0], MaxFile: uint32(len(buf)), Filter: &filter[0], FilterIndex: 1, Title: str(title), Flags: flags, DefaultExt: str(ext)}
	o.Size = uint32(unsafe.Sizeof(o))
	if call(dialogs, proc, uintptr(unsafe.Pointer(&o))) == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}
