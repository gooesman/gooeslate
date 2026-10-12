package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gooesman/gooeslate/internal/engine"
)

const configurationFileSizeLimit = 2 << 20
const configurationTransferFormat = "gooestranslate-config"
const configurationTransferSchema = 1

type configurationImportSummary struct {
	TranslationCount    int  `json:"translation_count"`
	OCRCount            int  `json:"ocr_count"`
	TTSCount            int  `json:"tts_count"`
	ContainsCredentials bool `json:"contains_credentials"`
	CredentialsCount    int  `json:"credentials_count"`
	SchemaVersion       int  `json:"schema_version"`
	OfflinePathCount    int  `json:"offline_path_count"`
}

type configurationTransferDocument struct {
	Format              string         `json:"format"`
	SchemaVersion       int            `json:"schema_version"`
	ExportedAt          string         `json:"exported_at"`
	IncludesCredentials bool           `json:"includes_credentials"`
	Config              map[string]any `json:"config"`
}

// Portable files deliberately contain no Windows-bound protected blobs. Keys
// are included as plaintext only when the caller explicitly requests it.
func encodeConfiguration(c engine.Config, includeCredentials bool) ([]byte, error) {
	c = engine.NormalizeConfig(cloneConfig(c))
	if validateSettings(c) != nil {
		return nil, errors.New("当前配置包含无效的设置，请修正后再导出")
	}
	if includeCredentials && unreadableCredential(&c) {
		return nil, errors.New("有服务密钥无法解密，请重新填写或选择不包含密钥")
	}
	secrets := configurationSecrets(c)
	for _, ref := range configCredentials(&c) {
		*ref.encrypted = ""
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, errors.New("配置无法编码")
	}
	value, err := configurationJSONValue(raw)
	if err != nil {
		return nil, errors.New("配置无法编码")
	}
	config := value.(map[string]any)
	// Login startup is a local Windows preference, never part of a portable file.
	delete(config, "start_at_login")
	if includeCredentials {
		putConfigurationCredentials(config, c)
	} else {
		config, err = redactConfigurationMap(config, secrets)
		if err != nil {
			return nil, err
		}
	}
	// Reject ambiguous custom JSON even when credentials are included.
	if _, err = configurationCredentialCount(config); err != nil {
		return nil, err
	}
	doc := configurationTransferDocument{configurationTransferFormat, configurationTransferSchema,
		time.Now().UTC().Format(time.RFC3339Nano), includeCredentials, config}
	result, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, errors.New("配置无法编码")
	}
	if len(result)+1 > configurationFileSizeLimit {
		return nil, errors.New("配置文件不能超过 2 MB")
	}
	// The same decoder validates our redacted output. Nothing is written if a
	// rare redaction would invalidate an identifier or required local path.
	if _, _, err = decodeConfiguration(result); err != nil {
		return nil, errors.New("导出的配置无法通过校验，请检查自定义请求和本地词典设置")
	}
	return append(result, '\n'), nil
}

