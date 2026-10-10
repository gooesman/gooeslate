package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/gooesman/gooestranslate/internal/engine"
)

func TestLegacyVocabularyWebsiteIsRepairedWithoutLosingAuthorization(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	cfg := engine.DefaultConfig()
	cfg.Vocabulary.Enabled = true
	cfg.Vocabulary.BaseURL = "https://my.eudic.net/OpenAPI/Authorization"
	cfg.Vocabulary.APIKey = "NIS TEST-LEGACY-VOCABULARY-TOKEN"
	cfg.Vocabulary.CategoryID = "123456789012345678"
	// Reproduce the settings v0.3.0 allowed, including DPAPI encrypted token.
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig()
	if err != nil || loaded.Vocabulary.BaseURL != engine.DefaultVocabularyBaseURL || loaded.Vocabulary.APIKey != cfg.Vocabulary.APIKey || loaded.Vocabulary.CategoryID != cfg.Vocabulary.CategoryID || !loaded.Vocabulary.Enabled {
		t.Fatal("repair must only change the mistaken API address", err)
	}
}

func TestServiceSettingsPreserveCredentialsByIDWithoutMutatingCurrent(t *testing.T) {
	current := engine.DefaultConfig()
	current.TranslationServices[0].Model = "old-model"
	current.TranslationServices[0].APIKey = "TEST-TRANSLATION-SECRET"
	current.TranslationServices[0].EncryptedKey = "old-cipher"
	current.OCRServices[0].APIKey = "TEST-OCR-SECRET"
	current.Vocabulary.APIKey = "TEST-VOCABULARY-SECRET"
	patch := json.RawMessage(`{"translation_services":[{"id":"translation-copy","name":"副本","api_key":"","encrypted_key":"injected"},{"id":"translation-1","name":"现有","model":"new-model","api_key":"","encrypted_key":"injected"}],"active_translation":"translation-1","vocabulary":{"api_key":"","encrypted_key":"injected"}}`)
	next, changed, err := mergeSettings(current, patch)
	if err != nil || changed {
		t.Fatal("blank service credentials must be retained", err)
	}
	if next.TranslationServices[0].APIKey != "" || next.TranslationServices[0].EncryptedKey != "" {
		t.Fatal("new ID inherited or injected credential")
	}
	if next.TranslationServices[1].APIKey != current.TranslationServices[0].APIKey || next.TranslationServices[1].EncryptedKey != "old-cipher" {
		t.Fatal("existing profile credential was lost after reordering")
	}
	if current.TranslationServices[0].Model != "old-model" || len(current.TranslationServices) != 1 || next.Vocabulary.APIKey != current.Vocabulary.APIKey {
		t.Fatal("merge mutated active settings or lost book credential")
	}
	next, changed, err = mergeSettings(next, json.RawMessage(`{"translation_services":[{"id":"translation-1","clear_api_key":true}],"vocabulary":{"api_key":" NEW-VOCABULARY-KEY "}}`))
	if err != nil || !changed || next.TranslationServices[0].APIKey != "" || next.TranslationServices[0].EncryptedKey != "" || next.Vocabulary.APIKey != "NEW-VOCABULARY-KEY" {
		t.Fatal("explicit service key changes failed", err)
	}
	public := jsonString((&appState{cfg: current}).state())
	for _, secret := range []string{"TEST-TRANSLATION-SECRET", "TEST-OCR-SECRET", "TEST-VOCABULARY-SECRET", "old-cipher", "encrypted_key", `"api_key"`} {
		if strings.Contains(public, secret) {
			t.Fatal("profile secret leaked into WebView")
		}
	}
}

func TestAllServiceCredentialsDPAPIRoundTrip(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	cfg := engine.DefaultConfig()
	cfg.TranslationServices[0].APIKey = "TEST-TRANSLATION-KEY"
	cfg.OCRServices[0].APIKey = "TEST-OCR-KEY"
	cfg.TTSServices[0].APIKey = "TEST-TTS-KEY"
	cfg.Vocabulary.APIKey = "TEST-BOOK-KEY"
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"TEST-TRANSLATION-KEY", "TEST-OCR-KEY", "TEST-TTS-KEY", "TEST-BOOK-KEY"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("service key persisted in plaintext")
		}
	}
	loaded, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TranslationServices[0].APIKey != cfg.TranslationServices[0].APIKey || loaded.OCRServices[0].APIKey != cfg.OCRServices[0].APIKey || loaded.TTSServices[0].APIKey != cfg.TTSServices[0].APIKey || loaded.Vocabulary.APIKey != cfg.Vocabulary.APIKey {
		t.Fatal("service credential round trip failed")
	}
	if cfg.TranslationServices[0].EncryptedKey != "" {
		t.Fatal("saving mutated live service settings")
	}
}

func TestExplicitProfileRemovalCannotResurrectLegacySecret(t *testing.T) {
	current := engine.DefaultConfig()
	current.BaseURL = "https://example.com/v1"
	current.Model = "old-model"
	current.APIKey = "TEST-OLD-SECRET"
	current.EncryptedKey = "old-cipher"
	current = engine.NormalizeConfig(current)
	for _, raw := range []string{`{"translation_services":[]}`, `{"translation_services":[{"id":"translation-1","base_url":"","model":"","clear_api_key":true}]}`} {
		next, keyChanged, err := mergeSettings(current, json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if !keyChanged {
			t.Fatal("deleting or clearing a credential must also clear an obsolete decrypt error")
		}
		for _, profile := range next.TranslationServices {
			if profile.APIKey != "" || profile.EncryptedKey != "" || profile.BaseURL != "" || profile.Model != "" {
				t.Fatal("legacy profile resurrected deleted settings")
			}
		}
		if current.APIKey != "TEST-OLD-SECRET" || current.TranslationServices[0].APIKey != current.APIKey {
			t.Fatal("failed edit mutated existing credentials")
		}
	}
}

func TestUnreadableProfilePreservedAndRecoverable(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	cfg := engine.DefaultConfig()
	cfg.TranslationServices[0].EncryptedKey = "unreadable-original"
	cfg.OCRServices[0].APIKey = "TEST-VALID-OCR-KEY"
	updated, _, err := mergeSettings(cfg, json.RawMessage(`{"theme":"light"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = saveConfig(updated); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig()
	if err == nil || loaded.TranslationServices[0].EncryptedKey != "unreadable-original" || loaded.OCRServices[0].APIKey != "TEST-VALID-OCR-KEY" {
		t.Fatal("unreadable profile corrupted other credentials")
	}
	updated, _, err = mergeSettings(loaded, json.RawMessage(`{"translation_services":[{"id":"translation-1","clear_api_key":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = saveConfig(updated); err != nil {
		t.Fatal(err)
	}
	if _, err = loadConfig(); err != nil {
		t.Fatal("explicit profile clear failed", err)
	}
}
