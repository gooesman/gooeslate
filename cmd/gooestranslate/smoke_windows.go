package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/gooesman/gooestranslate/internal/engine"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"time"
	"unsafe"
)

func testDirectory() string {
	if len(os.Args) > 2 {
		return os.Args[2]
	}
	return "test-artifacts-v2"
}
func writePNG(file string, img image.Image) error {
	f, e := os.Create(file)
	if e != nil {
		return e
	}
	e = png.Encode(f, img)
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	return e
}
func syntheticImage() (*image.RGBA, error) {
	w, h := 640, 180
	dc := call(gdi, "CreateCompatibleDC", 0)
	defer call(gdi, "DeleteDC", dc)
	info := bitmapinfo{Header: bitmapheader{Size: 40, Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}}
	var bits unsafe.Pointer
	bmp := call(gdi, "CreateDIBSection", dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return nil, fmt.Errorf("无法创建测试图")
	}
	defer call(gdi, "DeleteObject", bmp)
	old := call(gdi, "SelectObject", dc, bmp)
	defer call(gdi, "SelectObject", dc, old)
	f := font(28, false)
	defer call(gdi, "DeleteObject", f)
	fill(dc, rect{0, 0, 640, 180}, rgb(255, 255, 255))
	drawText(dc, "Hello world", rect{20, 20, 400, 60}, f, rgb(15, 20, 30), 0)
	drawText(dc, "Original image translation", rect{20, 85, 610, 135}, f, rgb(15, 20, 30), 0)
	call(gdi, "GdiFlush")
	return pixelsImage(unsafe.Slice((*byte)(bits), w*h*4), w, h), nil
}
func smokeTest() error {
	directory := testDirectory()
	if e := os.MkdirAll(directory, 0700); e != nil {
		return e
	}
	if e := CaptureSmokeTest(directory); e != nil {
		return e
	}
	original, e := syntheticImage()
	if e != nil {
		return e
	}
	if e = writePNG(filepath.Join(directory, "ocr-fixture.png"), original); e != nil {
		return e
	}
	blocks := []engine.Block{{Text: "Hello world", Box: [4][2]float64{{20, 20}, {250, 20}, {250, 60}, {20, 60}}}, {Text: "Original image translation", Box: [4][2]float64{{20, 85}, {480, 85}, {480, 135}, {20, 135}}}}
	lines := engine.GroupLines(blocks)
	translations := map[int]string{0: "你好，世界", 1: "原图翻译"}
	result, e := renderExport(original, lines, translations)
	if e != nil || bytes.Equal(result.Pix, original.Pix) {
		return fmt.Errorf("译文图片绘制验证失败")
	}
	if e = writePNG(filepath.Join(directory, "translated-preview.png"), result); e != nil {
		return e
	}
	secret := []byte("TEST-NOT-A-REAL-KEY")
	encrypted, e := protect(secret, false)
	if e != nil {
		return e
	}
	decrypted, e := protect(encrypted, true)
	if e != nil || !bytes.Equal(decrypted, secret) || bytes.Contains(encrypted, secret) {
		return fmt.Errorf("密钥加密验证失败")
	}
	speech := NewSpeechService()
	voices, e := speech.Voices()
	speech.Stop()
	speech.Close()
	if e != nil {
		return e
	}
	report := map[string]any{"capture_cache_and_toolbar": true, "translation_png_export": true, "dpapi_round_trip": true, "voices_enumerated": len(voices), "desktop_capture_performed": false, "audio_played": false}
	if os.Args[1] == "--live-test" {
		cfg, e := loadConfig()
		if e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		start := time.Now()
		blocks, e = engine.RecognizeConfigured(ctx, engine.HTTPClient(), cfg, original)
		if e != nil {
			return e
		}
		report["real_ocr_ms"] = time.Since(start).Milliseconds()
		report["real_ocr_blocks"] = len(blocks)
		lines = engine.GroupLines(blocks)
		report["resolved_target_language"] = engine.ResolveTargetLanguage(cfg.Language, engine.Text(blocks))
		start = time.Now()
		values, e := engine.Translate(ctx, engine.HTTPClient(), cfg, lines)
		if e != nil {
			return e
		}
		report["real_translation_ms"] = time.Since(start).Milliseconds()
		report["real_translation_lines"] = values
		translations = map[int]string{}
		for _, v := range values {
			translations[v.ID] = v.Text
		}
		picture, e := renderExport(original, lines, translations)
		if e != nil {
			return e
		}
		if e = writePNG(filepath.Join(directory, "live-translated-preview.png"), picture); e != nil {
			return e
		}
		start = time.Now()
		entry, e := engine.LookupDictionary(ctx, engine.HTTPClient(), cfg, "workflow")
		if e != nil {
			return e
		}
		report["real_dictionary_ms"] = time.Since(start).Milliseconds()
		report["real_dictionary"] = entry
	}
	raw, _ := json.MarshalIndent(report, "", "  ")
	if e = os.WriteFile(filepath.Join(directory, "smoke-test.json"), raw, 0600); e != nil {
		return e
	}
	fmt.Println(string(raw))
	return nil
}
func (a *appState) runUIProbe() {
	directory := testDirectory()
	if err := os.MkdirAll(directory, 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		a.post(func() { a.testExitCode = 1; a.quit() })
		return
	}
	time.Sleep(1200 * time.Millisecond)
	a.post(func() {
		a.settings.browser.Eval("getState().then(s=>uiProbe(!!window.__uiReady&&document.querySelectorAll('.nav button').length>=6&&!('api_key' in s.config)&&!('encrypted_key' in s.config)))")
		a.result.browser.Eval("getResult().then(r=>uiProbe(!!window.__uiReady&&typeof window.setResult==='function'&&typeof window.setDictionary==='function'))")
	})
	ok := true
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for i := 0; i < 2; i++ {
		select {
		case passed := <-a.uiProbe:
			ok = ok && passed
		case <-timer.C:
			ok = false
			i = 2
		}
	}
	bridgeOK := ok
	if bridgeOK {
		a.post(func() {
			img, err := syntheticImage()
			if err != nil {
				a.uiProbe <- false
				return
			}
			var encoded bytes.Buffer
			if err := png.Encode(&encoded, img); err != nil {
				a.uiProbe <- false
				return
			}
			blocks := []engine.Block{{Text: "Hello world", Box: [4][2]float64{{20, 20}, {250, 20}, {250, 60}, {20, 60}}}, {Text: "Original image translation", Box: [4][2]float64{{20, 85}, {480, 85}, {480, 135}, {20, 135}}}}
			a.img = img
			a.lines = engine.GroupLines(blocks)
			a.data = resultData{Mode: "image", Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes()), Width: 640, Height: 180, OriginalText: engine.Text(blocks), TranslatedText: "你好，世界\n原图翻译", Blocks: blocks, Lines: []resultLine{}, Translations: []engine.Translation{{ID: 0, Text: "你好，世界"}, {ID: 1, Text: "原图翻译"}}, DictionaryEnabled: true, VocabularyEnabled: true, CoordinatesSupported: true, Theme: "dark", Language: "简体中文", LanguageSetting: "auto", SourceLanguage: "英语", Status: "翻译完成 · 测试图片"}
			for _, line := range a.lines {
				a.data.Lines = append(a.data.Lines, resultLine{line.ID, line.Text, line.Bounds})
			}
			a.pushResult()
			a.result.browser.Eval("getResult().then(r=>uiProbe(r.mode==='image'&&r.width===640&&r.coordinates_supported&&r.language_setting==='auto'&&r.lines.length===2&&document.getElementById('title').textContent==='原图翻译'&&document.getElementById('app').dataset.view==='image'))")
		})
		select {
		case passed := <-a.uiProbe:
			ok = passed
		case <-time.After(8 * time.Second):
			ok = false
		}
	}
	report := map[string]any{"settings_and_result_bridge": bridgeOK, "native_webview_html_loaded": bridgeOK, "secret_fields_not_exposed": bridgeOK, "image_mode_push": ok, "desktop_capture_performed": false, "personal_settings_written": false}
	if ok && os.Args[1] == "--render-smoke-test" {
		if err := a.runRenderProbe(directory); err != nil {
			fmt.Fprintln(os.Stderr, err)
			ok = false
		}
		report["visible_window_pixels"] = ok
	}
	report["passed"] = ok
	raw, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(filepath.Join(directory, "ui-smoke-test.json"), raw, 0600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		ok = false
	}
	fmt.Println(string(raw))
	a.post(func() {
		if !ok {
			a.testExitCode = 1
		}
		a.quit()
	})
}

