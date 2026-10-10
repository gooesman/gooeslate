package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gooesman/gooestranslate/internal/engine"
)

func cloneConfig(c engine.Config) engine.Config {
	c.TranslationServices = append([]engine.TranslationService(nil), c.TranslationServices...)
	c.OCRServices = append([]engine.OCRService(nil), c.OCRServices...)
	c.TTSServices = append([]engine.TTSService(nil), c.TTSServices...)
	return c
}

type credentialRef struct{ key, encrypted *string }

func configCredentials(c *engine.Config) []credentialRef {
	refs := []credentialRef{{&c.APIKey, &c.EncryptedKey}, {&c.Vocabulary.APIKey, &c.Vocabulary.EncryptedKey}}
	for i := range c.TranslationServices {
		p := &c.TranslationServices[i]
		refs = append(refs, credentialRef{&p.APIKey, &p.EncryptedKey})
	}
	for i := range c.OCRServices {
		p := &c.OCRServices[i]
		refs = append(refs, credentialRef{&p.APIKey, &p.EncryptedKey})
	}
	for i := range c.TTSServices {
		p := &c.TTSServices[i]
		refs = append(refs, credentialRef{&p.APIKey, &p.EncryptedKey})
	}
	return refs
}

func decryptConfigCredentials(c *engine.Config) error {
	var firstError error
	for _, ref := range configCredentials(c) {
		if *ref.encrypted == "" {
			continue
		}
		encrypted, err := base64.StdEncoding.DecodeString(*ref.encrypted)
		if err == nil {
			var raw []byte
			raw, err = protect(encrypted, true)
			if err == nil {
				*ref.key = string(raw)
			}
		}
		if err != nil && firstError == nil {
			firstError = fmt.Errorf("有服务密钥无法解密，请重新填写或清除该密钥")
		}
	}
	return firstError
}

func encryptConfigCredentials(c *engine.Config) error {
	for _, ref := range configCredentials(c) {
		// Blank keys retain their original recoverable encrypted blob.
		if *ref.key == "" && *ref.encrypted != "" {
			continue
		}
		encrypted, err := protect([]byte(*ref.key), false)
		if err != nil {
			return err
		}
		*ref.encrypted = base64.StdEncoding.EncodeToString(encrypted)
	}
	return nil
}

func unreadableCredential(c *engine.Config) bool {
	for _, ref := range configCredentials(c) {
		if *ref.key == "" && *ref.encrypted != "" {
			return true
		}
	}
	return false
}

type credentialPatch struct {
	ID     string `json:"id"`
	APIKey string `json:"api_key"`
	Clear  bool   `json:"clear_api_key"`
}

func applyCredential(ref credentialRef, oldKey, oldEncrypted string, p credentialPatch) bool {
	*ref.key, *ref.encrypted = oldKey, oldEncrypted
	changed := false
	if p.Clear {
		*ref.key, *ref.encrypted = "", ""
		changed = true
	}
	if key := strings.TrimSpace(p.APIKey); key != "" {
		*ref.key, *ref.encrypted = key, ""
		changed = true
	}
	return changed
}

