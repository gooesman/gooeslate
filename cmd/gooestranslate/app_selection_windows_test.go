package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gooesman/gooeslate/internal/engine"
)

func TestSelectedTextTranslationUsesConfiguredServiceWithoutOCR(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/v1/chat/completions" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer TEST-SELECTION-KEY" {
			t.Error("selected text did not use the active configured translation service")
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			Template map[string]any `json:"chat_template_kwargs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "selected-model" || len(body.Messages) != 2 || body.Template["enable_thinking"] != false {
			t.Errorf("translation request mismatch: %v", err)
		}
		var lines []engine.Line
		if err := json.Unmarshal([]byte(body.Messages[1].Content), &lines); err != nil || len(lines) != 1 || lines[0].Text != "Hello world\nKeep the paragraph." {
			t.Error("selected text or its paragraph boundaries changed")
		}
		w.Header().Set("Content-Type", "application/json")
		content, _ := json.Marshal([]engine.Translation{{ID: 0, Text: "你好，世界\n保留段落。"}})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}}}})
	}))
	defer server.Close()
	cfg := engine.DefaultConfig()
	p := &cfg.TranslationServices[0]
	p.BaseURL, p.Model, p.APIKey = server.URL, "selected-model", "TEST-SELECTION-KEY"
	cfg.OCRServices[0].URL = "http://127.0.0.1:1/ocr-must-not-be-called"
	data, lines, err := selectedTextResult(cfg, "  Hello world\nKeep the paragraph.  ")
	if err != nil || data.Mode != "selection" || data.Image != "" || data.CoordinatesSupported || len(data.Blocks) != 0 || data.Language != "简体中文" || data.SourceLanguage != "英语" {
		t.Fatalf("selected text result mismatch: %+v %v", data, err)
	}
	values, err := engine.Translate(context.Background(), server.Client(), cfg, lines)
	if err != nil || !called || len(values) != 1 || values[0].Text != "你好，世界\n保留段落。" {
		t.Fatalf("selected text translation failed: %+v %v", values, err)
	}
	cfg.Language = "auto"
	data, _, err = selectedTextResult(cfg, "中文翻译测试")
	if err != nil || data.Language != "英语" {
		t.Fatal("Chinese selection did not use automatic English target")
	}
}

func TestSelectionConfigurationUpgradeAndDisable(t *testing.T) {
	cfg := engine.DefaultConfig()
	if err := json.Unmarshal([]byte(`{"language":"日语","hotkeys":{"translate":"Ctrl+Shift+T"}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Selection.Enabled || cfg.Selection.ShowButton || cfg.Hotkeys.Selection != "Ctrl+Alt+D" || cfg.Hotkeys.Translate != "Ctrl+Shift+T" || cfg.Language != "日语" {
		t.Fatal("selection defaults broke legacy settings")
	}
	updated, changed, err := mergeSettings(cfg, json.RawMessage(`{"selection":{"enabled":false,"show_button":false},"hotkeys":{"selection":"Ctrl+Alt+G"}}`))
	if err != nil || changed || updated.Selection.Enabled || updated.Selection.ShowButton || effectiveHotkeys(updated).Selection != "" || updated.Hotkeys.Selection != "Ctrl+Alt+G" || updated.Hotkeys.Translate != "Ctrl+Shift+T" {
		t.Fatal("disabling selection did not retain its saved shortcut or other preferences")
	}
	updated.Selection.Enabled = true
	if effectiveHotkeys(updated).Selection != "Ctrl+Alt+G" {
		t.Fatal("selection shortcut did not restore after enabling")
	}
	updated.Hotkeys.Selection = updated.Hotkeys.Translate
	if err := engine.ValidateHotkeys(updated.Hotkeys); err == nil || !strings.Contains(err.Error(), "划词翻译") {
		t.Fatal("selection shortcut collision was not detected")
	}
	for _, text := range []string{" ", strings.Repeat("a", selectionMaxBytes+1), "hidden\x00text"} {
		if _, _, err := selectedTextResult(cfg, text); err == nil {
			t.Fatal("invalid selection was accepted")
		}
	}
}
