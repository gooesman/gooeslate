package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"runtime"
	"testing"
	"unsafe"
)

func captureColorFixture(width, height, seed int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{byte(x*17 + seed*13), byte(y*29 + seed*7), byte(x*3 + y*11 + seed*23), 255})
		}
	}
	return img
}

func readCaptureTestBitmap(t *testing.T, bitmap captureBitmap, width, height int) *image.RGBA {
	t.Helper()
	if call(gdi, "GdiFlush") == 0 {
		t.Fatal("synthetic drawing flush failed")
	}
	if old := call(gdi, "SelectObject", bitmap.dc, bitmap.previous); old == 0 || old == ^uintptr(0) {
		t.Fatal("synthetic bitmap deselection failed")
	}
	defer call(gdi, "SelectObject", bitmap.dc, bitmap.bitmap)
	img, err := readCapturedPixels(width, height, func(data []byte, info *bitmapinfo) uintptr {
		return call(gdi, "GetDIBits", bitmap.dc, bitmap.bitmap, 0, u(height), uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(info)), 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func assertCapturePixelsEqual(t *testing.T, got, want *image.RGBA, context string) {
	t.Helper()
	if got.Bounds() != want.Bounds() {
		t.Fatalf("%s: got bounds %v, want %v", context, got.Bounds(), want.Bounds())
	}
	if bytes.Equal(got.Pix, want.Pix) {
		return
	}
	for y := 0; y < got.Bounds().Dy(); y++ {
		for x := 0; x < got.Bounds().Dx(); x++ {
			if actual, expected := got.RGBAAt(x, y), want.RGBAAt(x, y); actual != expected {
				t.Fatalf("%s: pixel (%d,%d) got %v, want %v", context, x, y, actual, expected)
			}
		}
	}
}

func TestCapturedPixelsRejectPartialRead(t *testing.T) {
	for _, rows := range []uintptr{0, 1, 2, 4} {
		t.Run(fmt.Sprint(rows), func(t *testing.T) {
			img, err := readCapturedPixels(5, 3, func(data []byte, info *bitmapinfo) uintptr {
				if len(data) != 60 || info.Header.Height != -3 || info.Header.BitCount != 32 {
					t.Fatal("capture read did not request complete top-down BGRA")
				}
				for i := range data {
					data[i] = 127
				}
				return rows
			})
			if img != nil || err == nil {
				t.Fatalf("partial or excessive row count must not deliver an image: %v %v", img, err)
			}
		})
	}
	img, err := readCapturedPixels(1, 1, func(data []byte, _ *bitmapinfo) uintptr {
		copy(data, []byte{12, 34, 56, 0})
		return 1
	})
	if err != nil || img.RGBAAt(0, 0) != (color.RGBA{56, 34, 12, 255}) {
		t.Fatalf("native BGRA must convert to opaque RGBA: %v %v", img, err)
	}
	if img, err := readCapturedPixels(0, 1, func([]byte, *bitmapinfo) uintptr { t.Fatal("invalid size must not call GDI"); return 0 }); img != nil || err == nil {
		t.Fatal("invalid capture size was accepted")
	}
}

func TestCaptureDCImageUsesOnlySyntheticPixels(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	const width, height = 40, 32
	source, err := newCaptureBitmap(width, height, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer source.close()
	for seed := 0; seed < 50; seed++ {
		fixture := captureColorFixture(width, height, seed)
		paintPixels(source.dc, imagePixels(fixture), width, height, rect{R: width, B: height})
		if call(gdi, "GdiFlush") == 0 {
			t.Fatal("unable to prepare synthetic source")
		}
		got, err := captureDCImage(source.dc, 3, 5, 29, 21)
		if err != nil {
			t.Fatal(err)
		}
		want := image.NewRGBA(image.Rect(0, 0, 29, 21))
		for y := 0; y < 21; y++ {
			for x := 0; x < 29; x++ {
				want.SetRGBA(x, y, fixture.RGBAAt(x+3, y+5))
			}
		}
		assertCapturePixelsEqual(t, got, want, fmt.Sprintf("capture frame %d", seed))
	}
	if img, err := captureDCImage(0, 0, 0, 20, 20); img != nil || err == nil {
		t.Fatal("missing source DC must be rejected")
	}
}

func TestCaptureBitmapRejectsMalformedPixelsAndClearsEmptyBuffer(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for _, size := range [][2]int{{0, 1}, {1, -1}, {100001, 1}, {100000, 100000}} {
		if bitmap, err := newCaptureBitmap(size[0], size[1], nil); err == nil || bitmap.dc != 0 {
			t.Fatalf("invalid native bitmap dimensions allocated resources: %v", size)
		}
	}
	if bitmap, err := newCaptureBitmap(5, 3, make([]byte, 59)); err == nil || bitmap.dc != 0 {
		t.Fatal("truncated pixels must be rejected before native allocation")
	}
	bitmap, err := newCaptureBitmap(5, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bitmap.close()
	got := readCaptureTestBitmap(t, bitmap, 5, 3)
	for y := 0; y < 3; y++ {
		for x := 0; x < 5; x++ {
			if got.RGBAAt(x, y) != (color.RGBA{A: 255}) {
				t.Fatal("empty bitmap must be explicitly cleared, never stale colors")
			}
		}
	}
}

func TestCapturePartialPaintMatchesFullRepaint(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	const width, height = 1000, 640
	fixture := captureColorFixture(width, height, 7)
	before := bytes.Clone(fixture.Pix)
	for _, scale := range []float64{1, 1.5, 2} {
		t.Run(fmt.Sprint(scale), func(t *testing.T) {
			incremental := &CaptureController{mode: "choose"}
			full := &CaptureController{mode: "choose"}
			for _, c := range []*CaptureController{incremental, full} {
				if err := c.prepare(fixture, image.Rect(-1000, -640, 0, 0)); err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				c.scale = scale
				c.textFont, c.hintFont = font(c.size(14), false), font(c.size(12), false)
				c.dragging = true
			}
			// Before the first full WM_PAINT the cache already contains the dimmed
			// snapshot, so a first partial exposure cannot reveal undefined pixels.
			if err := incremental.paint(incremental.back.dc, image.Rect(0, 0, 3, 3)); err != nil {
				t.Fatal(err)
			}
			if err := full.paint(full.back.dc, fixture.Bounds()); err != nil {
				t.Fatal(err)
			}
			assertCapturePixelsEqual(t, readCaptureTestBitmap(t, incremental.back, width, height), readCaptureTestBitmap(t, full.back, width, height), "initial partial exposure")
			for step := 0; step < 32; step++ {
				old := incremental.dirty()
				start := image.Pt((step*109)%1160-80, (step*83)%800-80)
				end := image.Pt(width-(step*97)%1160+80, height-(step*61)%800+80)
				for _, c := range []*CaptureController{incremental, full} {
					c.start, c.end = start, end
					c.ready, c.dragging = step%3 != 0, step%3 == 0
					c.hoverAction = []string{"", "ocr", "translate", "image"}[step%4]
				}
				invalid := old.Union(incremental.dirty()).Intersect(fixture.Bounds())
				if err := incremental.paint(incremental.back.dc, invalid); err != nil {
					t.Fatal(err)
				}
				if err := full.paint(full.back.dc, fixture.Bounds()); err != nil {
					t.Fatal(err)
				}
				assertCapturePixelsEqual(t, readCaptureTestBitmap(t, incremental.back, width, height), readCaptureTestBitmap(t, full.back, width, height), fmt.Sprintf("scale %g step %d", scale, step))
			}
			crop := captureCrop(fixture, image.Rect(17, 23, 83, 71))
			if crop == nil || crop.RGBAAt(0, 0) != fixture.RGBAAt(17, 23) || !bytes.Equal(fixture.Pix, before) {
				t.Fatal("overlay painting must not modify the original screenshot or confirmed crop")
			}
		})
	}
}

func TestCapturePaintRejectsInvalidSnapshot(t *testing.T) {
	for _, snapshot := range []*image.RGBA{nil, image.NewRGBA(image.Rectangle{}), {Pix: []byte{0}, Stride: 8, Rect: image.Rect(0, 0, 2, 2)}} {
		c := &CaptureController{snapshot: snapshot}
		if err := c.paint(0, image.Rect(0, 0, 2, 2)); err == nil {
			t.Fatal("invalid capture must return an error before any native repaint")
		}
		if err := c.prepare(snapshot, image.Rect(0, 0, 2, 2)); err == nil {
			t.Fatal("invalid snapshot must be rejected before reading pixels")
		}
	}
}
