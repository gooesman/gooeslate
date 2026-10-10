package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// VocabularyCategory IDs must remain strings: EuDic IDs exceed JavaScript's
// safe integer range. See https://my.eudic.net/OpenAPI/doc_api_study.
type VocabularyCategory struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Language string `json:"language"`
}

func (category *VocabularyCategory) UnmarshalJSON(raw []byte) error {
	var value struct {
		ID       json.RawMessage `json:"id"`
		Name     string          `json:"name"`
		Language string          `json:"language"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	var id string
	if json.Unmarshal(value.ID, &id) != nil {
		var number json.Number
		if json.Unmarshal(value.ID, &number) != nil || number == "" || strings.ContainsAny(number.String(), ".eE+-") {
			return errors.New("生词本 ID 格式无效")
		}
		id = number.String()
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("生词本 ID 不能为空")
	}
	*category = VocabularyCategory{ID: id, Name: value.Name, Language: value.Language}
	return nil
}

func vocabularyRequest(ctx context.Context, client *http.Client, cfg VocabularyConfig, method, path string, query url.Values, payload any) ([]byte, error) {
	key := strings.TrimSpace(cfg.APIKey)
	if len(key) >= 4 && strings.EqualFold(key[:4], "NIS ") {
		key = strings.TrimSpace(key[4:])
	}
	if key == "" || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return nil, errors.New("请在生词本设置中填写欧路 OpenAPI 授权信息")
	}
	switch cfg.Language {
	case "en", "fr", "de", "es":
	default:
		return nil, errors.New("欧路生词本语言请选择英语、法语、德语或西班牙语")
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = "https://api.frdic.com/api/open/v1"
	}
	if err := ValidateURL(base); err != nil {
		return nil, err
	}
	u, _ := url.Parse(base)
	u.Path = strings.TrimRight(u.Path, "/") + path
	q := u.Query()
	for k, values := range query {
		q[k] = values
	}
	u.RawQuery = q.Encode()
	timeout := cfg.TimeoutMS
	if timeout <= 0 {
		timeout = 30000
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			return nil, errors.New("无法构造生词本请求")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), &body)
	if err != nil {
		return nil, errors.New("生词本地址无效")
	}
	req.Header.Set("Authorization", "NIS "+key)
	req.Header.Set("User-Agent", "gooestranslate")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if client == nil {
		client = HTTPClient()
	}
	return response(client, req)
}

func ListVocabulary(ctx context.Context, client *http.Client, cfg VocabularyConfig) ([]VocabularyCategory, error) {
	raw, err := vocabularyRequest(ctx, client, cfg, http.MethodGet, "/studylist/category", url.Values{"language": {cfg.Language}}, nil)
	if err != nil {
		return nil, err
	}
	var reply struct {
		Data []VocabularyCategory `json:"data"`
	}
	if json.Unmarshal(raw, &reply) != nil || reply.Data == nil {
		return nil, errors.New("欧路没有返回有效的生词本列表")
	}
	return reply.Data, nil
}

func vocabularyWord(word string) (string, error) {
	word = strings.TrimSpace(word)
	if word == "" || utf8.RuneCountInString(word) > 150 || strings.IndexFunc(word, unicode.IsControl) >= 0 {
		return "", errors.New("请选择不超过 150 个字符的单词或短语")
	}
	return word, nil
}

func AddVocabulary(ctx context.Context, client *http.Client, cfg VocabularyConfig, word string) (string, error) {
	word, err := vocabularyWord(word)
	if err != nil {
		return "", err
	}
	if !cfg.Enabled {
		return "", errors.New("请先在设置中启用欧路生词本")
	}
	if cfg.Language == "en" && strings.IndexFunc(word, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
		return "", errors.New("英语生词本请收藏英文原文")
	}
	id := strings.TrimSpace(cfg.CategoryID)
	if id == "" {
		id = "0"
	}
	raw, err := vocabularyRequest(ctx, client, cfg, http.MethodPost, "/studylist/words", nil, map[string]any{"language": cfg.Language, "category_id": id, "words": []string{word}})
	if err != nil {
		return "", err
	}
	var reply struct {
		Message string `json:"message"`
	}
	if len(bytes.TrimSpace(raw)) != 0 && json.Unmarshal(raw, &reply) != nil {
		return "", errors.New("欧路返回了无效的收藏结果")
	}
	// HTTP 201/204 is authoritative. Do not display account/provider payloads.
	return "已收藏到欧路生词本", nil
}

var vocabularyTags = regexp.MustCompile(`<[^>]*>`)

func LookupVocabulary(ctx context.Context, client *http.Client, cfg VocabularyConfig, word string) (DictionaryEntry, error) {
	word, err := vocabularyWord(word)
	if err != nil {
		return DictionaryEntry{}, err
	}
	raw, err := vocabularyRequest(ctx, client, cfg, http.MethodGet, "/studylist/word", url.Values{"language": {cfg.Language}, "word": {word}}, nil)
	if err != nil {
		return DictionaryEntry{}, err
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return DictionaryEntry{}, errors.New("欧路没有返回有效词条")
	}
	if data, ok := envelope["data"]; ok {
		raw = data
	}
	var entry struct {
		Word string `json:"word"`
		Exp  string `json:"exp"`
		Phon string `json:"phon"`
	}
	if json.Unmarshal(raw, &entry) != nil {
		var entries []json.RawMessage
		if json.Unmarshal(raw, &entries) != nil || len(entries) == 0 || json.Unmarshal(entries[0], &entry) != nil {
			return DictionaryEntry{}, errors.New("欧路生词本里没有该词，请使用模型或离线词典查词")
		}
	}
	if strings.TrimSpace(entry.Word) == "" {
		return DictionaryEntry{}, errors.New("欧路生词本里没有该词，请使用模型或离线词典查词")
	}
	meaning := strings.ReplaceAll(strings.ReplaceAll(entry.Exp, "<br>", "\n"), "<br/>", "\n")
	meaning = strings.TrimSpace(html.UnescapeString(vocabularyTags.ReplaceAllString(meaning, "")))
	if meaning == "" {
		meaning = "该词已收藏，欧路未返回释义"
	}
	return validateDictionary(DictionaryEntry{Word: entry.Word, Phonetic: entry.Phon, Definitions: strings.Split(meaning, "\n")}, word, "欧路生词本词条")
}