func decodeConfiguration(raw []byte) (engine.Config, configurationImportSummary, error) {
	fail := func(message string) (engine.Config, configurationImportSummary, error) {
		return engine.Config{}, configurationImportSummary{}, errors.New(message)
	}
	if len(raw) > configurationFileSizeLimit {
		return fail("配置文件不能超过 2 MB")
	}
	raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(raw) {
		return fail("配置文件需要使用 UTF-8 编码")
	}
	value, err := configurationJSONValue(raw)
	if err != nil {
		return fail("配置文件不是有效的 JSON，或包含重复字段")
	}
	doc, ok := value.(map[string]any)
	if !ok {
		return fail("请选择鹅译导出的配置文件")
	}
	allowed := map[string]bool{"format": true, "schema_version": true, "exported_at": true, "includes_credentials": true, "config": true}
	if len(doc) != len(allowed) {
		return fail("配置文件格式不完整或包含未知字段")
	}
	for key := range doc {
		if !allowed[key] {
			return fail("配置文件包含未知字段")
		}
	}
	if doc["format"] != configurationTransferFormat {
		return fail("请选择鹅译导出的配置文件")
	}
	version, ok := doc["schema_version"].(json.Number)
	if !ok || version.String() != "1" {
		return fail("不支持此配置文件版本，请更新软件后重试")
	}
	exportedAt, ok := doc["exported_at"].(string)
	if !ok {
		return fail("配置文件导出时间无效")
	}
	if _, err = time.Parse(time.RFC3339Nano, exportedAt); err != nil {
		return fail("配置文件导出时间无效")
	}
	includes, ok := doc["includes_credentials"].(bool)
	if !ok {
		return fail("配置文件的密钥标记无效")
	}
	config, ok := doc["config"].(map[string]any)
	if !ok {
		return fail("配置文件缺少设置内容")
	}
	if err = validateConfigurationShape(config, reflect.TypeOf(engine.Config{})); err != nil {
		return fail("配置字段或类型无效；请使用软件导出的配置文件")
	}
	if err = validateConfigurationProfileIDs(config); err != nil {
		return fail(err.Error())
	}
	credentialCount, err := configurationCredentialCount(config)
	if err != nil {
		return fail(err.Error())
	}
	if !includes && credentialCount != 0 {
		return fail("配置标记为不含密钥，但发现了授权信息")
	}
	keys := takeConfigurationCredentials(config)
	encoded, err := json.Marshal(config)
	if err != nil {
		return fail("配置文件无法读取")
	}
	c := engine.DefaultConfig()
	if err = json.Unmarshal(encoded, &c); err != nil {
		return fail("配置字段或类型无效")
	}
	restoreConfigurationCredentials(&c, keys)
	if validateSettings(c) != nil {
		return fail("配置中的服务参数、快捷键或模板无效，请检查后重新导出")
	}
	// Modern profile lists are authoritative. Clear legacy migration inputs
	// before NormalizeConfig so explicitly empty fields cannot be resurrected.
	for _, profile := range c.TranslationServices {
		if profile.ID == c.ActiveTranslationID {
			c.BaseURL, c.Model, c.APIKey, c.EncryptedKey, c.DisableThinking = profile.BaseURL, profile.Model, profile.APIKey, "", profile.DisableThinking
		}
	}
	for _, profile := range c.OCRServices {
		if profile.ID == c.ActiveOCRID {
			c.OCRURL = profile.URL
		}
	}
	for _, profile := range c.TTSServices {
		if profile.ID == c.ActiveTTSID {
			c.TTS = engine.TTSConfig{Provider: profile.Provider, Voice: profile.Voice, Rate: profile.Rate, AutoRead: profile.AutoRead}
		}
	}
	c = engine.NormalizeConfig(c)
	// Explicit service lists were validated before normalization. Resolve legacy
	// aliases from the imported active profile, never from the current machine.
	active := engine.ActiveTranslation(c)
	c.BaseURL, c.Model, c.APIKey, c.EncryptedKey, c.DisableThinking = active.BaseURL, active.Model, active.APIKey, "", active.DisableThinking
	c.OCRURL = engine.ActiveOCR(c).URL
	tts := engine.ActiveTTS(c)
	c.TTS = engine.TTSConfig{Provider: tts.Provider, Voice: tts.Voice, Rate: tts.Rate, AutoRead: tts.AutoRead}
	if validateSettings(c) != nil {
		return fail("配置中的服务参数、快捷键或模板无效，请检查后重新导出")
	}
	summary := configurationImportSummary{TranslationCount: len(c.TranslationServices), OCRCount: len(c.OCRServices), TTSCount: len(c.TTSServices), ContainsCredentials: credentialCount > 0, CredentialsCount: credentialCount, SchemaVersion: configurationTransferSchema}
	if strings.TrimSpace(c.Dictionary.LocalPath) != "" {
		summary.OfflinePathCount = 1
	}
	return c, summary, nil
}

// Decode recursively rather than relying on Unmarshal's last-value-wins rule.
// Errors deliberately omit input fragments: those may include API credentials.
func configurationJSONValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var read func(int) (any, error)
	invalid := errors.New("invalid configuration JSON")
	read = func(depth int) (any, error) {
		if depth > 64 {
			return nil, invalid
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, invalid
		}
		delimiter, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return token, nil
		}
		switch delimiter {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				token, err := decoder.Token()
				if err != nil {
					return nil, invalid
				}
				key, ok := token.(string)
				if !ok {
					return nil, invalid
				}
				if _, found := object[key]; found {
					return nil, invalid
				}
				value, err := read(depth + 1)
				if err != nil {
					return nil, invalid
				}
				object[key] = value
			}
			token, err := decoder.Token()
			if err != nil || token != json.Delim('}') {
				return nil, invalid
			}
			return object, nil
		case '[':
			array := []any{}
			for decoder.More() {
				value, err := read(depth + 1)
				if err != nil {
					return nil, invalid
				}
				array = append(array, value)
			}
			token, err := decoder.Token()
			if err != nil || token != json.Delim(']') {
				return nil, invalid
			}
			return array, nil
		default:
			return nil, invalid
		}
	}
	value, err := read(0)
	if err != nil {
		return nil, invalid
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, invalid
	}
	return value, nil
}

