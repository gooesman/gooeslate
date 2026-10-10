package engine

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ProbeResult describes an explicit test of the supplied draft. It is not a
// configuration record or a guarantee that later requests will succeed.
type ProbeResult struct {
	Kind        string `json:"kind"`
	ServiceID   string `json:"service_id,omitempty"`
	OK          bool   `json:"ok"`
	Message     string `json:"message"`
	ElapsedMS   int64  `json:"elapsed_ms"`
	TestedAt    string `json:"tested_at"`
	Coordinates *bool  `json:"coordinates,omitempty"`
	ItemCount   int    `json:"item_count,omitempty"`
	AudioFormat string `json:"audio_format,omitempty"`
}

// TestService sends only generated test input. It never saves settings, captures
// the desktop, plays audio, or writes to an external vocabulary account. The
// desktop bridge handles local system speech capability checks separately.
func TestService(ctx context.Context, client *http.Client, cfg Config, kind string) (result ProbeResult) {
	started := time.Now()
	result.Kind = kind
	defer func() {
		result.ElapsedMS = time.Since(started).Milliseconds()
		result.TestedAt = time.Now().UTC().Format(time.RFC3339)
	}()
	if err := ctx.Err(); err != nil {
		result.Message = probeErrorMessage(err, cfg)
		return result
	}
	cfg = NormalizeConfig(cfg)
	var err error
	switch kind {
	case "translation":
		result.ServiceID = ActiveTranslation(cfg).ID
	case "ocr":
		result.ServiceID = ActiveOCR(cfg).ID
	case "tts":
		result.ServiceID = ActiveTTS(cfg).ID
	case "vocabulary", "dictionary":
	default:
		result.Message = "请选择有效的服务类型"
		return result
	}
	// Validate only the chosen service. An unrelated unfinished draft must not
	// prevent the user from testing a complete service.
	if err = ValidateServiceConfig(probeConfig(cfg, kind)); err != nil {
		result.Message = probeErrorMessage(err, cfg)
		return result
	}
	switch kind {
	case "translation":
		var values []Translation
		values, err = Translate(ctx, client, cfg, []Line{{ID: 0, Text: "Hello, world!"}})
		if err == nil {
			result.OK, result.ItemCount = true, len(values)
			result.Message = "测试通过，已收到有效译文"
		}
	case "ocr":
		var blocks []Block
		blocks, err = RecognizeConfigured(ctx, client, cfg, probeOCRImage())
		if err == nil && len(blocks) == 0 {
			err = errors.New("接口已响应，但测试图片没有识别到文字，请检查识别服务和响应字段")
		}
		if err == nil {
			coordinates := ActiveOCR(cfg).Coordinates
			for _, b := range blocks {
				bounds := b.Bounds()
				coordinates = coordinates && bounds.W > 0 && bounds.H > 0
			}
			result.OK, result.ItemCount, result.Coordinates = true, len(blocks), &coordinates
			if coordinates {
				result.Message = "测试通过，已收到文字和坐标，可用于原图选字与原图翻译"
			} else {
				result.Message = "文字识别测试通过；此配置未返回坐标，不能用于原图选字与原图翻译"
			}
		}
	case "tts":
		p := ActiveTTS(cfg)
		if p.Provider == "system" {
			err = errors.New("系统语音需要由本机检查可用声音，尚未完成测试")
		} else {
			var format string
			_, format, err = FetchSpeech(ctx, client, p, "Hello.")
			if err == nil {
				result.OK, result.AudioFormat = true, format
				result.Message = "测试通过，已收到 " + strings.ToUpper(format) + " 音频；未播放"
			}
		}
	case "vocabulary":
		var categories []VocabularyCategory
		categories, err = ListVocabulary(ctx, client, cfg.Vocabulary)
		if err == nil {
			selected := strings.TrimSpace(cfg.Vocabulary.CategoryID)
			found := selected == "" || selected == "0"
			for _, c := range categories {
				found = found || c.ID == selected
			}
			if !found {
				err = errors.New("连接成功，但列表中未找到所填生词本 ID，请检查语言和 ID")
			} else {
				result.OK, result.ItemCount = true, len(categories)
				result.Message = "连接测试通过，已读取生词本列表；未执行收藏写入"
			}
		}
	case "dictionary":
		word := "hello"
		if cfg.Dictionary.Provider == "local" {
			word, err = probeLocalDictionaryWord(ctx, cfg.Dictionary.LocalPath)
		}
		if err == nil {
			var entry DictionaryEntry
			entry, err = LookupDictionary(ctx, client, cfg, word)
			if err == nil {
				result.OK, result.ItemCount = true, len(entry.Definitions)
				result.Message = "查词测试通过，已收到有效释义"
			}
		}
	}
	if err != nil {
		result.Message = probeErrorMessage(err, cfg)
	}
	return result
}

