package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gooesman/gooestranslate/internal/engine"
)

func TestConfigurationImportPreviewIsolatedAndInvalidated(t *testing.T) {
	current := engine.DefaultConfig()
	current.APIKey = "TEST-CURRENT-SECRET"
	current.TranslationServices[0].APIKey = current.APIKey
	imported := cloneConfig(current)
	imported.Theme = "light"
	imported.TranslationServices[0].BaseURL = "https://example.invalid/v1"
	raw, err := encodeConfiguration(imported, false)
	if err != nil {
		t.Fatal(err)
	}
	a := &appState{cfg: current}
	preview, err := a.stageConfigurationImport(raw, "backup.json")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Token == "" || preview.Summary == nil || preview.Summary.ContainsCredentials || a.cfg.Theme != current.Theme {
		t.Fatal("preview changed saved configuration or omitted confirmation metadata")
	}
	public, _ := json.Marshal(preview)
	if bytes.Contains(public, []byte("TEST-CURRENT")) || bytes.Contains(public, []byte("example.invalid")) || bytes.Contains(public, []byte("encrypted_key")) {
		t.Fatal("configuration contents leaked through import preview")
	}
	candidate, err := a.configurationImportCandidate(preview.Token)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.APIKey != "" || candidate.TranslationServices[0].APIKey != "" || candidate.TranslationServices[0].EncryptedKey != "" {
		t.Fatal("same-ID imported profile inherited current credentials")
	}
	candidate.TranslationServices[0].Name = "mutated copy"
	again, _ := a.configurationImportCandidate(preview.Token)
	if again.TranslationServices[0].Name == candidate.TranslationServices[0].Name {
		t.Fatal("candidate aliased staged profiles")
	}
	a.cancelConfigurationImport("incorrect-token")
	if _, err := a.configurationImportCandidate(preview.Token); err != nil {
		t.Fatal("unrelated cancellation destroyed preview")
	}
	a.cancelConfigurationImport(preview.Token)
	if _, err := a.configurationImportCandidate(preview.Token); err == nil {
		t.Fatal("cancelled preview remained usable")
	}
	next, err := a.stageConfigurationImport(raw, "backup.json")
	if err != nil {
		t.Fatal(err)
	}
	if next.Token == preview.Token {
		t.Fatal("import token was reused")
	}
	a.configurationRevision++
	if _, err := a.configurationImportCandidate(next.Token); err == nil {
		t.Fatal("stale preview accepted after settings changed")
	}
	if _, err := a.stageConfigurationImport([]byte(`{"format":"unknown"}`), "bad.json"); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	if a.pendingConfigurationImport != nil || a.cfg.Theme != current.Theme || a.cfg.APIKey != current.APIKey {
		t.Fatal("invalid import changed current configuration")
	}
}

func TestConfigurationImportedCredentialsAreEncryptedLocally(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	c := engine.DefaultConfig()
	c.APIKey = "TEST-PORTABLE-TRANSLATION"
	c.TranslationServices[0].APIKey = c.APIKey
	c.OCRServices[0].APIKey = "TEST-PORTABLE-OCR"
	c.TTSServices[0].APIKey = "TEST-PORTABLE-TTS"
	c.Vocabulary.APIKey = "TEST-PORTABLE-EUDIC"
	raw, err := encodeConfiguration(c, true)
	if err != nil {
		t.Fatal(err)
	}
	imported, _, err := decodeConfiguration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(imported); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(configFile())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(saved, []byte("TEST-PORTABLE")) || bytes.Contains(saved, []byte(`"api_key"`)) {
		t.Fatal("portable credentials were persisted as plaintext")
	}
	loaded, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TranslationServices[0].APIKey != c.TranslationServices[0].APIKey || loaded.OCRServices[0].APIKey != c.OCRServices[0].APIKey || loaded.TTSServices[0].APIKey != c.TTSServices[0].APIKey || loaded.Vocabulary.APIKey != c.Vocabulary.APIKey {
		t.Fatal("local DPAPI roundtrip lost imported credentials")
	}
}

func TestConfigurationPersistenceRollsBackHotkeysOnFailure(t *testing.T) {
	old := engine.DefaultConfig()
	next := cloneConfig(old)
	next.Hotkeys.Capture = "Ctrl+Alt+Q"
	for _, scenario := range []string{"register", "write", "success"} {
		t.Run(scenario, func(t *testing.T) {
			var calls []engine.Hotkeys
			saved := false
			replace := func(keys engine.Hotkeys) []string {
				calls = append(calls, keys)
				if len(calls) == 1 && scenario == "register" {
					return []string{"TEST registration conflict"}
				}
				return nil
			}
			save := func(c engine.Config) error {
				saved = true
				if scenario == "write" {
					return errors.New("TEST write denied")
				}
				if c.Hotkeys.Capture != next.Hotkeys.Capture {
					t.Fatal("wrong configuration persisted")
				}
				return nil
			}
			err := persistConfigurationChange(old, next, replace, save)
			if scenario == "success" {
				if err != nil || len(calls) != 1 || !saved {
					t.Fatal("successful persistence failed")
				}
			} else {
				if err == nil || len(calls) != 2 || calls[1] != effectiveHotkeys(old) {
					t.Fatal("failed apply did not restore old hotkeys")
				}
				if scenario == "register" && saved {
					t.Fatal("registration failure wrote configuration")
				}
			}
		})
	}
}

