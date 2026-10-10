package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"
)

// Only synthetic pixels are used here. This verifies the native cached bitmap
// rendering without reading the user's desktop or opening a screenshot window.
func TestCaptureCachedPaintAndSelectionMove(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	snapshot := image.NewRGBA(image.Rect(0, 0, 240, 180))
	draw.Draw(snapshot, snapshot.Bounds(), &image.Uniform{color.RGBA{200, 100, 40, 255}}, image.Point{}, draw.Src)
	c := &CaptureController{mode: "ocr", scale: 1}
	if err := c.prepare(snapshot, image.Rect(-240, -180, 0, 0)); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.textFont, c.hintFont = font(14, false), font(12, false)
	c.start, c.end, c.ready = image.Pt(10, 60), image.Pt(80, 100), true
	c.paint(c.back.dc, snapshot.Bounds())
	read := func() *image.RGBA {
		call(gdi, "GdiFlush")
		call(gdi, "SelectObject", c.back.dc, c.back.previous)
		defer call(gdi, "SelectObject", c.back.dc, c.back.bitmap)
		data := make([]byte, 240*180*4)
		info := bitmapinfo{Header: bitmapheader{Size: 40, Width: 240, Height: -180, Planes: 1, BitCount: 32}}
		if call(gdi, "GetDIBits", c.back.dc, c.back.bitmap, 0, 180, uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&info)), 0) == 0 {
			t.Fatal("unable to read synthetic native bitmap")
		}
		return pixelsImage(data, 240, 180)
	}
	painted := read()
	if got := painted.RGBAAt(20, 80); got != (color.RGBA{200, 100, 40, 255}) {
		t.Fatalf("selection must preserve brightness: %v", got)
	}
	if got := painted.RGBAAt(220, 150); got != (color.RGBA{110, 55, 22, 255}) {
		t.Fatalf("outside area must be dimmed without byte overflow: %v", got)
	}
	old := c.dirty()
	c.start, c.end = image.Pt(140, 80), image.Pt(210, 140)
	c.paint(c.back.dc, old.Union(c.dirty()).Intersect(snapshot.Bounds()))
	painted = read()
	if got := painted.RGBAAt(20, 80); got != (color.RGBA{110, 55, 22, 255}) {
		t.Fatalf("previous selection must be restored on incremental repaint: %v", got)
	}
	if got := painted.RGBAAt(170, 115); got != (color.RGBA{200, 100, 40, 255}) {
		t.Fatalf("new selection must be bright: %v", got)
	}
}

func TestCaptureAcceptReleasesBeforeShowingResult(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	snapshot := image.NewRGBA(image.Rect(0, 0, 24, 20))
	snapshot.SetRGBA(4, 6, color.RGBA{90, 130, 200, 255})
	c := &CaptureController{active: true, mode: "choose", scale: 1}
	if err := c.prepare(snapshot, snapshot.Bounds()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.start, c.end, c.ready = image.Pt(4, 6), image.Pt(18, 17), true
	called := false
	completed := false
	c.SetOnComplete(func(img *image.RGBA, mode string) {
		completed = true
		if called || c.Active() || c.snapshot != nil || img.Bounds() != image.Rect(0, 0, 14, 11) || mode != "translate" {
			t.Fatal("completion must receive final crop after overlay closes and before OCR")
		}
	})
	c.onCrop = func(img *image.RGBA, mode string) {
		called = true
		if !completed {
			t.Fatal("screenshot copy opportunity must precede OCR/result callback")
		}
		if c.Active() || c.snapshot != nil || c.original.dc != 0 || c.dim.dc != 0 || c.back.dc != 0 {
			t.Fatal("result callback must run after capture resources are released")
		}
		if mode != "translate" || img.Bounds() != image.Rect(0, 0, 14, 11) || img.RGBAAt(0, 0).R != 90 {
			t.Fatal("cropped image or requested action was lost")
		}
	}
	c.accept("translate")
	if !called {
		t.Fatal("accepted screenshot did not invoke result callback")
	}
	c.accept("ocr")
}

func TestCaptureCanceledUnconfirmedAndSmallCropDoNotComplete(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for _, action := range []string{"cancel", "unconfirmed", "small"} {
		t.Run(action, func(t *testing.T) {
			c := &CaptureController{active: true, snapshot: image.NewRGBA(image.Rect(0, 0, 20, 20)), ready: true}
			c.start, c.end = image.Pt(3, 3), image.Pt(13, 13)
			c.SetOnComplete(func(*image.RGBA, string) { t.Fatal("incomplete screenshot must never trigger clipboard callback") })
			c.onCrop = func(*image.RGBA, string) { t.Fatal("incomplete screenshot must never reach OCR") }
			switch action {
			case "cancel":
				c.cancel()
			case "unconfirmed":
				c.ready = false
				c.accept("image")
			case "small":
				c.end = image.Pt(4, 4)
				c.accept("ocr")
			}
			c.Close()
		})
	}
}

func TestCaptureSmokeHiddenPopup(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	dir := t.TempDir()
	if err := CaptureSmokeTest(dir); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "capture-toolbar.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil || img.Bounds() != image.Rect(0, 0, 800, 480) {
		t.Fatalf("invalid native rendering output: %v", err)
	}
	if len(captureWindows) != 0 {
		t.Fatal("hidden capture popup was not removed after smoke check")
	}
}
