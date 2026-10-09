package main

import (
	"image"
	"image/color"
	"testing"
)

func TestCaptureNegativeMonitorAndReverseDrag(t *testing.T) {
	desktop := image.Rect(-1920, -200, 2560, 1440)
	start := captureLocalPoint(image.Pt(-1600, 400), desktop)
	end := captureLocalPoint(image.Pt(-1800, 100), desktop)
	got := captureSelection(start, end, image.Rect(0, 0, desktop.Dx(), desktop.Dy()))
	want := image.Rect(120, 300, 320, 600)
	if got != want {
		t.Fatalf("negative origin and reversed drag: got %v, want %v", got, want)
	}
}

func TestCaptureClampsAndCopiesPixels(t *testing.T) {
	snapshot := image.NewRGBA(image.Rect(0, 0, 10, 8))
	snapshot.SetRGBA(3, 2, color.RGBA{31, 72, 114, 255})
	selected := captureSelection(image.Pt(12, 10), image.Pt(3, 2), snapshot.Bounds())
	cropped := captureCrop(snapshot, selected)
	if cropped == nil || cropped.Bounds() != image.Rect(0, 0, 7, 6) {
		t.Fatalf("unexpected crop: %v", cropped)
	}
	if got := cropped.RGBAAt(0, 0); got != (color.RGBA{31, 72, 114, 255}) {
		t.Fatalf("crop did not preserve origin pixel: %v", got)
	}
	cropped.SetRGBA(0, 0, color.RGBA{})
	if snapshot.RGBAAt(3, 2).R != 31 {
		t.Fatal("crop must own its pixels after frozen desktop buffers are released")
	}
	if captureCrop(snapshot, image.Rect(0, 0, 2, 6)) != nil {
		t.Fatal("two pixel accidental drag must not trigger OCR")
	}
}

func TestCaptureToolbarRoutesAndFitsAtBottom(t *testing.T) {
	screen := image.Rect(0, 0, 1920, 1080)
	selection := image.Rect(1650, 970, 1910, 1070)
	actions := captureToolbar(selection, screen, 1.5)
	if len(actions) != 4 {
		t.Fatal("expected four capture actions")
	}
	for i, mode := range []string{"ocr", "translate", "image", "cancel"} {
		a := actions[i]
		if a.Bounds.Intersect(screen) != a.Bounds || a.Bounds.Max.Y >= selection.Min.Y {
			t.Fatalf("toolbar should fit above bottom selection: %v", a.Bounds)
		}
		if got := captureActionAt(actions, a.Bounds.Min.Add(image.Pt(3, 3))); got != mode {
			t.Fatalf("button %d: got %s, want %s", i, got, mode)
		}
	}
	if captureActionAt(actions, image.Pt(0, 0)) != "" || captureShortcut('3', false) != "" || captureShortcut(27, false) != "cancel" || captureShortcut(13, true) != "image" {
		t.Fatal("outside clicks or keyboard selection readiness routed incorrectly")
	}
}
