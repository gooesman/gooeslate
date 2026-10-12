package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gooesman/gooeslate/internal/engine"
)

func transferTestConfig() engine.Config {
	c := engine.DefaultConfig()
	c.Theme = "light"
	c.Language = "auto"
	c.AutoCopyImage = false
	c.Selection = engine.SelectionConfig{Enabled: true, ShowButton: false}
	c.Hotkeys.Selection = "Ctrl+Alt+Q"
	c.TranslationServices[0].Name = "模型甲"
	c.TranslationServices[0].BaseURL = "https://model.example.invalid/v1"
	c.TranslationServices[0].Model = "test-model"
	c.TranslationServices[0].APIKey = "TEST-TRANSFER-TRANSLATION-SECRET"
	c.TranslationServices[0].EncryptedKey = "TEST-TRANSFER-WINDOWS-BLOB"
	c.TranslationServices[0].Headers = `{"Authorization":"Bearer {{api_key}}","Accept":"application/json"}`
	c.TranslationServices[0].BodyTemplate = `{"model":"{{model}}","messages":"{{messages}}","chat_template_kwargs":{"enable_thinking":false}}`
	c.TranslationServices[0].RequestMode = "custom"
	second := c.TranslationServices[0]
	second.ID = "translation-second"
	second.Name = "模型乙"
	second.Model = "another-model"
	second.APIKey = "TEST-TRANSFER-SECOND-SECRET"
	second.EncryptedKey = ""
	c.TranslationServices = append(c.TranslationServices, second)
	c.ActiveTranslationID = second.ID
	c.BaseURL, c.Model, c.APIKey, c.EncryptedKey = second.BaseURL, second.Model, second.APIKey, second.EncryptedKey
	c.OCRServices[0].URL = "https://ocr.example.invalid/v1/ocr"
	c.OCRServices[0].APIKey = "TEST-TRANSFER-OCR-SECRET"
	c.OCRServices[0].Headers = `{"X-API-Key":"{{api_key}}","Accept":"application/json"}`
	c.OCRURL = c.OCRServices[0].URL
	c.TTSServices[0].Provider = "openai"
	c.TTSServices[0].URL = "https://tts.example.invalid/v1"
	c.TTSServices[0].Model = "test-voice-model"
	c.TTSServices[0].Voice = "alloy"
	c.TTSServices[0].APIKey = "TEST-TRANSFER-TTS-SECRET"
	c.TTS.Provider = "openai"
	c.TTS.Voice = "alloy"
	c.Vocabulary.Enabled = true
	c.Vocabulary.APIKey = "TEST-TRANSFER-VOCABULARY-SECRET"
	c.Vocabulary.CategoryID = "test-category"
	c.Dictionary = engine.DictionaryConfig{Enabled: true, Provider: "local", LocalPath: `D:\Dictionaries\test.tsv`}
	return c
}