func probeConfig(cfg Config, kind string) Config {
	isolated := DefaultConfig()
	isolated.Language = cfg.Language
	switch kind {
	case "translation":
		isolated.TranslationServices = []TranslationService{ActiveTranslation(cfg)}
	case "ocr":
		isolated.OCRServices = []OCRService{ActiveOCR(cfg)}
	case "tts":
		isolated.TTSServices = []TTSService{ActiveTTS(cfg)}
	case "vocabulary":
		isolated.Vocabulary = cfg.Vocabulary
	case "dictionary":
		if cfg.Dictionary.Provider == "openai" || cfg.Dictionary.Provider == "" {
			isolated.TranslationServices = []TranslationService{ActiveTranslation(cfg)}
		} else if cfg.Dictionary.Provider == "eudic" {
			isolated.Vocabulary = cfg.Vocabulary
		}
	}
	return isolated
}

func probeErrorMessage(err error, cfg Config) string {
	if errors.Is(err, context.Canceled) {
		return "测试已取消"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "测试超时，请检查网络或调整服务超时时间"
	}
	message := err.Error()
	keys := []string{cfg.APIKey, cfg.Vocabulary.APIKey}
	for _, p := range cfg.TranslationServices {
		keys = append(keys, p.APIKey)
	}
	for _, p := range cfg.OCRServices {
		keys = append(keys, p.APIKey)
	}
	for _, p := range cfg.TTSServices {
		keys = append(keys, p.APIKey)
	}
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			message = strings.ReplaceAll(message, key, "[授权信息]")
		}
	}
	if utf8.RuneCountInString(message) > 500 {
		return "测试失败，请检查接口地址、授权信息及请求和响应配置"
	}
	return message
}

// A fixed bitmap keeps the OCR test independent of fonts, screen contents and
// machine locale. It spells HELLO on a white 192 x 64 image.
func probeOCRImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 192, 64))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	glyphs := map[rune][7]string{
		'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
		'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
		'L': {"10000", "10000", "10000", "10000", "10000", "10000", "11111"},
		'O': {"01110", "10001", "10001", "10001", "10001", "10001", "01110"},
	}
	for i, letter := range "HELLO" {
		for y, row := range glyphs[letter] {
			for x, pixel := range row {
				if pixel == '1' {
					r := image.Rect(20+i*30+x*5, 14+y*5, 25+i*30+x*5, 19+y*5)
					draw.Draw(img, r, image.NewUniform(color.Black), image.Point{}, draw.Src)
				}
			}
		}
	}
	return img
}

// Probe a real entry from the offline dictionary instead of requiring one
// particular word to be present in a user's small custom dictionary.
func probeLocalDictionaryWord(ctx context.Context, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("请先选择本地 JSON 或 ECDICT CSV 词典")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("无法打开本地词典，请检查文件路径")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("请选择有效的词典文件")
	}
	validWord := func(word string) bool {
		return strings.TrimSpace(word) != "" && utf8.RuneCountInString(word) <= 150 && strings.IndexFunc(word, unicode.IsControl) < 0
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		if info.Size() > 32<<20 {
			return "", errors.New("JSON 词典超过 32 MB，请使用 ECDICT CSV 格式")
		}
		var entries map[string]json.RawMessage
		if json.NewDecoder(io.LimitReader(f, (32<<20)+1)).Decode(&entries) != nil {
			return "", errors.New("JSON 词典需要使用单词到释义的对象映射")
		}
		words := make([]string, 0, len(entries))
		for word := range entries {
			if validWord(word) {
				words = append(words, word)
			}
		}
		sort.Strings(words)
		if len(words) != 0 {
			return words[0], ctx.Err()
		}
	case ".csv":
		if info.Size() > 256<<20 {
			return "", errors.New("CSV 词典超过 256 MB")
		}
		reader := csv.NewReader(io.LimitReader(f, (256<<20)+1))
		reader.FieldsPerRecord = -1
		header, err := reader.Read()
		if err != nil {
			return "", errors.New("无法读取 CSV 词典表头")
		}
		wordColumn := -1
		for i, name := range header {
			if strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "\ufeff") == "word" {
				wordColumn = i
			}
		}
		if wordColumn < 0 {
			return "", errors.New("CSV 词典需要 word 和 translation 或 definition 列")
		}
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			row, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", errors.New("CSV 词典格式无效")
			}
			if wordColumn < len(row) && validWord(row[wordColumn]) {
				return row[wordColumn], nil
			}
		}
	default:
		return "", errors.New("本地词典支持 JSON 和 ECDICT CSV 文件")
	}
	return "", errors.New("本地词典没有可测试的词条")
}
