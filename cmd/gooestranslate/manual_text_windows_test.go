package main

import (
	"context"
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gooesman/gooestranslate/internal/engine"
)

func TestManualTextInvalidInputKeepsLiveTranslation(t *testing.T) {
	for _, input := range []struct {
		name, text string
		requestID  int
	}{
		{"empty", "", 1},
		{"whitespace", " \n\t", 1},
		{"null", "a\x00b", 1},
		{"invalid UTF8", string([]byte{0xff}), 1},
		{"too long", strings.Repeat("a", selectionMaxBytes+1), 1},
		{"invalid request ID", "Hello", -1},
	} {
		t.Run(input.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			img := image.NewRGBA(image.Rect(0, 0, 2, 2))
			a := &appState{cfg: engine.DefaultConfig(), img: img, cancel: cancel, generation: 4, snapshotID: 8, data: resultData{DocumentID: 7, OriginalText: "Keep this", Mode: "image", Busy: true, Image: "keep-image"}}
			if err := a.prepareManualText(input.text, "auto", input.requestID); err == nil {
				t.Fatal("invalid manual input was accepted")
			}
			if ctx.Err() != nil || a.cancel == nil || a.generation != 4 || a.snapshotID != 8 || a.img != img || a.data.DocumentID != 7 || a.data.OriginalText != "Keep this" || !a.data.Busy || a.data.Image != "keep-image" || a.data.Mode != "image" {
				t.Fatal("invalid input changed or canceled the current screenshot/translation")
			}
		})
	}
}

func TestManualTextReplacesBusyScreenshotWithoutStaleCoordinates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lookup, lookupCancel := context.WithCancel(context.Background())
	vocabulary, vocabularyCancel := context.WithCancel(context.Background())
	a := &appState{cfg: engine.DefaultConfig(), img: image.NewRGBA(image.Rect(0, 0, 2, 2)), cancel: cancel, lookupCancel: lookupCancel, vocabularyCancel: vocabularyCancel, generation: 11, lookupGeneration: 2, vocabularyGeneration: 3, snapshotID: 20, lastImageSent: "old-image", captureClipboardError: "old-error", data: resultData{DocumentID: 18, Mode: "image", Busy: true, Image: "old-image", CoordinatesSupported: true, Width: 2, Height: 2}, lines: []engine.Line{{ID: 8, Text: "Old OCR", Bounds: engine.Rect{W: 1, H: 1}}}}
	if err := a.prepareManualText("  你好\n这是手动输入。  ", "auto", 12); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != context.Canceled || lookup.Err() != context.Canceled || vocabulary.Err() != context.Canceled || a.cancel != nil || a.generation != 12 || a.lookupGeneration != 3 || a.vocabularyGeneration != 4 {
		t.Fatal("previous translation and word requests can still complete after a manual replacement")
	}
	if a.data.DocumentID != 18 || a.data.TextRequestID != 12 || !a.data.ManualInput || a.data.Mode != "selection" || a.data.OriginalText != "你好\n这是手动输入。" || a.data.Language != "英语" || a.data.SourceLanguage != "简体中文" {
		t.Fatalf("manual replacement identity/language mismatch: %+v", a.data)
	}
	if a.img != nil || a.data.Image != "" || a.data.CoordinatesSupported || a.data.Width != 0 || a.data.Height != 0 || len(a.data.Blocks) != 0 || len(a.data.Translations) != 0 || a.lastImageSent != "" || a.captureClipboardError != "" || a.snapshotID != 21 || a.data.Busy {
		t.Fatal("manual translation retained screenshot coordinates, stale results, or busy state")
	}
	if len(a.lines) != 1 || a.lines[0].ID != 0 || a.lines[0].Text != a.data.OriginalText || a.lines[0].Bounds != (engine.Rect{}) {
		t.Fatal("manual paragraphs were not prepared as one coordinate-free input")
	}
}

func TestManualTextConfiguredServiceAndLanguageOverrideWithoutOCR(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer TEST-MANUAL-KEY" {
			t.Error("manual text did not use the configured translation service")
		}
		var body struct {
			Model    string                     `json:"model"`
			Messages []struct{ Content string } `json:"messages"`
			Template map[string]any             `json:"chat_template_kwargs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "manual-model" || len(body.Messages) != 2 || body.Template["enable_thinking"] != false {
			t.Errorf("configured request was not preserved: %v", err)
		}
		var lines []engine.Line
		if len(body.Messages) == 2 {
			if !strings.Contains(body.Messages[0].Content, "日语") {
				t.Error("explicit target language was not passed to the custom prompt")
			}
			if err := json.Unmarshal([]byte(body.Messages[1].Content), &lines); err != nil || len(lines) != 1 || lines[0].Text != "Hello\nKeep this paragraph." {
				t.Error("manual multiline input changed")
			}
		}
		content, _ := json.Marshal([]engine.Translation{{ID: 0, Text: "こんにちは\n段落を保持します。"}})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}}}})
	}))
	defer server.Close()
	cfg := engine.DefaultConfig()
	cfg.TranslationServices[0].BaseURL = server.URL
	cfg.TranslationServices[0].Model = "manual-model"
	cfg.TranslationServices[0].APIKey = "TEST-MANUAL-KEY"
	cfg.OCRServices[0].URL = "http://127.0.0.1:1/ocr-must-not-be-called"
	data, lines, err := manualTextResult(cfg, "Hello\nKeep this paragraph.", "日语")
	if err != nil || data.LanguageSetting != "日语" || data.Language != "日语" || !data.ManualInput {
		t.Fatalf("manual language override failed: %+v %v", data, err)
	}
	cfg.Language = data.LanguageSetting
	values, err := engine.Translate(context.Background(), server.Client(), cfg, lines)
	if err != nil || requests != 1 || len(values) != 1 || values[0].Text != "こんにちは\n段落を保持します。" {
		t.Fatalf("manual translation failed: %+v %v", values, err)
	}
}
