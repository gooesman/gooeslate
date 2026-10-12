package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gooesman/gooeslate/internal/engine"
)

type serviceProbeRecord struct {
	Fingerprint string             `json:"fingerprint"`
	Result      engine.ProbeResult `json:"result"`
}

func serviceProbeKey(cfg engine.Config, kind string) string {
	switch kind {
	case "translation":
		return kind + ":" + engine.ActiveTranslation(cfg).ID
	case "ocr":
		return kind + ":" + engine.ActiveOCR(cfg).ID
	case "tts":
		return kind + ":" + engine.ActiveTTS(cfg).ID
	default:
		return kind
	}
}

// Only a digest is stored; plaintext keys and configured request bodies never
// leave memory. Re-encryption and display-name changes do not invalidate tests.
func serviceProbeFingerprint(cfg engine.Config, kind string) string {
	cfg = engine.NormalizeConfig(cfg)
	translation := func() any {
		p := engine.ActiveTranslation(cfg)
		p.Name = ""
		p.EncryptedKey = ""
		return []any{p, p.APIKey, cfg.Language}
	}
	vocabulary := func() any { p := cfg.Vocabulary; p.Enabled = false; p.EncryptedKey = ""; return []any{p, p.APIKey} }
	var value any
	switch kind {
	case "translation":
		value = translation()
	case "ocr":
		p := engine.ActiveOCR(cfg)
		p.Name = ""
		p.EncryptedKey = ""
		value = []any{p, p.APIKey, cfg.Language}
	case "tts":
		p := engine.ActiveTTS(cfg)
		p.Name = ""
		p.EncryptedKey = ""
		p.AutoRead = false
		value = []any{p, p.APIKey}
	case "vocabulary":
		value = vocabulary()
	case "dictionary":
		p := cfg.Dictionary
		p.Enabled = false
		value = []any{p}
		if p.Provider == "openai" {
			value = []any{p, translation()}
		} else if p.Provider == "eudic" {
			value = []any{p, vocabulary()}
		} else {
			if info, err := os.Stat(p.LocalPath); err == nil {
				value = []any{p, info.Size(), info.ModTime().UnixNano()}
			}
		}
	}
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func serviceProbeRecordsFile() string {
	return filepath.Join(filepath.Dir(configFile()), "service-tests.json")
}

func loadServiceProbeResults() map[string]serviceProbeRecord {
	result := map[string]serviceProbeRecord{}
	info, err := os.Stat(serviceProbeRecordsFile())
	if err != nil || info.Size() > 256<<10 {
		return result
	}
	raw, err := os.ReadFile(serviceProbeRecordsFile())
	if err != nil || json.Unmarshal(raw, &result) != nil {
		return map[string]serviceProbeRecord{}
	}
	return result
}

func saveServiceProbeResults(records map[string]serviceProbeRecord) error {
	if len(records) > 400 {
		keys := make([]string, 0, len(records))
		for key := range records {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return records[keys[i]].Result.TestedAt < records[keys[j]].Result.TestedAt })
		for _, key := range keys[:len(keys)-400] {
			delete(records, key)
		}
	}
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	file := serviceProbeRecordsFile()
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), "service-tests-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, file)
}

func (a *appState) publicServiceProbeResults() map[string]engine.ProbeResult {
	result := map[string]engine.ProbeResult{}
	for key, record := range a.serviceProbeResults {
		cfg := cloneConfig(a.cfg)
		kind, id, _ := strings.Cut(key, ":")
		found := true
		switch kind {
		case "translation":
			found = false
			for _, p := range cfg.TranslationServices {
				if p.ID == id {
					cfg.ActiveTranslationID = id
					found = true
					break
				}
			}
		case "ocr":
			found = false
			for _, p := range cfg.OCRServices {
				if p.ID == id {
					cfg.ActiveOCRID = id
					found = true
					break
				}
			}
		case "tts":
			found = false
			for _, p := range cfg.TTSServices {
				if p.ID == id {
					cfg.ActiveTTSID = id
					found = true
					break
				}
			}
		case "vocabulary", "dictionary":
		default:
			found = false
		}
		if found && record.Fingerprint == serviceProbeFingerprint(cfg, kind) {
			result[key] = record.Result
		}
	}
	return result
}
