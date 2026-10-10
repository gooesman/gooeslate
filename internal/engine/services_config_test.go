package engine

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestServiceDefaultsAndLegacyMigration(t *testing.T) {
	c := DefaultConfig()
	if c.Language != "auto" || !c.AutoCopyImage || len(c.TranslationServices) != 1 || len(c.OCRServices) != 1 || len(c.TTSServices) != 1 {
		t.Fatalf("incomplete defaults: %+v", c)
	}
	if err := ValidateServiceConfig(c); err != nil {
		t.Fatal(err)
	}
	c.BaseURL, c.Model, c.APIKey, c.OCRURL = "https://translation.example/v1", "legacy-model", "legacy-test-key", "https://ocr.example/recognize"
	c.TTS = TTSConfig{Provider: "system", Voice: "legacy-voice", Rate: -2, AutoRead: true}
	p := ActiveTranslation(c)
	if p.BaseURL != c.BaseURL || p.Model != c.Model || p.APIKey != c.APIKey || !p.DisableThinking {
		t.Fatalf("legacy translation was not migrated: %+v", p)
	}
	if ActiveOCR(c).URL != c.OCRURL || !ActiveOCR(c).Coordinates || ActiveTTS(c).Voice != "legacy-voice" {
		t.Fatal("legacy OCR or system voice was not migrated")
	}
	if c.TranslationServices[0].BaseURL != "" {
		t.Fatal("normalization mutated the input profile slice")
	}
	legacy := NormalizeConfig(Config{BaseURL: "https://old.example/v1", Model: "old", APIKey: "old-key", DisableThinking: true, OCRURL: "https://old.example/ocr", TTS: c.TTS})
	if ActiveTranslation(legacy).APIKey != "old-key" || ActiveOCR(legacy).URL != "https://old.example/ocr" || ActiveTTS(legacy).Rate != -2 {
		t.Fatal("raw legacy settings were not migrated")
	}
}

func TestServiceSelectionAndSecretSerialization(t *testing.T) {
	c := Config{APIKey: "secret-legacy", BaseURL: "https://legacy.example", Model: "legacy", TranslationServices: []TranslationService{{ID: "first", APIKey: "secret-first"}, {ID: "selected", BaseURL: "https://current.example", Model: "current", APIKey: "secret-selected"}}, ActiveTranslationID: "selected", OCRServices: []OCRService{{ID: "ocr", APIKey: "secret-ocr"}}, TTSServices: []TTSService{{ID: "tts", APIKey: "secret-tts"}}, Vocabulary: VocabularyConfig{APIKey: "secret-vocabulary"}}
	if ActiveTranslation(c).ID != "selected" || ActiveTranslation(c).APIKey != "secret-selected" {
		t.Fatal("active profile was replaced by legacy settings")
	}
	if ActiveOCR(c).ID != "ocr" || ActiveTTS(c).ID != "tts" {
		t.Fatal("active defaults are unstable")
	}
	raw, err := json.Marshal(NormalizeConfig(c))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-") || strings.Contains(string(raw), "api_key") {
		t.Fatal("plaintext keys were serialized")
	}
	if c.TranslationServices[0].Provider != "" {
		t.Fatal("normalization mutated source settings")
	}
}

func TestAutomaticTargetLanguage(t *testing.T) {
	for _, tc := range []struct{ text, target, source string }{
		{"Hello world", "简体中文", "英语"},
		{"你好，世界", "英语", "简体中文"},
		{"先完成这个 HTTP 请求", "英语", "简体中文"},
		{"A sentence with 中文 words.", "简体中文", "英语"},
		{"12345", "简体中文", "英语"},
	} {
		if got := ResolveTargetLanguage("auto", tc.text); got != tc.target {
			t.Errorf("target for %q: %s", tc.text, got)
		}
		if got := DetectSourceLanguage(tc.text); got != tc.source {
			t.Errorf("source for %q: %s", tc.text, got)
		}
		if got := ResolveTargetLanguage(" 日语 ", tc.text); got != "日语" {
			t.Fatal("fixed language was ignored")
		}
	}
}

func TestTypedTemplatesEscapeTextWithoutRecursiveExpansion(t *testing.T) {
	text := "quoted \"text\"\\path\n{{api_key}} 中文"
	vars := validationVariables()
	vars["text"] = text
	vars["api_key"] = "must-not-replace-input"
	vars["lines"] = []Line{{ID: 7, Text: text}}
	vars["speed"] = 1.25
	payload, err := templateObject(`{"input":"{{text}}","nested":{"lines":"{{lines}}","speed":"{{speed}}"},"label":"Translate {{text}}"}`, vars)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil || !json.Valid(encoded) {
		t.Fatal("template produced invalid JSON")
	}
	var roundtrip map[string]any
	json.Unmarshal(encoded, &roundtrip)
	if roundtrip["input"] != text || roundtrip["label"] != "Translate "+text || strings.Contains(string(encoded), "must-not-replace-input") {
		t.Fatal("source text was escaped incorrectly or interpreted as another template")
	}
	nested := roundtrip["nested"].(map[string]any)
	if nested["speed"] != 1.25 || nested["lines"].([]any)[0].(map[string]any)["id"] != float64(7) {
		t.Fatal("whole variables were not kept as typed JSON values")
	}
	for _, template := range []string{`{"text":"{{unknown}}"}`, `{"text":"{{text}"}`, `{"{{text}}":"value"}`, `{"text":{{text}}}`, `[]`, `{} {}`} {
		if _, err := templateObject(template, vars); err == nil {
			t.Errorf("accepted invalid template: %s", template)
		}
	}
}

