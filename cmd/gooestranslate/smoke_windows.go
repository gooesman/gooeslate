package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gooesman/gooestranslate/internal/engine"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

// Exercises our own hidden window and renderer. Never captures the desktop.
func smokeTest() error {
	directory := "test-artifacts"
	if len(os.Args) > 2 {
		directory = os.Args[2]
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	cfg := engine.DefaultConfig()
	app = &appState{cfg: cfg, controls: map[int]uintptr{}, labels: map[int]uintptr{}, scale: 1, queue: make(chan func(), 32), selected: map[int]bool{}, translations: map[int]string{}}
	for _, c := range []struct {
		name string
		proc uintptr
	}{{"GooesTranslateMain", syscall.NewCallback(mainProc)}, {"GooesTranslateCanvas", syscall.NewCallback(canvasProc)}, {"GooesTranslateCapture", syscall.NewCallback(captureProc)}} {
		if err := register(c.name, c.proc, 32512); err != nil {
			return err
		}
	}
	app.hwnd = create("GooesTranslateMain", "gooestranslate self test", 0xcf0000|0x02000000, 0, 0, 0, 1240, 850, 0, 0)
	if app.hwnd == 0 {
		return fmt.Errorf("native main window failed")
	}
	defer call(user, "DestroyWindow", app.hwnd)
	app.createUI()
	app.layout()
	for _, id := range []int{101, 102, 103, 104, 105, 106, 107, 201, 202, 203, 204, 205, 206, 207, 208, 209, 210} {
		if app.controls[id] == 0 {
			return fmt.Errorf("control %d missing", id)
		}
	}
	b := image.NewRGBA(image.Rect(0, 0, 640, 180))
	draw.Draw(b, b.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	app.setImage(b)
	// Draw a synthetic English source image using the same Windows font API.
	dc := call(gdi, "CreateCompatibleDC", 0)
	defer call(gdi, "DeleteDC", dc)
	info := bitmapinfo{Header: bitmapheader{Size: 40, Width: 640, Height: -180, Planes: 1, BitCount: 32}}
	var bits unsafe.Pointer
	bmp := call(gdi, "CreateDIBSection", dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 {
		return fmt.Errorf("fixture bitmap failed")
	}
	defer call(gdi, "DeleteObject", bmp)
	old := call(gdi, "SelectObject", dc, bmp)
	defer call(gdi, "SelectObject", dc, old)
	fill(dc, rect{0, 0, 640, 180}, rgb(255, 255, 255))
	f := font(28, false)
	defer call(gdi, "DeleteObject", f)
	drawText(dc, "Hello world", rect{20, 20, 400, 60}, f, rgb(15, 20, 30), 0)
	drawText(dc, "Original image translation", rect{20, 85, 610, 135}, f, rgb(15, 20, 30), 0)
	call(gdi, "GdiFlush")
	source := pixelsImage(unsafe.Slice((*byte)(bits), 640*180*4), 640, 180)
	app.setImage(source)
	fixture, err := os.Create(filepath.Join(directory, "ocr-fixture.png"))
	if err != nil {
		return err
	}
	err = png.Encode(fixture, source)
	fixture.Close()
	if err != nil {
		return err
	}
	app.blocks = []engine.Block{{Text: "Hello world", Box: [4][2]float64{{20, 20}, {250, 20}, {250, 60}, {20, 60}}}, {Text: "Original image translation", Box: [4][2]float64{{20, 85}, {480, 85}, {480, 135}, {20, 135}}}}
	app.lines = engine.GroupLines(app.blocks)
	app.translations = map[int]string{0: "你好，世界", 1: "原图翻译"}
	translated, err := app.composite()
	if err != nil {
		return err
	}
	if bytes.Equal(translated.Pix, source.Pix) {
		return fmt.Errorf("translation renderer did not change pixels")
	}
	out, err := os.Create(filepath.Join(directory, "translated-preview.png"))
	if err != nil {
		return err
	}
	err = png.Encode(out, translated)
	out.Close()
	if err != nil {
		return err
	}
	app.view = engine.Fit(640, 180, engine.Rect{W: 640, H: 180})
	app.dragStart = engine.Point{X: 30, Y: 30}
	app.dragEnd = app.dragStart
	app.selection()
	if text(app.controls[210]) != "Hello world" {
		return fmt.Errorf("original-image text selection failed")
	}
	secret := []byte("TEST-NOT-A-REAL-KEY")
	encrypted, err := protect(secret, false)
	if err != nil {
		return err
	}
	decrypted, err := protect(encrypted, true)
	if err != nil || !bytes.Equal(secret, decrypted) || bytes.Contains(encrypted, secret) {
		return fmt.Errorf("DPAPI round trip failed")
	}
	report := map[string]any{"native_window_and_controls": true, "original_image_text_selection": true, "translation_pixels_exported": true, "dpapi_round_trip": true, "desktop_capture_performed": false, "manual_desktop_acceptance": false}
	if os.Args[1] == "--live-test" {
		cfg, e := loadConfig()
		if e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		start := time.Now()
		blocks, e := engine.Recognize(ctx, engine.HTTPClient(), cfg.OCRURL, source)
		if e != nil {
			return e
		}
		report["real_ocr_ms"] = time.Since(start).Milliseconds()
		report["real_ocr_blocks"] = len(blocks)
		report["real_ocr_coordinates"] = blocks
		lines := engine.GroupLines(blocks)
		if len(lines) == 0 {
			return fmt.Errorf("live OCR returned no text")
		}
		start = time.Now()
		translated, e := engine.Translate(ctx, engine.HTTPClient(), cfg, lines)
		if e != nil {
			return e
		}
		report["real_translation_ms"] = time.Since(start).Milliseconds()
		report["real_translation_lines"] = translated
		app.blocks = blocks
		app.lines = lines
		app.translations = map[int]string{}
		for _, v := range translated {
			app.translations[v.ID] = v.Text
		}
		picture, e := app.composite()
		if e != nil {
			return e
		}
		f, e := os.Create(filepath.Join(directory, "live-translated-preview.png"))
		if e != nil {
			return e
		}
		e = png.Encode(f, picture)
		f.Close()
		if e != nil {
			return e
		}
	}
	raw, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(directory, "smoke-test.json"), raw, 0600); err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}
