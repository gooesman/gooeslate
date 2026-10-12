package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gooesman/gooeslate/internal/engine"
)

func TestAnthropicSettingsPersistAndTransferWithoutCredentialLeak(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	cfg := engine.DefaultConfig()
	cfg.TranslationServices = []engine.TranslationService{{
		ID: "anthropic-test", Name: "Anthropic test", Provider: "anthropic",
		BaseURL: "https://api.example.test/v1", Model: "claude-test-model",
		APIKey: "TEST-ANTHROPIC-PRIVATE-KEY", MaxTokens: 8192,
	}}
	cfg.ActiveTranslationID = "anthropic-test"
	cfg = engine.NormalizeConfig(cfg)
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	p := engine.ActiveTranslation(loaded)
	if p.Provider != "anthropic" || p.MaxTokens != 8192 || p.APIKey != cfg.TranslationServices[0].APIKey {
		t.Fatal("Anthropic profile or protected credential did not survive persistence")
	}
	a := &appState{cfg: loaded, testMode: true}
	public := jsonString(a.state())
	if !strings.Contains(public, `"max_tokens":8192`) || strings.Contains(public, "TEST-ANTHROPIC-PRIVATE") {
		t.Fatal("public state lost output limit or exposed a credential")
	}
	for _, includeCredentials := range []bool{false, true} {
		exported, err := encodeConfiguration(loaded, includeCredentials)
		if err != nil {
			t.Fatal(err)
		}
		if !includeCredentials && strings.Contains(string(exported), "TEST-ANTHROPIC-PRIVATE") {
			t.Fatal("credential-free export exposed an Anthropic key")
		}
		imported, _, err := decodeConfiguration(exported)
		if err != nil {
			t.Fatal(err)
		}
		p := engine.ActiveTranslation(imported)
		if p.Provider != "anthropic" || p.MaxTokens != 8192 || p.ResponsePath != "content" {
			t.Fatal("configuration transfer lost Anthropic protocol settings")
		}
	}
}

func TestAnthropicDraftModelCatalogRetainsOnlyOwnCredential(t *testing.T) {
	current := engine.DefaultConfig()
	current.TranslationServices = []engine.TranslationService{{
		ID: "anthropic-saved", Provider: "anthropic", APIKey: "TEST-OWN-ANTHROPIC-KEY",
		BaseURL: "https://api.example.test/v1", Model: "claude-example", MaxTokens: 4096,
	}}
	current.ActiveTranslationID = "anthropic-saved"
	for _, id := range []string{"anthropic-saved", "new-anthropic"} {
		draft := map[string]any{"kind": "translation", "service": map[string]any{
			"id": id, "name": "Draft Anthropic", "provider": "anthropic",
			"base_url": "https://api.example.test/v1", "api_key": "", "max_tokens": 16384,
		}}
		raw, err := json.Marshal(draft)
		if err != nil {
			t.Fatal(err)
		}
		candidate, kind, err := prepareModelCatalog(current, raw)
		if err != nil {
			t.Fatal(err)
		}
		p := engine.ActiveTranslation(candidate)
		if kind != "translation" || p.Provider != "anthropic" || p.MaxTokens != 16384 {
			t.Fatal("model catalog draft lost Anthropic fields")
		}
		if id == "anthropic-saved" && p.APIKey != "TEST-OWN-ANTHROPIC-KEY" {
			t.Fatal("own saved credential was not retained")
		}
		if id != "anthropic-saved" && p.APIKey != "" {
			t.Fatal("a new Anthropic profile borrowed another profile's key")
		}
	}
}
