package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gooesman/gooeslate/internal/engine"
)

func probeFixture() engine.Config {
	c := engine.DefaultConfig()
	c.TranslationServices = []engine.TranslationService{{ID: "one", Name: "First", BaseURL: "https://example.test/v1", Model: "test", APIKey: "private-sample-key", EncryptedKey: "old-blob"}, {ID: "two", Name: "Second", BaseURL: "https://second.test/v1", Model: "other", APIKey: "second-sample-key"}}
	c.ActiveTranslationID = "one"
	c.Vocabulary.APIKey = "vocabulary-sample-key"
	return engine.NormalizeConfig(c)
}

func TestProbeDraftRetainsOnlyMatchingCredentialAndNeverMutatesConfig(t *testing.T) {
	c := probeFixture()
	before := cloneConfig(c)
	for _, tc := range []struct {
		id, key string
		clear   bool
		want    string
	}{
		{"one", "", false, "private-sample-key"},
		{"one", "", true, ""},
		{"one", " replacement ", true, "replacement"},
		{"new", "", false, ""},
	} {
		raw, _ := json.Marshal(map[string]any{"kind": "translation", "service": map[string]any{"id": tc.id, "name": "Draft", "base_url": "https://draft.test/v1", "model": "draft", "api_key": tc.key, "clear_api_key": tc.clear}})
		cfg, kind, err := prepareServiceProbe(c, raw)
		p := engine.ActiveTranslation(cfg)
		if err != nil || kind != "translation" || p.ID != tc.id || p.APIKey != tc.want || p.BaseURL != "https://draft.test/v1" {
			t.Fatalf("draft credential merge failed for %s", tc.id)
		}
	}
	if !reflect.DeepEqual(c, before) {
		t.Fatal("service test mutated current settings")
	}
}

func TestProbeDictionaryUsesUnSavedDependencies(t *testing.T) {
	c := probeFixture()
	raw := json.RawMessage(`{"kind":"dictionary","service":{"provider":"openai","enabled":true},"translation_service":{"id":"two","base_url":"https://draft.test/v1","model":"draft"},"language":"英语"}`)
	cfg, kind, err := prepareServiceProbe(c, raw)
	if err != nil || kind != "dictionary" || cfg.Language != "英语" || engine.ActiveTranslation(cfg).ID != "two" || engine.ActiveTranslation(cfg).APIKey != "second-sample-key" {
		t.Fatal("dictionary did not use selected draft and saved credential")
	}
	raw = json.RawMessage(`{"kind":"dictionary","service":{"provider":"eudic"},"vocabulary":{"category_id":"987654321012345678","language":"fr","api_key":"draft-vocabulary"}}`)
	cfg, _, err = prepareServiceProbe(c, raw)
	if err != nil || cfg.Vocabulary.CategoryID != "987654321012345678" || cfg.Vocabulary.APIKey != "draft-vocabulary" {
		t.Fatal("dictionary vocabulary draft or long ID lost")
	}
}

func TestProbeRejectsInvalidRequests(t *testing.T) {
	for _, raw := range []string{`null`, `{"kind":"oops","service":{}}`, `{"kind":"translation","service":{}}`, `{"kind":"translation","service":[]}`, `{"kind":"tts","service":{"id":"s","provider":"system","rate":99}}`} {
		if _, _, err := prepareServiceProbe(probeFixture(), json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid probe accepted: %s", raw)
		}
	}
}

func TestProbeRecordsPersistWithoutSecretsAndInvalidateChangedInputs(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	c := probeFixture()
	result := engine.ProbeResult{Kind: "translation", ServiceID: "one", OK: true, Message: "测试通过", TestedAt: "2026-10-10T02:00:00Z"}
	key := serviceProbeKey(c, "translation")
	records := map[string]serviceProbeRecord{key: {Fingerprint: serviceProbeFingerprint(c, "translation"), Result: result}}
	if err := saveServiceProbeResults(records); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(serviceProbeRecordsFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-sample-key", "vocabulary-sample-key", "old-blob", "example.test"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("probe records exposed private configuration")
		}
	}
	a := appState{cfg: c, serviceProbeResults: loadServiceProbeResults()}
	if len(a.publicServiceProbeResults()) != 1 {
		t.Fatal("unchanged saved test not restored")
	}
	a.cfg.TranslationServices[0].Name = "Renamed"
	a.cfg.TranslationServices[0].EncryptedKey = "new-blob"
	if len(a.publicServiceProbeResults()) != 1 {
		t.Fatal("rename or re-encryption invalidated a connection test")
	}
	a.cfg.TranslationServices[0].APIKey = "changed-key"
	if len(a.publicServiceProbeResults()) != 0 {
		t.Fatal("changed key retained stale success")
	}
	a.cfg = c
	a.cfg.TranslationServices[0].Model = "changed-model"
	if len(a.publicServiceProbeResults()) != 0 {
		t.Fatal("changed model retained stale success")
	}
}

func TestProbeRecordsIgnoreUnrelatedProfilesAndRejectRemovedService(t *testing.T) {
	c := probeFixture()
	key := serviceProbeKey(c, "translation")
	a := appState{cfg: cloneConfig(c), serviceProbeResults: map[string]serviceProbeRecord{key: {Fingerprint: serviceProbeFingerprint(c, "translation"), Result: engine.ProbeResult{OK: true}}}}
	a.cfg.TranslationServices[1].Model = "other-edit"
	if len(a.publicServiceProbeResults()) != 1 {
		t.Fatal("unrelated profile invalidated saved test")
	}
	a.cfg.TranslationServices = a.cfg.TranslationServices[1:]
	if len(a.publicServiceProbeResults()) != 0 {
		t.Fatal("deleted profile retained saved test")
	}
}