func TestResponsePathsAndInvalidConfiguration(t *testing.T) {
	raw := []byte(`{"data":{"items":[{"text":"hello"}]}}`)
	for _, path := range []string{"data.items.0.text", "data.items[0].text", "$.data.items[0].text"} {
		v, err := responseValue(raw, path)
		if err != nil || v != "hello" {
			t.Fatalf("path %s: %v, %v", path, v, err)
		}
	}
	if v, err := responseValue([]byte(`[{"text":"root"}]`), "$[0].text"); err != nil || v != "root" {
		t.Fatalf("root array path: %v %v", v, err)
	}
	for _, path := range []string{"data.items[0", "data.items]", "data..items", "$.", "$bad", "data.items[-1]", "data.items[0]extra", "data.items.[0]", "data.items[*]"} {
		if err := validatePath(path); err == nil {
			t.Errorf("malformed response path accepted: %s", path)
		}
	}
	for _, raw := range []string{`{} {}`, `null false`, `not-json`} {
		if _, err := responseValue([]byte(raw), ""); err == nil {
			t.Error("invalid service JSON accepted")
		}
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.TranslationServices[0].BodyTemplate = `{"stream":true}` },
		func(c *Config) { c.TranslationServices[0].BodyTemplate = `{"stream":"false"}` },
		func(c *Config) { c.TranslationServices[0].Headers = `{"X-Test":"line\nfeed"}` },
		func(c *Config) { c.TranslationServices[0].Headers = `{"Content-Length":"1"}` },
		func(c *Config) { c.TranslationServices[0].Headers = `{"非ASCII":"x"}` },
		func(c *Config) { c.TranslationServices[0].SystemPrompt = `{{unknown}}` },
		func(c *Config) { c.TranslationServices[0].TimeoutMS = -1 },
		func(c *Config) { c.TranslationServices[0].RequestMode = "custom" },
		func(c *Config) { c.OCRServices[0].ResponsePath = "data[0" },
		func(c *Config) { c.OCRServices[0].FormFields = `[]` },
		func(c *Config) { c.TTSServices[0].Speed = 5 },
		func(c *Config) { c.Vocabulary.TimeoutMS = -1 },
		func(c *Config) { c.TranslationServices = append(c.TranslationServices, c.TranslationServices[0]) },
	} {
		c := DefaultConfig()
		change(&c)
		if err := ValidateServiceConfig(c); err == nil {
			t.Errorf("invalid config accepted: %+v", c)
		}
	}
}

func TestOpenAIEndpointPreservesQueryParameters(t *testing.T) {
	for _, tc := range []struct{ base, suffix, want string }{
		{"https://service.example/v1?deployment=test", "/chat/completions", "https://service.example/v1/chat/completions?deployment=test"},
		{"https://service.example/v1/chat/completions/?deployment=test", "/chat/completions", "https://service.example/v1/chat/completions?deployment=test"},
		{"https://service.example/v1?deployment=test", "/audio/speech", "https://service.example/v1/audio/speech?deployment=test"},
	} {
		if got := appendServicePath(tc.base, tc.suffix); got != tc.want {
			t.Errorf("endpoint query was corrupted: %s", got)
		}
	}
}

func TestNoCoordinateOCRPreservesIndependentLines(t *testing.T) {
	p := OCRService{TextField: "text", Coordinates: false}
	v, err := responseValue([]byte(`[{"text":"second"},{"text":"first"},{"text":""},{"text":"第三行"}]`), "")
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := decodeConfiguredOCR(v, p, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	lines := GroupLines(blocks)
	want := []Line{{ID: 0, Text: "second"}, {ID: 1, Text: "first"}, {ID: 2, Text: "第三行"}}
	if !reflect.DeepEqual(lines, want) || Text(blocks) != "second\r\nfirst\r\n第三行" {
		t.Fatalf("text entries were reordered or merged: %+v", lines)
	}
	p.Coordinates = true
	if _, err = decodeConfiguredOCR(`[{"text":"first"}] trailing`, p, 100, 100); err == nil {
		t.Fatal("OCR JSON string with trailing content accepted")
	}
}

func TestTextOnlyOCRPreservesJSONSourceCode(t *testing.T) {
	p := OCRService{TextField: "text", BoxField: "box", BoxFormat: "quad", Coordinates: false}
	for _, text := range []string{`{"foo":"bar"}`, `[1,2]`, `[]`, `[{"text":"source code"}]`, "{not valid JSON}\n[partial array"} {
		blocks, err := decodeConfiguredOCR(text, p, 100, 100)
		if err != nil || len(blocks) != 1 || blocks[0].Text != text || Text(blocks) != text {
			t.Fatalf("OCR source code was interpreted as a response schema: %q %+v %v", text, blocks, err)
		}
	}
	p.Coordinates = true
	blocks, err := decodeConfiguredOCR("```json\n[{\"text\":\"recognized\",\"box\":[[1,2],[20,2],[20,10],[1,10]]}]\n```", p, 100, 100)
	if err != nil || len(blocks) != 1 || blocks[0].Text != "recognized" || blocks[0].Bounds() != (Rect{X: 1, Y: 2, W: 19, H: 8}) {
		t.Fatalf("coordinate string response no longer parses: %+v %v", blocks, err)
	}
}