func TestConfigurationTransferRPCRestrictsResultWindowAndSelfTests(t *testing.T) {
	a := &appState{cfg: engine.DefaultConfig(), testMode: true}
	for _, method := range []string{"exportConfiguration", "chooseConfigurationImport", "applyConfigurationImport", "cancelConfigurationImport"} {
		_, err := a.handleRPC(&webWindow{kind: "result"}, rpcRequest{Method: method})
		if err == nil {
			t.Fatalf("%s permitted from result window", method)
		}
	}
	for _, method := range []string{"exportConfiguration", "chooseConfigurationImport"} {
		result, err := a.handleRPC(&webWindow{kind: "settings"}, rpcRequest{Method: method, Args: []json.RawMessage{json.RawMessage(`{"include_credentials":true}`)}})
		if err != nil {
			t.Fatal(err)
		}
		transfer := result.(configurationTransferResult)
		if !transfer.OK || !transfer.Cancelled || a.pendingConfigurationImport != nil {
			t.Fatal("self-test opened file operation")
		}
	}
	a.configurationBusy = true
	if _, err := a.saveSettings(json.RawMessage(`{"theme":"light"}`)); err == nil {
		t.Fatal("settings changed during native configuration dialog")
	}
	if a.cfg.Theme != "dark" {
		t.Fatal("blocked save changed configuration")
	}
	if !strings.Contains(bridgeScript, "'exportConfiguration'") || !strings.Contains(bridgeScript, "'applyConfigurationImport'") {
		t.Fatal("configuration RPC bindings missing")
	}
}

func TestConfigurationExportPreservesOldFileAndProtectsLiveSettings(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	directory := t.TempDir()
	file := filepath.Join(directory, "backup.json")
	if err := os.WriteFile(file, []byte("old backup"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigurationExport(file, []byte("new backup")); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "new backup" {
		t.Fatal("export did not replace backup")
	}
	obstacle := filepath.Join(directory, "directory.json")
	if err := os.Mkdir(obstacle, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(obstacle, "original")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigurationExport(obstacle, []byte("new")); err == nil {
		t.Fatal("writing over a directory succeeded")
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "keep" {
		t.Fatal("failed export damaged original destination")
	}
	matches, _ := filepath.Glob(filepath.Join(directory, ".gooestranslate-export-*.tmp"))
	if len(matches) != 0 {
		t.Fatal("export temporary file leaked")
	}
	if err := saveConfig(engine.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(configFile())
	if err := writeConfigurationExport(strings.ToUpper(configFile()), []byte("bad")); err == nil {
		t.Fatal("live settings overwrite allowed")
	}
	after, _ := os.ReadFile(configFile())
	if !bytes.Equal(original, after) {
		t.Fatal("live settings were changed by export")
	}
	if !internalConfigurationPath(serviceProbeRecordsFile()) {
		t.Fatal("live service test records not protected")
	}
}

func TestConfigurationImportFileReadIsBounded(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "backup.json")
	if err := os.WriteFile(file, []byte("test file"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := readConfigurationImport(file); err != nil || string(data) != "test file" {
		t.Fatal("configuration file could not be read")
	}
	if _, err := readConfigurationImport(dir); err == nil {
		t.Fatal("directory accepted as configuration")
	}
	if err := os.WriteFile(file, bytes.Repeat([]byte("x"), configurationFileSizeLimit+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfigurationImport(file); err == nil {
		t.Fatal("oversized configuration file accepted")
	}
}

func TestConfigurationBlankKeysStayAbsentAcrossPersistence(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	cfg := engine.DefaultConfig()
	raw, err := encodeConfiguration(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	imported, _, err := decodeConfiguration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(imported); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(configFile())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte("encrypted_key")) {
		t.Fatal("empty credentials gained encrypted blobs")
	}
	loaded, err := loadConfig()
	if err != nil || unreadableCredential(&loaded) {
		t.Fatal("no-key backup became unreadable after restart")
	}
	public := publicConfig(loaded)
	for _, field := range []string{"translation_services", "ocr_services", "tts_services"} {
		for _, profile := range public[field].([]any) {
			if profile.(map[string]any)["has_api_key"] != false {
				t.Fatal("no-key profile shown as configured")
			}
		}
	}
	if public["vocabulary"].(map[string]any)["has_api_key"] != false {
		t.Fatal("empty vocabulary authorization shown as configured")
	}
	if _, err := encodeConfiguration(loaded, true); err != nil {
		t.Fatal("blank credentials blocked later export")
	}
}
