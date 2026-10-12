package main

import (
	"fmt"
	"image"
	"unsafe"
)

func capturePixelBytes(width, height int) (int, error) {
	if width <= 0 || height <= 0 || width > 100000 || height > 100000 || int64(width)*int64(height) > 100000000 {
		return 0, fmt.Errorf("截图尺寸不支持")
	}
	return width * height * 4, nil
}

func validCaptureSnapshot(snapshot *image.RGBA) bool {
	if snapshot == nil {
		return false
	}
	w, h := snapshot.Bounds().Dx(), snapshot.Bounds().Dy()
	if _, err := capturePixelBytes(w, h); err != nil || snapshot.Stride < w*4 {
		return false
	}
	return int64(snapshot.Stride)*int64(h-1)+int64(w)*4 <= int64(len(snapshot.Pix))
}

// A nonzero GetDIBits result is a row count, not merely success. Never deliver
// a half-read screenshot to the clipboard or recognition service.
func readCapturedPixels(width, height int, copyRows func([]byte, *bitmapinfo) uintptr) (*image.RGBA, error) {
	length, err := capturePixelBytes(width, height)
	if err != nil {
		return nil, err
	}
	data := make([]byte, length)
	info := bitmapinfo{Header: bitmapheader{Size: 40, Width: int32(width), Height: -int32(height), Planes: 1, BitCount: 32}}
	if copyRows(data, &info) != uintptr(height) {
		return nil, fmt.Errorf("截图像素读取不完整，请重新截图")
	}
	return pixelsImage(data, width, height), nil
}

// The source may be the screen DC or an owned synthetic bitmap in tests.
// Tests never need to inspect the user's desktop to exercise the capture path.
func captureDCImage(sourceDC uintptr, x, y, width, height int) (*image.RGBA, error) {
	if _, err := capturePixelBytes(width, height); err != nil {
		return nil, err
	}
	if sourceDC == 0 {
		return nil, fmt.Errorf("无法访问截图画布")
	}
	mem := call(gdi, "CreateCompatibleDC", sourceDC)
	if mem == 0 {
		return nil, fmt.Errorf("无法创建截图缓存")
	}
	defer call(gdi, "DeleteDC", mem)
	bmp := call(gdi, "CreateCompatibleBitmap", sourceDC, u(width), u(height))
	if bmp == 0 {
		return nil, fmt.Errorf("无法创建截图")
	}
	defer call(gdi, "DeleteObject", bmp)
	previous := call(gdi, "SelectObject", mem, bmp)
	if previous == 0 || previous == ^uintptr(0) {
		return nil, fmt.Errorf("无法准备截图缓存")
	}
	selected := true
	defer func() {
		if selected {
			call(gdi, "SelectObject", mem, previous)
		}
	}()
	if call(gdi, "BitBlt", mem, 0, 0, u(width), u(height), sourceDC, u(x), u(y), 0x40cc0020) == 0 || call(gdi, "GdiFlush") == 0 {
		return nil, fmt.Errorf("无法读取截图图像，请重新截图")
	}
	// GetDIBits requires the bitmap to be outside every DC when it is read.
	removed := call(gdi, "SelectObject", mem, previous)
	if removed == 0 || removed == ^uintptr(0) {
		return nil, fmt.Errorf("无法读取截图缓存")
	}
	selected = false
	return readCapturedPixels(width, height, func(data []byte, info *bitmapinfo) uintptr {
		return call(gdi, "GetDIBits", mem, bmp, 0, u(height), uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(info)), 0)
	})
}