func transferDocument(t *testing.T, include bool) map[string]any {
	t.Helper()
	raw, err := encodeConfiguration(transferTestConfig(), include)
	if err != nil {
		t.Fatal(err)
	}
	value, err := configurationJSONValue(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value.(map[string]any)
}
func transferBytes(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func transferProfile(doc map[string]any, field string) map[string]any {
	return doc["config"].(map[string]any)[field].([]any)[0].(map[string]any)
}

func TestConfigurationExportKeepsLoginStartupLocal(t *testing.T) {
	current := transferTestConfig()
	current.StartAtLogin = true
	for _, include := range []bool{false, true} {
		raw, err := encodeConfiguration(current, include)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("start_at_login")) {
			t.Fatal("machine startup preference was included in a portable export")
		}
		imported, _, err := decodeConfiguration(raw)
		if err != nil {
			t.Fatal(err)
		}
		if imported.StartAtLogin {
			t.Fatal("portable export unexpectedly enables startup on another computer")
		}
		if !current.StartAtLogin {
			t.Fatal("export changed the local startup preference")
		}
	}
}

func TestConfigurationTransferRoundTripWithCredentials(t *testing.T) {
	original := transferTestConfig()
	before := cloneConfig(original)
	raw, err := encodeConfiguration(original, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatal("export mutated the caller's configuration")
	}
	if bytes.Contains(raw, []byte("encrypted_key")) || bytes.Contains(raw, []byte("TEST-TRANSFER-WINDOWS-BLOB")) {
		t.Fatal("machine-bound encrypted credentials were exported")
	}
	for _, secret := range []string{"TEST-TRANSFER-TRANSLATION-SECRET", "TEST-TRANSFER-SECOND-SECRET", "TEST-TRANSFER-OCR-SECRET", "TEST-TRANSFER-TTS-SECRET", "TEST-TRANSFER-VOCABULARY-SECRET"} {
		if !bytes.Contains(raw, []byte(secret)) {
			t.Fatal("explicitly included credential missing")
		}
	}
	result, summary, err := decodeConfiguration(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range configCredentials(&before) {
		*ref.encrypted = ""
	}
	if !reflect.DeepEqual(result, before) {
		t.Fatal("round-trip changed services, prompts, hotkeys, selection or preferences")
	}
	if summary.TranslationCount != 2 || summary.OCRCount != 1 || summary.TTSCount != 1 || !summary.ContainsCredentials || summary.CredentialsCount != 6 || summary.SchemaVersion != 1 || summary.OfflinePathCount != 1 {
		t.Fatalf("unexpected nonsecret preview summary: %+v", summary)
	}
	parsed, _ := configurationJSONValue(raw)
	exportedAt := parsed.(map[string]any)["exported_at"].(string)
	ts, err := time.Parse(time.RFC3339Nano, exportedAt)
	if err != nil || ts.Location() != time.UTC {
		t.Fatal("export timestamp is not UTC")
	}
}

func TestConfigurationTransferDefaultOmitsCredentials(t *testing.T) {
	original := transferTestConfig()
	before := cloneConfig(original)
	raw, err := encodeConfiguration(original, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatal("credential omission mutated saved state")
	}
	if bytes.Contains(raw, []byte("TEST-TRANSFER-")) || bytes.Contains(raw, []byte("encrypted_key")) || bytes.Contains(raw, []byte(`"api_key":`)) {
		t.Fatal("default export contained a dedicated credential")
	}
	result, summary, err := decodeConfiguration(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range configCredentials(&before) {
		*ref.key, *ref.encrypted = "", ""
	}
	if !reflect.DeepEqual(result, before) {
		t.Fatal("default export changed nonsecret services or template placeholders")
	}
	if summary.ContainsCredentials || summary.CredentialsCount != 0 {
		t.Fatal("empty credential backup advertised credentials")
	}
	if result.TranslationServices[0].Headers != original.TranslationServices[0].Headers {
		t.Fatal("template Authorization header was not preserved")
	}
}

func TestConfigurationTransferRedactsCustomAuthorization(t *testing.T) {
	c := transferTestConfig()
	p := &c.TranslationServices[0]
	p.Headers = `{"Authorization":"Bearer TEST-HEADER-AUTH","X-API-Key":"TEST-HEADER-KEY","X-Auth-Token":"TEST-X-AUTH","Api-Token":"TEST-API-TOKEN","Secret-Key":"TEST-SECRET-KEY","X-Normal":"keep","X-Templated":"{{api_key}}"}`
	p.BodyTemplate = `{"messages":"{{messages}}","authorization":"Bearer TEST-BODY-AUTH","nested":{"api_key":"TEST-BODY-KEY","auth_token":"TEST-BODY-TOKEN","secret_key":"TEST-BODY-SECRET"},"preserve":"{{model}}","text":"prefix TEST-TRANSFER-TRANSLATION-SECRET suffix","url":"https://user:TEST-USER-PASSWORD@example.invalid/path?api_key=TEST-QUERY-KEY&keep=yes"}`
	c.OCRServices[0].FormFields = `{"return_word_box":"True","token":"TEST-FORM-TOKEN","nested":{"client_secret":"TEST-FORM-SECRET"},"api_key":"{{api_key}}"}`
	c.OCRServices[0].URL = "https://ocr.example.invalid/v1/ocr?access_token=TEST-URL-TOKEN&keep=1"
	c.OCRURL = c.OCRServices[0].URL
	raw, err := encodeConfiguration(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("TEST-")) {
		t.Fatal("known custom authorization or configured secret was leaked")
	}
	result, summary, err := decodeConfiguration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ContainsCredentials {
		t.Fatal("redacted credentials were still detected")
	}
	var headers, body, form map[string]any
	json.Unmarshal([]byte(result.TranslationServices[0].Headers), &headers)
	json.Unmarshal([]byte(result.TranslationServices[0].BodyTemplate), &body)
	json.Unmarshal([]byte(result.OCRServices[0].FormFields), &form)
	if headers["X-Normal"] != "keep" || headers["X-Templated"] != "{{api_key}}" || body["messages"] != "{{messages}}" || body["preserve"] != "{{model}}" || form["return_word_box"] != "True" || form["api_key"] != "{{api_key}}" {
		t.Fatal("redaction damaged nonsecret fields or placeholders")
	}
	if body["url"] != "https://example.invalid/path?keep=yes" || result.OCRServices[0].URL != "https://ocr.example.invalid/v1/ocr?keep=1" {
		t.Fatal("URL user info/query credentials were not removed while preserving ordinary parameters")
	}
	if body["text"] != "prefix  suffix" {
		t.Fatal("dedicated key occurrence in custom content was not scrubbed")
	}
}

func TestConfigurationTransferIncludesCustomAuthorizationOnRequest(t *testing.T) {
	c := transferTestConfig()
	c.TranslationServices[0].Headers = `{"Authorization":"Bearer TEST-CUSTOM-INCLUDED","X-Auth-Token":"TEST-CUSTOM-XAUTH"}`
	c.OCRServices[0].FormFields = `{"token":"TEST-CUSTOM-FORM","return_word_box":"True"}`
	raw, err := encodeConfiguration(c, true)
	if err != nil {
		t.Fatal(err)
	}
	result, summary, err := decodeConfiguration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.TranslationServices[0].Headers != c.TranslationServices[0].Headers || result.OCRServices[0].FormFields != c.OCRServices[0].FormFields || summary.CredentialsCount != 9 {
		t.Fatal("explicit custom credentials were not preserved and summarized")
	}
}

func TestConfigurationTransferRejectsInvalidImport(t *testing.T) {
	tests := []struct {
		name   string
		change func(map[string]any)
	}{
		{"wrong format", func(d map[string]any) { d["format"] = "local-settings" }},
		{"unsupported version", func(d map[string]any) { d["schema_version"] = 2 }},
		{"version type", func(d map[string]any) { d["schema_version"] = "1" }},
		{"extra envelope field", func(d map[string]any) { d["private_data"] = "TEST-SENSITIVE-VALUE" }},
		{"missing envelope field", func(d map[string]any) { delete(d, "includes_credentials") }},
		{"null config", func(d map[string]any) { d["config"] = nil }},
		{"bad timestamp", func(d map[string]any) { d["exported_at"] = "invalid" }},
		{"key declaration", func(d map[string]any) { d["includes_credentials"] = false }},
		{"encrypted key", func(d map[string]any) {
			transferProfile(d, "translation_services")["encrypted_key"] = "TEST-MACHINE-SECRET"
		}},
		{"empty encrypted key", func(d map[string]any) { transferProfile(d, "translation_services")["encrypted_key"] = "" }},
		{"public flags", func(d map[string]any) { transferProfile(d, "translation_services")["has_api_key"] = true }},
		{"clear flag", func(d map[string]any) { transferProfile(d, "translation_services")["clear_api_key"] = true }},
		{"key type", func(d map[string]any) { transferProfile(d, "translation_services")["api_key"] = 123 }},
		{"null profile", func(d map[string]any) { d["config"].(map[string]any)["translation_services"] = []any{nil} }},
		{"empty profile list", func(d map[string]any) { d["config"].(map[string]any)["translation_services"] = []any{} }},
		{"null profile list", func(d map[string]any) { d["config"].(map[string]any)["ocr_services"] = nil }},
		{"missing profile list", func(d map[string]any) { delete(d["config"].(map[string]any), "tts_services") }},
		{"blank service id", func(d map[string]any) { transferProfile(d, "translation_services")["id"] = "" }},
		{"whitespace service id", func(d map[string]any) { transferProfile(d, "ocr_services")["id"] = " " }},
		{"repeated service id", func(d map[string]any) {
			a := d["config"].(map[string]any)["translation_services"].([]any)
			a[1].(map[string]any)["id"] = a[0].(map[string]any)["id"]
		}},
		{"unknown active id", func(d map[string]any) { d["config"].(map[string]any)["active_translation"] = "missing" }},
		{"missing active id", func(d map[string]any) { delete(d["config"].(map[string]any), "active_ocr") }},
		{"blank active id", func(d map[string]any) { d["config"].(map[string]any)["active_tts"] = "" }},
		{"blank provider", func(d map[string]any) { transferProfile(d, "translation_services")["provider"] = "" }},
		{"missing provider", func(d map[string]any) { delete(transferProfile(d, "ocr_services"), "provider") }},
		{"unknown provider", func(d map[string]any) { transferProfile(d, "tts_services")["provider"] = "unsupported" }},
		{"zero timeout", func(d map[string]any) { transferProfile(d, "translation_services")["timeout_ms"] = 0 }},
		{"fractional timeout", func(d map[string]any) { transferProfile(d, "translation_services")["timeout_ms"] = 1000.5 }},
		{"zero speed", func(d map[string]any) { transferProfile(d, "tts_services")["speed"] = 0 }},
		{"blank request mode", func(d map[string]any) { transferProfile(d, "translation_services")["request_mode"] = "" }},
		{"blank box format", func(d map[string]any) { transferProfile(d, "ocr_services")["box_format"] = "" }},
		{"blank encoding", func(d map[string]any) { transferProfile(d, "ocr_services")["image_encoding"] = "" }},
		{"blank vocab provider", func(d map[string]any) { d["config"].(map[string]any)["vocabulary"].(map[string]any)["provider"] = "" }},
		{"zero vocab timeout", func(d map[string]any) { d["config"].(map[string]any)["vocabulary"].(map[string]any)["timeout_ms"] = 0 }},
		{"invalid legacy provider", func(d map[string]any) {
			d["config"].(map[string]any)["tts"].(map[string]any)["provider"] = "unsupported"
		}},
		{"wrong preference type", func(d map[string]any) { d["config"].(map[string]any)["auto_copy_image"] = "yes" }},
		{"null nested preference", func(d map[string]any) { d["config"].(map[string]any)["selection"] = nil }},
		{"wrong case alias", func(d map[string]any) { d["config"].(map[string]any)["Theme"] = "dark" }},
		{"malformed template", func(d map[string]any) {
			transferProfile(d, "translation_services")["body_template"] = `{"x":"TEST-SENSITIVE-VALUE"`
		}},
		{"unknown template variable", func(d map[string]any) { transferProfile(d, "translation_services")["system_prompt"] = "{{unknown}}" }},
		{"invalid URL", func(d map[string]any) {
			transferProfile(d, "translation_services")["base_url"] = "TEST-SENSITIVE-VALUE"
		}},
		{"duplicate hotkey", func(d map[string]any) {
			d["config"].(map[string]any)["hotkeys"].(map[string]any)["ocr"] = d["config"].(map[string]any)["hotkeys"].(map[string]any)["capture"]
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := transferDocument(t, true)
			test.change(doc)
			_, summary, err := decodeConfiguration(transferBytes(t, doc))
			if err == nil {
				t.Fatal("invalid import was accepted")
			}
			if strings.Contains(err.Error(), "TEST-") || !reflect.DeepEqual(summary, configurationImportSummary{}) {
				t.Fatal("rejected import leaked credential data or returned a successful preview")
			}
		})
	}
}

func TestConfigurationTransferRejectsWrongCredentialDeclaration(t *testing.T) {
	tests := []struct{ name, field, value string }{
		{"header", "headers", `{"Authorization":"Bearer TEST-CUSTOM-SECRET"}`},
		{"alternate header", "headers", `{"X-Auth-Token":"TEST-CUSTOM-SECRET"}`},
		{"nested body", "body_template", `{"nested":{"token":"TEST-CUSTOM-SECRET"}}`},
		{"query", "base_url", "https://example.invalid/v1?api_key=TEST-CUSTOM-SECRET"},
		{"duplicate header", "headers", `{"Authorization":"safe","Authorization":"TEST-CUSTOM-SECRET"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := transferDocument(t, false)
			transferProfile(doc, "translation_services")[test.field] = test.value
			_, _, err := decodeConfiguration(transferBytes(t, doc))
			if err == nil || strings.Contains(err.Error(), "TEST-CUSTOM-SECRET") {
				t.Fatal("credential-free declaration accepted literal custom credentials or leaked them in error")
			}
		})
	}
	doc := transferDocument(t, false)
	transferProfile(doc, "translation_services")["api_key"] = "{{api_key}}"
	if _, _, err := decodeConfiguration(transferBytes(t, doc)); err == nil {
		t.Fatal("nonempty native key was mistaken for a request placeholder")
	}
}

func TestConfigurationTransferStrictJSONAndBOM(t *testing.T) {
	raw, err := encodeConfiguration(transferTestConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = decodeConfiguration(append([]byte{0xef, 0xbb, 0xbf}, raw...)); err != nil {
		t.Fatal("UTF-8 BOM import failed")
	}
	cases := [][]byte{
		nil, []byte(`{}`), []byte(`{"api_key":"TEST-SENSITIVE-VALUE"}`), []byte(`[]`),
		append(append([]byte(nil), raw...), []byte(` {}`)...),
		bytes.Replace(raw, []byte(`"format": "gooestranslate-config"`), []byte(`"format":"gooestranslate-config","format":"gooestranslate-config"`), 1),
		bytes.Replace(raw, []byte(`"theme": "light"`), []byte(`"theme":"light","theme":"dark"`), 1),
		bytes.Replace(raw, []byte("another-model"), []byte{'x', 0xff}, 1),
		bytes.Repeat([]byte(" "), configurationFileSizeLimit+1),
	}
	for index, bad := range cases {
		if _, _, err = decodeConfiguration(bad); err == nil || strings.Contains(err.Error(), "TEST-SENSITIVE-VALUE") {
			t.Fatalf("invalid JSON case %d accepted or leaked input", index)
		}
	}
	deeplyNested := []byte(strings.Repeat("[", 65) + `"data"` + strings.Repeat("]", 65))
	if _, err = configurationJSONValue(deeplyNested); err == nil {
		t.Fatal("excessive JSON nesting accepted")
	}
}

func TestConfigurationTransferMissingOptionalFieldsReceivesSafeDefaults(t *testing.T) {
	doc := transferDocument(t, false)
	config := doc["config"].(map[string]any)
	delete(config, "selection")
	delete(config, "auto_copy_image")
	delete(config["hotkeys"].(map[string]any), "selection")
	for _, field := range []string{"request_mode", "timeout_ms", "headers", "response_path"} {
		delete(transferProfile(doc, "translation_services"), field)
	}
	for _, field := range []string{"timeout_ms", "image_encoding", "box_format"} {
		delete(transferProfile(doc, "ocr_services"), field)
	}
	for _, field := range []string{"timeout_ms", "speed"} {
		delete(transferProfile(doc, "tts_services"), field)
	}
	result, _, err := decodeConfiguration(transferBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Selection.Enabled || result.Selection.ShowButton || !result.AutoCopyImage || result.Hotkeys.Selection != "Ctrl+Alt+D" || result.TranslationServices[0].TimeoutMS != 90000 || result.OCRServices[0].ImageEncoding != "base64" || result.TTSServices[0].Speed != 1 {
		t.Fatal("missing optional preferences did not receive safe defaults")
	}
}

func TestConfigurationTransferUnreadableKeysAndUnconfiguredServices(t *testing.T) {
	c := engine.DefaultConfig()
	c.TranslationServices[0].EncryptedKey = "TEST-UNREADABLE-BLOB"
	if _, err := encodeConfiguration(c, true); err == nil {
		t.Fatal("credential-inclusive export silently dropped an unreadable key")
	}
	raw, err := encodeConfiguration(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("TEST-UNREADABLE-BLOB")) {
		t.Fatal("protected credential escaped exclusion")
	}
	result, summary, err := decodeConfiguration(raw)
	if err != nil || result.TranslationServices[0].BaseURL != "" || result.TranslationServices[0].APIKey != "" || summary.ContainsCredentials {
		t.Fatal("new unconfigured application could not be backed up and restored")
	}
}

func TestConfigurationTransferPreservesPlaceholderNamesAndURLQueries(t *testing.T) {
	if got := replaceConfigurationSecrets("{{api_key}} api_key {{model}}", []string{"api_key"}); got != "{{api_key}}  {{model}}" {
		t.Fatal("known secret redaction broke a placeholder name")
	}
	value, count := configurationURLCredentials("https://example.invalid/v1?api_key={{api_key}}&keep=a%20b&token=TEST-SECRET", true)
	if count != 1 || value != "https://example.invalid/v1?api_key={{api_key}}&keep=a%20b" {
		t.Fatal("query redaction changed placeholder or unrelated URL encoding")
	}
}

func TestConfigurationTransferExplicitEmptyProfilesDoNotInheritLegacyAliases(t *testing.T) {
	raw, err := encodeConfiguration(engine.DefaultConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := configurationJSONValue(raw)
	doc := value.(map[string]any)
	config := doc["config"].(map[string]any)
	config["base_url"] = "https://legacy.example.invalid/v1"
	config["model"] = "legacy-model"
	config["ocr_url"] = "https://legacy-ocr.example.invalid/v1/ocr"
	config["tts"].(map[string]any)["voice"] = "legacy-voice"
	result, _, err := decodeConfiguration(transferBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if result.TranslationServices[0].BaseURL != "" || result.TranslationServices[0].Model != "" || result.OCRServices[0].URL != "" || result.TTSServices[0].Voice != "" || result.BaseURL != "" || result.OCRURL != "" || result.TTS.Voice != "" {
		t.Fatal("legacy aliases resurrected an explicit empty service setting")
	}
}

func TestConfigurationTransferExportSizeLimit(t *testing.T) {
	config := engine.DefaultConfig()
	config.TranslationServices[0].SystemPrompt = strings.Repeat("a", configurationFileSizeLimit)
	if _, err := encodeConfiguration(config, false); err == nil {
		t.Fatal("oversized portable export was generated")
	}
}

func TestConfigurationTransferUnusedLegacyCredentialDoesNotPopulateProfiles(t *testing.T) {
	raw, err := encodeConfiguration(engine.DefaultConfig(), false)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := configurationJSONValue(raw)
	doc := value.(map[string]any)
	doc["includes_credentials"] = true
	config := doc["config"].(map[string]any)
	config["api_key"] = "TEST-LEGACY-ONLY-SECRET"
	config["base_url"] = "https://legacy.example.invalid/v1"
	config["model"] = "legacy-model"
	result, summary, err := decodeConfiguration(transferBytes(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if result.TranslationServices[0].APIKey != "" || result.APIKey != "" || result.BaseURL != "" || !summary.ContainsCredentials {
		t.Fatal("unused legacy credential was injected into an explicit empty profile")
	}
}

func TestConfigurationTransferPreservesNonsecretAuthorizationFlags(t *testing.T) {
	config := engine.DefaultConfig()
	config.TranslationServices[0].BodyTemplate = `{"messages":"{{messages}}","auth":false,"authorization":null,"credentials":{"enabled":true,"token":"{{api_key}}"},"nested":{"token":["TEST-LITERAL-ARRAY-SECRET","{{api_key}}"],"password":12345}}`
	raw, err := encodeConfiguration(config, false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("TEST-LITERAL-ARRAY-SECRET")) || bytes.Contains(raw, []byte("12345")) {
		t.Fatal("nested array or numeric credentials escaped redaction")
	}
	result, _, err := decodeConfiguration(raw)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.Unmarshal([]byte(result.TranslationServices[0].BodyTemplate), &body)
	credentials := body["credentials"].(map[string]any)
	if body["auth"] != false || body["authorization"] != nil || credentials["enabled"] != true || credentials["token"] != "{{api_key}}" {
		t.Fatal("nonsecret auth flags, null values or placeholders were modified")
	}
}