func (a *appState) runRenderProbe(directory string) (err error) {
	report := map[string]any{"desktop_capture_performed": false, "personal_settings_written": false}
	defer func() {
		report["passed"] = err == nil
		if err != nil {
			report["error"] = err.Error()
		}
		raw, _ := json.MarshalIndent(report, "", "  ")
		if writeErr := os.WriteFile(filepath.Join(directory, "render-smoke-test.json"), raw, 0600); writeErr != nil && err == nil {
			err = writeErr
		}
		fmt.Println(string(raw))
	}()
	for _, phase := range []string{"initial", "restore", "minimized", "maximized", "normal"} {
		if phase == "restore" || phase == "minimized" {
			hidden := make(chan error, 1)
			a.post(func() {
				command := uintptr(0)
				if phase == "minimized" {
					command = 6
				}
				for _, w := range []*webWindow{a.settings, a.result} {
					call(user, "ShowWindow", w.hwnd, command)
					visible, err := controllerVisible(w)
					if err != nil || visible {
						hidden <- fmt.Errorf("%s 收起后控制器仍然可见", w.kind)
						return
					}
				}
				hidden <- nil
			})
			select {
			case err := <-hidden:
				if err != nil {
					return err
				}
			case <-time.After(8 * time.Second):
				return fmt.Errorf("%s 收起验证超时", phase)
			}
			report[phase+"_controllers_hidden"] = true
			time.Sleep(100 * time.Millisecond)
		}
		a.post(func() {
			switch phase {
			case "restore":
				// The screenshot controller restores its owner with raw ShowWindow.
				call(user, "ShowWindow", a.settings.hwnd, 5)
				call(user, "ShowWindow", a.result.hwnd, 5)
			case "maximized":
				call(user, "ShowWindow", a.settings.hwnd, 3)
				call(user, "ShowWindow", a.result.hwnd, 3)
			case "normal":
				call(user, "ShowWindow", a.settings.hwnd, 9)
				call(user, "ShowWindow", a.result.hwnd, 9)
			}
			if phase == "initial" || phase == "minimized" {
				a.showSettings("")
				a.showResult()
			}
		})
		time.Sleep(500 * time.Millisecond)
		completed := make(chan error, 2)
		a.post(func() {
			for _, w := range []*webWindow{a.settings, a.result} {
				visible, err := controllerVisible(w)
				bounds, boundsErr := controllerBounds(w)
				report[phase+"_"+w.kind+"_controller"] = map[string]any{"visible": visible, "width": bounds.R - bounds.L, "height": bounds.B - bounds.T, "navigation_ok": w.navigationOK}
				fmt.Printf("%s %s: visible=%v bounds=%dx%d navigation=%v\n", phase, w.kind, visible, bounds.R-bounds.L, bounds.B-bounds.T, w.navigationOK)
				if err != nil || boundsErr != nil || !visible || bounds.R <= bounds.L || bounds.B <= bounds.T {
					completed <- fmt.Errorf("%s 控制器不可见或尺寸无效", w.kind)
					continue
				}
				if !w.navigationOK {
					completed <- fmt.Errorf("%s 页面导航失败：%d", w.kind, w.navigationError)
					continue
				}
				if call(user, "IsWindowVisible", w.hwnd) == 0 {
					completed <- fmt.Errorf("%s 窗口未显示", w.kind)
					continue
				}
				captureWebView(w, filepath.Join(directory, phase+"-"+w.kind+".png"), func(err error) {
					completed <- err
				})
			}
		})
		for i := 0; i < 2; i++ {
			select {
			case err := <-completed:
				if err != nil {
					return fmt.Errorf("%s 渲染验证：%w", phase, err)
				}
			case <-time.After(12 * time.Second):
				return fmt.Errorf("%s 渲染验证超时", phase)
			}
		}
		report[phase+"_settings_and_result_pixels"] = true
	}
	for _, pane := range []string{"translation", "ocr", "vocabulary", "tts"} {
		a.post(func() {
			a.settings.browser.Eval("window.openSettingsPane(" + jsonString(pane) + ");uiProbe(document.getElementById('pane-' + " + jsonString(pane) + ").classList.contains('active'))")
		})
		select {
		case passed := <-a.uiProbe:
			if !passed {
				return fmt.Errorf("%s 服务页未正确切换", pane)
			}
		case <-time.After(8 * time.Second):
			return fmt.Errorf("%s 服务页切换超时", pane)
		}
		time.Sleep(400 * time.Millisecond)
		completed := make(chan error, 1)
		a.post(func() {
			captureWebView(a.settings, filepath.Join(directory, "settings-"+pane+".png"), func(err error) { completed <- err })
		})
		select {
		case err := <-completed:
			if err != nil {
				return fmt.Errorf("%s 服务页渲染：%w", pane, err)
			}
		case <-time.After(12 * time.Second):
			return fmt.Errorf("%s 服务页渲染超时", pane)
		}
		report[pane+"_service_page_pixels"] = true
	}
	return nil
}
