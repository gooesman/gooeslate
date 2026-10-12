package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"unsafe"
)

// CaptureSmokeTest renders a synthetic selection and toolbar into a PNG. The
// native popup is created hidden to check its class and routing; no desktop
// image is read and no visible full-screen capture window is opened.
func CaptureSmokeTest(outputDir string) error {
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		return err
	}
	snapshot := image.NewRGBA(image.Rect(0, 0, 800, 480))
	draw.Draw(snapshot, snapshot.Bounds(), &image.Uniform{color.RGBA{239, 243, 249, 255}}, image.Point{}, draw.Src)
	draw.Draw(snapshot, image.Rect(60, 58, 740, 314), &image.Uniform{color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
	draw.Draw(snapshot, image.Rect(60, 58, 740, 82), &image.Uniform{color.RGBA{82, 115, 240, 255}}, image.Point{}, draw.Src)
	c := NewCaptureController(0, nil, nil)
	if captureRegistrationError != nil {
		return captureRegistrationError
	}
	if err := c.prepare(snapshot, image.Rect(-800, -480, 0, 0)); err != nil {
		return err
	}
	defer c.Close()
	creatingCapture = c
	c.hwnd = create("GooesTranslateCaptureV2", "Gooeslate screenshot smoke", 0x80000000, 0x80, 0, 0, 800, 480, 0, 0)
	creatingCapture = nil
	if c.hwnd == 0 {
		return fmt.Errorf("截图窗口创建验证失败")
	}
	captureWindows[c.hwnd] = c
	c.mode, c.ready = "choose", true
	c.start, c.end = image.Pt(80, 94), image.Pt(720, 294)
	c.textFont, c.hintFont = font(14, false), font(12, false)
	for _, b := range []struct {
		dc        uintptr
		textColor uintptr
	}{{c.original.dc, rgb(30, 39, 60)}, {c.dim.dc, rgb(16, 21, 33)}} {
		drawText(b.dc, "截图之后，选择你需要的操作。", rect{104, 123, 694, 166}, c.textFont, b.textColor, 4|0x20)
		drawText(b.dc, "文字识别 / 翻译文字 / 原图翻译", rect{104, 181, 694, 224}, c.textFont, b.textColor, 4|0x20)
		drawText(b.dc, "框选支持负坐标显示器，拖动时复用图像缓存。", rect{104, 234, 694, 268}, c.hintFont, b.textColor, 4|0x20)
	}
	if err := c.paint(c.back.dc, snapshot.Bounds()); err != nil {
		return err
	}
	call(gdi, "SelectObject", c.back.dc, c.back.previous)
	defer call(gdi, "SelectObject", c.back.dc, c.back.bitmap)
	data := make([]byte, 800*480*4)
	info := bitmapinfo{Header: bitmapheader{Size: 40, Width: 800, Height: -480, Planes: 1, BitCount: 32}}
	if call(gdi, "GetDIBits", c.back.dc, c.back.bitmap, 0, 480, uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&info)), 0) != 480 {
		return fmt.Errorf("截图缓存读取验证失败")
	}
	f, err := os.Create(filepath.Join(outputDir, "capture-toolbar.png"))
	if err != nil {
		return err
	}
	err = png.Encode(f, pixelsImage(data, 800, 480))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
