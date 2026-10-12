package main

import (
	"fmt"
	"github.com/gooesman/gooeslate/internal/engine"
	"image"
	"math"
	"unsafe"
)

func renderExport(img *image.RGBA, lines []engine.Line, translations map[int]string) (*image.RGBA, error) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	dc := call(gdi, "CreateCompatibleDC", 0)
	if dc == 0 {
		return nil, fmt.Errorf("无法创建图片画布")
	}
	defer call(gdi, "DeleteDC", dc)
	info := bitmapinfo{Header: bitmapheader{Size: 40, Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}}
	var bits unsafe.Pointer
	bmp := call(gdi, "CreateDIBSection", dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return nil, fmt.Errorf("无法创建导出图片")
	}
	defer call(gdi, "DeleteObject", bmp)
	old := call(gdi, "SelectObject", dc, bmp)
	defer call(gdi, "SelectObject", dc, old)
	paintPixels(dc, imagePixels(img), w, h, rect{0, 0, int32(w), int32(h)})
	for _, l := range lines {
		s, ok := translations[l.ID]
		if !ok {
			continue
		}
		b := l.Bounds
		r := rect{int32(math.Round(b.X)), int32(math.Round(b.Y)), int32(math.Round(b.X + b.W)), int32(math.Round(b.Y + b.H))}
		if r.R <= r.L || r.B <= r.T {
			continue
		}
		fill(dc, rect{r.L - 6, r.T - 6, r.R + 6, r.B + 6}, rgb(255, 255, 255))
		size := fitExportFont(max(5, int(float64(r.B-r.T)*.82)), func(size int) bool {
			f := font(size, false)
			previous := call(gdi, "SelectObject", dc, f)
			m := rect{R: r.R - r.L}
			call(user, "DrawTextW", dc, uintptr(unsafe.Pointer(str(s))), ^uintptr(0), uintptr(unsafe.Pointer(&m)), 0x400|0x10|0x800)
			call(gdi, "SelectObject", dc, previous)
			call(gdi, "DeleteObject", f)
			return m.B <= r.B-r.T && m.R <= r.R-r.L
		})
		f := font(size, false)
		state := call(gdi, "SaveDC", dc)
		call(gdi, "IntersectClipRect", dc, u(int(r.L)), u(int(r.T)), u(int(r.R)), u(int(r.B)))
		drawText(dc, s, r, f, rgb(30, 34, 40), 0x10)
		call(gdi, "RestoreDC", dc, state)
		call(gdi, "DeleteObject", f)
	}
	call(gdi, "GdiFlush")
	return pixelsImage(unsafe.Slice((*byte)(bits), w*h*4), w, h), nil
}

func fitExportFont(maximum int, fits func(int) bool) int {
	low, high := 5, max(5, maximum)
	best := low
	for low <= high {
		middle := low + (high-low)/2
		if fits(middle) {
			best = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best
}