func validateConfigurationShape(value any, kind reflect.Type) error {
	invalid := errors.New("invalid configuration shape")
	if value == nil {
		return invalid
	}
	switch kind.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return invalid
		}
		fields := map[string]reflect.Type{}
		hasCredentials := false
		for i := 0; i < kind.NumField(); i++ {
			field := kind.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.Name == "APIKey" {
				hasCredentials = true
			}
			if tag != "" && tag != "-" {
				fields[tag] = field.Type
			}
		}
		for key, item := range object {
			if key == "encrypted_key" {
				return invalid
			}
			if key == "api_key" && hasCredentials {
				if _, ok := item.(string); !ok {
					return invalid
				}
				continue
			}
			next, ok := fields[key]
			if !ok || validateConfigurationShape(item, next) != nil {
				return invalid
			}
		}
	case reflect.Slice:
		items, ok := value.([]any)
		if !ok {
			return invalid
		}
		for _, item := range items {
			if validateConfigurationShape(item, kind.Elem()) != nil {
				return invalid
			}
		}
	case reflect.String:
		if _, ok := value.(string); !ok {
			return invalid
		}
	case reflect.Bool:
		if _, ok := value.(bool); !ok {
			return invalid
		}
	case reflect.Int, reflect.Float64:
		if _, ok := value.(json.Number); !ok {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}

func validateConfigurationProfileIDs(config map[string]any) error {
	for _, category := range []struct{ list, active string }{{"translation_services", "active_translation"}, {"ocr_services", "active_ocr"}, {"tts_services", "active_tts"}} {
		items, ok := config[category.list].([]any)
		if !ok || len(items) == 0 || len(items) > 100 {
			return errors.New("每类服务必须包含 1 到 100 项")
		}
		ids := map[string]bool{}
		for _, item := range items {
			profile := item.(map[string]any)
			id, ok := profile["id"].(string)
			if !ok || strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) || ids[id] {
				return errors.New("服务 ID 不能为空或重复")
			}
			ids[id] = true
			// Missing modern optional fields receive defaults, but provider and ID are
			// identity-bearing and must never be silently repaired by NormalizeConfig.
			provider, ok := profile["provider"].(string)
			if !ok || strings.TrimSpace(provider) == "" {
				return errors.New("配置缺少有效的服务类型")
			}
			if value, present := profile["timeout_ms"]; present {
				integer, err := strconv.ParseInt(value.(json.Number).String(), 10, 64)
				if err != nil || integer < 1000 || integer > 600000 {
					return errors.New("配置中的请求超时范围无效")
				}
			}
			for _, field := range []string{"request_mode", "image_encoding", "box_format"} {
				if value, present := profile[field]; present && value == "" {
					return errors.New("配置中的请求模式或图片格式无效")
				}
			}
			if value, present := profile["speed"]; present {
				number, err := value.(json.Number).Float64()
				if err != nil || number < .25 || number > 4 {
					return errors.New("配置中的朗读速度范围无效")
				}
			}
		}
		active, ok := config[category.active].(string)
		if !ok || !ids[active] {
			return errors.New("当前使用的服务不存在于配置文件中")
		}
	}
	if vocabulary, ok := config["vocabulary"].(map[string]any); ok {
		if provider, present := vocabulary["provider"]; present && provider != "eudic" {
			return errors.New("配置中的生词本服务类型无效")
		}
		if value, present := vocabulary["timeout_ms"]; present {
			integer, err := strconv.ParseInt(value.(json.Number).String(), 10, 64)
			if err != nil || integer < 1000 || integer > 600000 {
				return errors.New("配置中的生词本超时范围无效")
			}
		}
	}
	if tts, ok := config["tts"].(map[string]any); ok {
		if provider, present := tts["provider"]; present && provider != "system" && provider != "openai" && provider != "http" {
			return errors.New("配置中的朗读服务类型无效")
		}
	}
	return nil
}

func putConfigurationCredentials(config map[string]any, c engine.Config) {
	if c.APIKey != "" {
		config["api_key"] = c.APIKey
	}
	if c.Vocabulary.APIKey != "" {
		config["vocabulary"].(map[string]any)["api_key"] = c.Vocabulary.APIKey
	}
	put := func(field string, keys []string) {
		for i, item := range config[field].([]any) {
			if keys[i] != "" {
				item.(map[string]any)["api_key"] = keys[i]
			}
		}
	}
	keys := []string{}
	for _, profile := range c.TranslationServices {
		keys = append(keys, profile.APIKey)
	}
	put("translation_services", keys)
	keys = nil
	for _, profile := range c.OCRServices {
		keys = append(keys, profile.APIKey)
	}
	put("ocr_services", keys)
	keys = nil
	for _, profile := range c.TTSServices {
		keys = append(keys, profile.APIKey)
	}
	put("tts_services", keys)
}

