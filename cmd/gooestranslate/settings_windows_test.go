package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unsafe"

	"github.com/gooesman/gooestranslate/internal/engine"
)

func TestSettingsPreserveUnreadableCredential(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	current := engine.DefaultConfig()
	current.EncryptedKey = "unreadable-but-recoverable-original"
	updated, changed, err := mergeSettings(current, json.RawMessage(`{"theme":"light","api_key":"","encrypted_key":"injected"}`))
	if err != nil || changed || updated.EncryptedKey != current.EncryptedKey {
		t.Fatalf("unrelated settings replaced protected credential: changed=%v err=%v", changed, err)
	}
	if err = saveConfig(updated); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig()
	if err == nil || loaded.EncryptedKey != current.EncryptedKey || loaded.Theme != "light" {
		t.Fatal("unreadable encrypted credential did not survive saving unrelated preferences")
	}
	updated, changed, err = mergeSettings(loaded, json.RawMessage(`{"clear_api_key":true}`))
	if err != nil || !changed || updated.EncryptedKey != "" || updated.APIKey != "" {
		t.Fatal("explicit clear did not reset credential")
	}
	if err = saveConfig(updated); err != nil {
		t.Fatal(err)
	}
	if _, err = loadConfig(); err != nil {
		t.Fatal("explicit clear did not recover settings")
	}
}

func TestSettingsReplacementAndStateNeverExposeSecret(t *testing.T) {
	current := engine.DefaultConfig()
	current.APIKey = "TEST-PRIVATE-KEY"
	current.EncryptedKey = "TEST-PRIVATE-CIPHERTEXT"
	updated, changed, err := mergeSettings(current, json.RawMessage(`{"theme":"light","api_key":"  "}`))
	if err != nil || changed || updated.APIKey != current.APIKey {
		t.Fatal("blank input did not preserve existing key")
	}
	updated, changed, err = mergeSettings(current, json.RawMessage(`{"api_key":" TEST-NEW-KEY "}`))
	if err != nil || !changed || updated.APIKey != "TEST-NEW-KEY" || updated.EncryptedKey != "" {
		t.Fatal("key replacement failed")
	}
	a := &appState{cfg: current, loadError: "重新填写密钥"}
	text := jsonString(a.state())
	if strings.Contains(text, "TEST-PRIVATE") || strings.Contains(text, "encrypted_key") || strings.Contains(text, `"api_key"`) {
		t.Fatal("private credential leaked into web view state")
	}
}

func TestLegacySettingsReceiveNewDefaults(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	if err := os.MkdirAll(strings.TrimSuffix(configFile(), "settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFile(), []byte(`{"model":"example-model","language":"English","disable_thinking":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil || cfg.Model != "example-model" || cfg.DisableThinking || !cfg.CloseToTray || cfg.Hotkeys.Image != "Ctrl+Alt+I" || !cfg.Dictionary.Enabled {
		t.Fatalf("legacy preferences were not upgraded correctly: %v", err)
	}
}

func TestLargeExportFontFitsLongTranslation(t *testing.T) {
	dc := call(gdi, "CreateCompatibleDC", 0)
	if dc == 0 {
		t.Fatal("cannot create native text measurement context")
	}
	defer call(gdi, "DeleteDC", dc)
	text := strings.Repeat("这是一段需要完整显示的长译文。", 6)
	fits := func(size int) bool {
		f := font(size, false)
		old := call(gdi, "SelectObject", dc, f)
		r := rect{R: 160}
		call(user, "DrawTextW", dc, uintptr(unsafe.Pointer(str(text))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), 0x400|0x10|0x800)
		call(gdi, "SelectObject", dc, old)
		call(gdi, "DeleteObject", f)
		return r.B <= 120 && r.R <= 160
	}
	size := fitExportFont(98, fits)
	if size >= 54 || !fits(size) || fits(size+1) {
		t.Fatalf("large text clipped or unnecessarily shrunk, size=%d", size)
	}
}
