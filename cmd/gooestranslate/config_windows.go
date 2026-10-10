package main

import (
	"encoding/json"
	"fmt"
	"github.com/gooesman/gooestranslate/internal/engine"
	"os"
	"path/filepath"
	"unsafe"
)

type blob struct {
	Size uint32
	Data *byte
}

func protect(input []byte, decrypt bool) ([]byte, error) {
	if len(input) == 0 {
		return nil, nil
	}
	in := blob{uint32(len(input)), &input[0]}
	var out blob
	proc := "CryptProtectData"
	if decrypt {
		proc = "CryptUnprotectData"
	}
	if call(crypt, proc, uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&out))) == 0 {
		return nil, fmt.Errorf("Windows 无法%s API 密钥", map[bool]string{true: "解密", false: "加密"}[decrypt])
	}
	defer call(kernel, "LocalFree", uintptr(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
func configFile() string {
	p, err := os.UserConfigDir()
	if err != nil {
		p = os.TempDir()
	}
	return filepath.Join(p, "gooestranslate", "settings.json")
}
func loadConfig() (engine.Config, error) {
	c := engine.DefaultConfig()
	b, err := os.ReadFile(configFile())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("设置文件无法读取，请重新填写")
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(b, &fields)
	if _, ok := fields["translation_services"]; !ok {
		c.TranslationServices = nil
		c.ActiveTranslationID = ""
	}
	if _, ok := fields["ocr_services"]; !ok {
		c.OCRServices = nil
		c.ActiveOCRID = ""
	}
	if _, ok := fields["tts_services"]; !ok {
		c.TTSServices = nil
		c.ActiveTTSID = ""
	}
	err = decryptConfigCredentials(&c)
	c = engine.NormalizeConfig(c)
	return c, err
}
func saveConfig(c engine.Config) error {
	c = cloneConfig(c)
	if err := encryptConfigCredentials(&c); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	file := configFile()
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), "settings-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, file)
}
