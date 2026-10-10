package main

import (
	"encoding/binary"
	"fmt"
	"image"
	"runtime"
	"unsafe"
)

// A positive-height BI_RGB DIB is understood by Windows' standard bitmap
// conversion and common image/chat editors. Screen crops are opaque; any
// transparent source pixels are flattened on white before copying.
func imageClipboardDIB(img *image.RGBA) ([]byte, error) {
	if img == nil || img.Bounds().Empty() {
		return nil, fmt.Errorf("没有可复制的截图")
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w > 100000 || h > 100000 || int64(w)*int64(h) > 100000000 || img.Stride < w*4 || int64(img.Stride)*int64(h-1)+int64(w)*4 > int64(len(img.Pix)) {
		return nil, fmt.Errorf("截图尺寸或像素数据无效")
	}
	data := make([]byte, 40+w*h*4)
	binary.LittleEndian.PutUint32(data[0:4], 40)
	binary.LittleEndian.PutUint32(data[4:8], uint32(w))
	binary.LittleEndian.PutUint32(data[8:12], uint32(h))
	binary.LittleEndian.PutUint16(data[12:14], 1)
	binary.LittleEndian.PutUint16(data[14:16], 32)
	binary.LittleEndian.PutUint32(data[20:24], uint32(w*h*4))
	for y := 0; y < h; y++ {
		src := img.Pix[y*img.Stride:]
		dst := data[40+(h-1-y)*w*4:]
		for x := 0; x < w; x++ {
			i := x * 4
			white := 255 - int(src[i+3])
			dst[i] = byte(min(255, int(src[i+2])+white))
			dst[i+1] = byte(min(255, int(src[i+1])+white))
			dst[i+2] = byte(min(255, int(src[i])+white))
			dst[i+3] = 255
		}
	}
	return data, nil
}

// The small interface makes failure/ownership checks possible without touching
// the user's clipboard in tests.
type imageClipboardAPI interface {
	allocate(data []byte) (uintptr, error)
	free(uintptr)
	open(owner uintptr) bool
	close()
	empty() bool
	setDIB(uintptr) bool
}

type nativeImageClipboard struct{}

func (nativeImageClipboard) allocate(data []byte) (uintptr, error) {
	mem := call(kernel, "GlobalAlloc", 0x42, uintptr(len(data))) // GMEM_MOVEABLE | ZEROINIT
	if mem == 0 {
		return 0, fmt.Errorf("无法为截图分配剪贴板内存")
	}
	p := call(kernel, "GlobalLock", mem)
	if p == 0 {
		call(kernel, "GlobalFree", mem)
		return 0, fmt.Errorf("无法写入截图剪贴板内存")
	}
	call(ntdll, "RtlMoveMemory", p, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	call(kernel, "GlobalUnlock", mem)
	runtime.KeepAlive(data)
	return mem, nil
}
func (nativeImageClipboard) free(mem uintptr)        { call(kernel, "GlobalFree", mem) }
func (nativeImageClipboard) open(owner uintptr) bool { return call(user, "OpenClipboard", owner) != 0 }
func (nativeImageClipboard) close()                  { call(user, "CloseClipboard") }
func (nativeImageClipboard) empty() bool             { return call(user, "EmptyClipboard") != 0 }
func (nativeImageClipboard) setDIB(mem uintptr) bool {
	return call(user, "SetClipboardData", 8, mem) != 0
}

func clipboardImageWithAPI(owner uintptr, img *image.RGBA, api imageClipboardAPI) error {
	if owner == 0 {
		return fmt.Errorf("截图剪贴板窗口不可用")
	}
	data, err := imageClipboardDIB(img)
	if err != nil {
		return err
	}
	// Prepare everything before EmptyClipboard. Invalid/canceled captures,
	// allocation failures and an occupied clipboard preserve previous contents.
	mem, err := api.allocate(data)
	if err != nil {
		return err
	}
	owned := true
	defer func() {
		if owned {
			api.free(mem)
		}
	}()
	if !api.open(owner) {
		return fmt.Errorf("剪贴板被其他程序占用，请重试")
	}
	defer api.close()
	if !api.empty() {
		return fmt.Errorf("无法准备截图剪贴板")
	}
	if !api.setDIB(mem) {
		return fmt.Errorf("无法将截图写入剪贴板")
	}
	owned = false // SetClipboardData transfers ownership to Windows.
	return nil
}

// clipboardImage copies a confirmed image as CF_DIB. It does not run OCR or
// capture the screen; call it with an existing crop and a live owner HWND.
func clipboardImage(owner uintptr, img *image.RGBA) error {
	return clipboardImageWithAPI(owner, img, nativeImageClipboard{})
}