// Extract into a detached map before the Config decode (APIKey is json:"-").
func takeConfigurationCredentials(config map[string]any) map[string]string {
	keys := map[string]string{}
	take := func(object map[string]any, id string) {
		if value, ok := object["api_key"].(string); ok {
			keys[id] = value
		}
		delete(object, "api_key")
	}
	take(config, "legacy")
	if vocabulary, ok := config["vocabulary"].(map[string]any); ok {
		take(vocabulary, "vocabulary")
	}
	for _, field := range []string{"translation_services", "ocr_services", "tts_services"} {
		for _, item := range config[field].([]any) {
			profile := item.(map[string]any)
			take(profile, field+":"+profile["id"].(string))
		}
	}
	return keys
}
func restoreConfigurationCredentials(c *engine.Config, keys map[string]string) {
	c.APIKey = keys["legacy"]
	c.Vocabulary.APIKey = keys["vocabulary"]
	for i := range c.TranslationServices {
		p := &c.TranslationServices[i]
		p.APIKey = keys["translation_services:"+p.ID]
	}
	for i := range c.OCRServices {
		p := &c.OCRServices[i]
		p.APIKey = keys["ocr_services:"+p.ID]
	}
	for i := range c.TTSServices {
		p := &c.TTSServices[i]
		p.APIKey = keys["tts_services:"+p.ID]
	}
}

func configurationSecrets(c engine.Config) []string {
	set := map[string]bool{}
	for _, ref := range configCredentials(&c) {
		if *ref.key != "" {
			set[*ref.key] = true
			set[url.QueryEscape(*ref.key)] = true
			set[url.PathEscape(*ref.key)] = true
		}
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	return keys
}

var configurationPlaceholder = regexp.MustCompile(`\{\{\s*[A-Za-z_][A-Za-z0-9_]*\s*\}\}`)

func replaceConfigurationSecrets(text string, secrets []string) string {
	// Never rewrite a template variable name even if it happens to equal a key.
	locations := configurationPlaceholder.FindAllStringIndex(text, -1)
	var out strings.Builder
	offset := 0
	replace := func(part string) string {
		for _, secret := range secrets {
			part = strings.ReplaceAll(part, secret, "")
		}
		return part
	}
	for _, span := range locations {
		out.WriteString(replace(text[offset:span[0]]))
		out.WriteString(text[span[0]:span[1]])
		offset = span[1]
	}
	out.WriteString(replace(text[offset:]))
	return out.String()
}

func configurationSensitiveName(name string) bool {
	name = strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		if r == '-' || r == '_' || r == '.' || r == ' ' {
			return -1
		}
		return r
	}, name)
	switch name {
	case "authorization", "proxyauthorization", "apikey", "xapikey", "xapitoken", "key", "token", "accesstoken", "refreshtoken", "idtoken", "clientsecret", "secret", "password", "passwd", "auth", "credentials", "authentication", "bearer", "cookie", "setcookie", "subscriptionkey", "ocpapimsubscriptionkey", "xgoogapikey", "xauthtoken", "authtoken", "apitoken", "secretkey":
		return true
	}
	return false
}

func configurationLiteralCredential(value any) bool {
	switch item := value.(type) {
	case string:
		remaining := strings.TrimSpace(configurationPlaceholder.ReplaceAllString(item, ""))
		if remaining == "" {
			return false
		}
		if configurationPlaceholder.MatchString(item) {
			switch strings.ToLower(remaining) {
			case "bearer", "basic", "token", "nis", "apikey", "api-key":
				return false
			}
		}
		return true
	case map[string]any:
		for _, value := range item {
			if configurationLiteralCredential(value) {
				return true
			}
		}
		return false
	case []any:
		for _, value := range item {
			if configurationLiteralCredential(value) {
				return true
			}
		}
		return false
	case nil, bool:
		return false
	default:
		return true
	}
}
func blankConfigurationCredential(value any) any {
	switch item := value.(type) {
	case string:
		if !configurationLiteralCredential(item) {
			return item
		}
		// Keep variables usable even if a literal secret was attached to one.
		return strings.Join(configurationPlaceholder.FindAllString(item, -1), "")
	case map[string]any:
		out := map[string]any{}
		for key, value := range item {
			out[key] = blankConfigurationCredential(value)
		}
		return out
	case []any:
		out := make([]any, len(item))
		for i, value := range item {
			out[i] = blankConfigurationCredential(value)
		}
		return out
	case nil, bool:
		return item
	default:
		return ""
	}
}