func mergeProfileCredentials(current engine.Config, next *engine.Config, raw json.RawMessage) (bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false, err
	}
	changed := false
	patches := func(name string) (map[string]credentialPatch, error) {
		out := map[string]credentialPatch{}
		if value, ok := fields[name]; ok {
			var items []credentialPatch
			if err := json.Unmarshal(value, &items); err != nil {
				return nil, err
			}
			for _, p := range items {
				out[p.ID] = p
			}
		}
		return out, nil
	}
	translations, err := patches("translation_services")
	if err != nil {
		return false, err
	}
	for i := range next.TranslationServices {
		p := &next.TranslationServices[i]
		oldKey, oldEncrypted := "", ""
		for _, old := range current.TranslationServices {
			if old.ID == p.ID {
				oldKey, oldEncrypted = old.APIKey, old.EncryptedKey
				break
			}
		}
		changed = applyCredential(credentialRef{&p.APIKey, &p.EncryptedKey}, oldKey, oldEncrypted, translations[p.ID]) || changed
	}
	ocrs, err := patches("ocr_services")
	if err != nil {
		return false, err
	}
	for i := range next.OCRServices {
		p := &next.OCRServices[i]
		oldKey, oldEncrypted := "", ""
		for _, old := range current.OCRServices {
			if old.ID == p.ID {
				oldKey, oldEncrypted = old.APIKey, old.EncryptedKey
				break
			}
		}
		changed = applyCredential(credentialRef{&p.APIKey, &p.EncryptedKey}, oldKey, oldEncrypted, ocrs[p.ID]) || changed
	}
	tts, err := patches("tts_services")
	if err != nil {
		return false, err
	}
	for i := range next.TTSServices {
		p := &next.TTSServices[i]
		oldKey, oldEncrypted := "", ""
		for _, old := range current.TTSServices {
			if old.ID == p.ID {
				oldKey, oldEncrypted = old.APIKey, old.EncryptedKey
				break
			}
		}
		changed = applyCredential(credentialRef{&p.APIKey, &p.EncryptedKey}, oldKey, oldEncrypted, tts[p.ID]) || changed
	}
	var vocabulary credentialPatch
	if value, ok := fields["vocabulary"]; ok {
		if err := json.Unmarshal(value, &vocabulary); err != nil {
			return false, err
		}
	}
	changed = applyCredential(credentialRef{&next.Vocabulary.APIKey, &next.Vocabulary.EncryptedKey}, current.Vocabulary.APIKey, current.Vocabulary.EncryptedKey, vocabulary) || changed
	remaining := map[string]bool{}
	for _, p := range next.TranslationServices {
		remaining["translation:"+p.ID] = true
	}
	for _, p := range next.OCRServices {
		remaining["ocr:"+p.ID] = true
	}
	for _, p := range next.TTSServices {
		remaining["tts:"+p.ID] = true
	}
	for _, p := range current.TranslationServices {
		if !remaining["translation:"+p.ID] && (p.APIKey != "" || p.EncryptedKey != "") {
			changed = true
		}
	}
	for _, p := range current.OCRServices {
		if !remaining["ocr:"+p.ID] && (p.APIKey != "" || p.EncryptedKey != "") {
			changed = true
		}
	}
	for _, p := range current.TTSServices {
		if !remaining["tts:"+p.ID] && (p.APIKey != "" || p.EncryptedKey != "") {
			changed = true
		}
	}
	return changed, nil
}

func publicConfig(c engine.Config) map[string]any {
	clean := cloneConfig(c)
	for _, ref := range configCredentials(&clean) {
		*ref.key, *ref.encrypted = "", ""
	}
	raw, _ := json.Marshal(clean)
	var result map[string]any
	json.Unmarshal(raw, &result)
	setKeys := func(field string, keys []bool) {
		if profiles, ok := result[field].([]any); ok {
			for i, p := range profiles {
				if entry, ok := p.(map[string]any); ok && i < len(keys) {
					entry["has_api_key"] = keys[i]
				}
			}
		}
	}
	keys := []bool{}
	for _, p := range c.TranslationServices {
		keys = append(keys, p.APIKey != "" || p.EncryptedKey != "")
	}
	setKeys("translation_services", keys)
	keys = nil
	for _, p := range c.OCRServices {
		keys = append(keys, p.APIKey != "" || p.EncryptedKey != "")
	}
	setKeys("ocr_services", keys)
	keys = nil
	for _, p := range c.TTSServices {
		keys = append(keys, p.APIKey != "" || p.EncryptedKey != "")
	}
	setKeys("tts_services", keys)
	if vocabulary, ok := result["vocabulary"].(map[string]any); ok {
		vocabulary["has_api_key"] = c.Vocabulary.APIKey != "" || c.Vocabulary.EncryptedKey != ""
	}
	return result
}
