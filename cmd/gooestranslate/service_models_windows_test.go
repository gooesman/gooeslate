package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gooesman/gooeslate/internal/engine"
)

func TestModelCatalogDraftUsesMatchingSecretWithoutRequiredModel(t *testing.T) {
	c := probeFixture()
	c.OCRServices = []engine.OCRService{{ID: "ocr", Provider: "openai", URL: "https://ocr.test/v1", APIKey: "ocr-saved"}}
	c.TTSServices = []engine.TTSService{{ID: "tts", Provider: "openai", URL: "https://tts.test/v1", APIKey: "tts-saved"}}
	before := cloneConfig(c)
	for _, tc := range []struct{ kind, id, key string }{
		{"translation", "one", "private-sample-key"},
		{"ocr", "ocr", "ocr-saved"},
		{"tts", "tts", "tts-saved"},
	} {
		for _, variant := range []struct {
			id, supplied, want string
			clear              bool
		}{
			{tc.id, "", tc.key, false},
			{tc.id, "", "", true},
			{tc.id, "new-key", "new-key", false},
			{"new-profile", "", "", false},
		} {
			raw, _ := json.Marshal(map[string]any{"kind": tc.kind, "service": map[string]any{"id": variant.id, "provider": "openai", "base_url": "https://draft.test", "url": "https://draft.test", "model": "", "api_key": variant.supplied, "clear_api_key": variant.clear}})
			cfg, kind, err := prepareModelCatalog(c, raw)
			if err != nil || kind != tc.kind {
				t.Fatalf("%s draft rejected without model", tc.kind)
			}
			var key, model string
			switch kind {
			case "translation":
				p := engine.ActiveTranslation(cfg)
				key, model = p.APIKey, p.Model
			case "ocr":
				p := engine.ActiveOCR(cfg)
				key, model = p.APIKey, p.Model
			case "tts":
				p := engine.ActiveTTS(cfg)
				key, model = p.APIKey, p.Model
			}
			if key != variant.want || kind != "tts" && model != "" {
				t.Fatalf("%s draft lost credential semantics or required a model", kind)
			}
		}
	}
	if !reflect.DeepEqual(c, before) {
		t.Fatal("model query mutated saved configuration")
	}
}

func TestModelCatalogRejectsUnrelatedOrInvalidRequests(t *testing.T) {
	for _, raw := range []string{`null`, `{"kind":"vocabulary","service":{}}`, `{"kind":"dictionary","service":{}}`, `{"kind":"translation","service":null}`, `{"kind":"translation","service":{}}`} {
		if _, _, err := prepareModelCatalog(probeFixture(), json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid model request accepted: %s", raw)
		}
	}
}