func configurationCustomJSONField(field string) bool {
	return field == "headers" || field == "body_template" || field == "form_fields"
}

func redactConfigurationMap(config map[string]any, secrets []string) (map[string]any, error) {
	var walk func(any, string, bool) (any, error)
	walk = func(value any, field string, sensitive bool) (any, error) {
		switch item := value.(type) {
		case map[string]any:
			out := map[string]any{}
			for key, value := range item {
				// Literal keys can also contain an explicitly configured credential.
				cleaned := replaceConfigurationSecrets(key, secrets)
				if cleaned != key {
					continue
				}
				result, err := walk(value, key, sensitive || configurationSensitiveName(key))
				if err != nil {
					return nil, err
				}
				out[key] = result
			}
			return out, nil
		case []any:
			out := make([]any, len(item))
			for i, value := range item {
				result, err := walk(value, field, sensitive)
				if err != nil {
					return nil, err
				}
				out[i] = result
			}
			return out, nil
		case string:
			if configurationCustomJSONField(field) && strings.TrimSpace(item) != "" {
				parsed, err := configurationJSONValue([]byte(item))
				if err != nil {
					return nil, errors.New("自定义请求包含无效或重复的 JSON 字段，请修正后再导出")
				}
				result, err := walk(parsed, "", sensitive)
				if err != nil {
					return nil, err
				}
				// Keep user formatting when the content does not need redaction.
				originalJSON, _ := json.Marshal(parsed)
				resultJSON, _ := json.Marshal(result)
				if bytes.Equal(originalJSON, resultJSON) {
					return item, nil
				}
				return string(resultJSON), nil
			}
			text := replaceConfigurationSecrets(item, secrets)
			text, _ = configurationURLCredentials(text, true)
			if sensitive {
				return blankConfigurationCredential(text), nil
			}
			return text, nil
		default:
			if sensitive {
				return blankConfigurationCredential(item), nil
			}
			return item, nil
		}
	}
	value, err := walk(config, "", false)
	if err != nil {
		return nil, err
	}
	return value.(map[string]any), nil
}

// Remove recognized query credentials without URL-encoding surviving template
// placeholders or other query parameters. User info is always credential data.
func configurationURLCredentials(text string, redact bool) (string, int) {
	parsed, err := url.Parse(text)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return text, 0
	}
	count := 0
	if parsed.User != nil && configurationLiteralCredential(parsed.User.String()) {
		count++
		if redact {
			parsed.User = nil
		}
	}
	query := strings.Split(parsed.RawQuery, "&")
	kept := make([]string, 0, len(query))
	for _, part := range query {
		pair := strings.SplitN(part, "=", 2)
		key, err := url.QueryUnescape(pair[0])
		if err != nil || !configurationSensitiveName(key) || len(pair) < 2 {
			kept = append(kept, part)
			continue
		}
		value, err := url.QueryUnescape(pair[1])
		if err == nil && configurationLiteralCredential(value) {
			count++
			if redact {
				continue
			}
		}
		kept = append(kept, part)
	}
	if !redact || count == 0 {
		return text, count
	}
	parsed.RawQuery = strings.Join(kept, "&")
	return parsed.String(), count
}

func configurationCredentialCount(config map[string]any) (int, error) {
	count := 0
	var walk func(any, string, bool) error
	walk = func(value any, field string, custom bool) error {
		switch item := value.(type) {
		case map[string]any:
			for key, value := range item {
				if key == "api_key" && !custom {
					if text, ok := value.(string); ok && text != "" {
						count++
					}
				} else if configurationSensitiveName(key) && configurationLiteralCredential(value) {
					count++
				}
				if err := walk(value, key, custom); err != nil {
					return err
				}
			}
		case []any:
			for _, value := range item {
				if err := walk(value, field, custom); err != nil {
					return err
				}
			}
		case string:
			if configurationCustomJSONField(field) && strings.TrimSpace(item) != "" {
				parsed, err := configurationJSONValue([]byte(item))
				if err != nil {
					return errors.New("自定义请求包含无效或重复的 JSON 字段")
				}
				return walk(parsed, "", true)
			}
			_, n := configurationURLCredentials(item, false)
			count += n
		}
		return nil
	}
	if err := walk(config, "", false); err != nil {
		return 0, err
	}
	return count, nil
}
