package engine

import (
	"context"
	"encoding/json"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func checkProbeTime(t *testing.T, p ProbeResult) {
	t.Helper()
	stamp, err := time.Parse(time.RFC3339, p.TestedAt)
	if err != nil || time.Since(stamp) > 2*time.Second || p.ElapsedMS < 0 {
		t.Fatalf("missing or invalid timing: %+v", p)
	}
}

func TestProbeTranslationUsesChosenDraftAndLeavesConfigAlone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer draft-key" {
			t.Error("probe did not use selected draft endpoint and credentials")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		if body["model"] != "draft-model" || body["stream"] != false {
			t.Error("draft model or mode lost")
		}
		messages := body["messages"].([]any)
		var lines []Line
		if json.Unmarshal([]byte(messages[1].(map[string]any)["content"].(string)), &lines) != nil || !reflect.DeepEqual(lines, []Line{{ID: 0, Text: "Hello, world!"}}) {
			t.Errorf("test did not use generated text: %+v", lines)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"[{\"id\":0,\"text\":\"你好，世界！\"}]"}}]}`)
	}))
	defer server.Close()
	cfg := Config{
		TranslationServices: []TranslationService{{ID: "broken", Provider: "invalid", BaseURL: "unfinished"}, {ID: "draft", BaseURL: server.URL + "/v1", Model: "draft-model", APIKey: "draft-key"}},
		ActiveTranslationID: "draft",
		OCRServices:         []OCRService{{Provider: "invalid", URL: "unfinished"}},
		Vocabulary:          VocabularyConfig{BaseURL: "unfinished"},
	}
	before := cfg
	p := TestService(context.Background(), HTTPClient(), cfg, "translation")
	if !p.OK || p.ServiceID != "draft" || p.ItemCount != 1 || !strings.Contains(p.Message, "有效译文") || strings.Contains(p.Message, "draft-key") {
		t.Fatalf("probe failed: %+v", p)
	}
	if !reflect.DeepEqual(cfg, before) {
		t.Fatal("probe changed caller's configuration")
	}
	checkProbeTime(t, p)
}

func TestProbeTranslationRequiresUsableContractAndHidesBodies(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"http-error", "provider-echo-secret", 401},
		{"malformed", "provider-echo-secret", 200},
		{"wrong-id", `{"choices":[{"message":{"content":"[{\"id\":8,\"text\":\"provider-echo-secret\"}]"}}]}`, 200},
		{"empty", `{"choices":[{"message":{"content":"[]"}}]}`, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			cfg := Config{TranslationServices: []TranslationService{{BaseURL: server.URL, Model: "test", APIKey: "provider-echo-secret"}}}
			p := TestService(context.Background(), HTTPClient(), cfg, "translation")
			if p.OK || p.Message == "" || strings.Contains(p.Message, "provider-echo-secret") {
				t.Fatalf("failure accepted or provider content exposed: %+v", p)
			}
		})
	}
}

func TestProbeOCRGeneratedImageAndCapabilities(t *testing.T) {
	for _, coordinates := range []bool{true, false} {
		t.Run(map[bool]string{true: "coordinates", false: "text-only"}[coordinates], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				defer r.MultipartForm.RemoveAll()
				f, _, err := r.FormFile("image")
				if err != nil {
					t.Error(err)
					return
				}
				defer f.Close()
				img, err := jpeg.Decode(f)
				if err != nil || img.Bounds().Dx() != 192 || img.Bounds().Dy() != 64 {
					t.Error("OCR did not receive fixed generated image")
					return
				}
				light, _, _, _ := img.At(0, 0).RGBA()
				dark, _, _, _ := img.At(22, 16).RGBA()
				if light < 60000 || dark > 10000 {
					t.Error("generated fixture is not legible black letters on white")
				}
				io.WriteString(w, `[{"text":"HELLO","box":[[20,14],[165,14],[165,49],[20,49]]}]`)
			}))
			defer server.Close()
			cfg := Config{OCRServices: []OCRService{{ID: "ocr-draft", URL: server.URL, Coordinates: coordinates}}}
			p := TestService(context.Background(), HTTPClient(), cfg, "ocr")
			if !p.OK || p.ServiceID != "ocr-draft" || p.Coordinates == nil || *p.Coordinates != coordinates || p.ItemCount != 1 {
				t.Fatalf("incorrect capability result: %+v", p)
			}
			if strings.Contains(p.Message, "HELLO") {
				t.Fatal("probe returned provider text rather than test status")
			}
		})
	}
}

func TestProbeOCREmptyResponseIsNotPass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `[]`) }))
	defer server.Close()
	p := TestService(context.Background(), HTTPClient(), Config{OCRServices: []OCRService{{URL: server.URL, Coordinates: true}}}, "ocr")
	if p.OK || !strings.Contains(p.Message, "没有识别到文字") {
		t.Fatalf("empty response confused with working OCR: %+v", p)
	}
}

func TestProbeVocabularyOnlyReadsAndChecksSelectedCategory(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || r.URL.Path != "/studylist/category" || r.URL.Query().Get("language") != "en" || r.Header.Get("Authorization") != "NIS fake-key" || r.ContentLength > 0 {
			t.Error("vocabulary probe wrote to account or ignored draft parameters")
		}
		io.WriteString(w, `{"data":[{"id":9223372036854775807,"name":"sample"}]}`)
	}))
	defer server.Close()
	for _, category := range []string{"0", "9223372036854775807", "9223372036854775806"} {
		cfg := Config{Vocabulary: VocabularyConfig{APIKey: "NIS fake-key", BaseURL: server.URL, Language: "en", CategoryID: category}}
		p := TestService(context.Background(), HTTPClient(), cfg, "vocabulary")
		if p.OK != (category != "9223372036854775806") || strings.Contains(p.Message, "fake-key") || strings.Contains(p.Message, "922337") {
			t.Fatalf("wrong category check or exposed private ID: %+v", p)
		}
		if p.OK && !strings.Contains(p.Message, "未执行收藏写入") {
			t.Fatal("read-only test presented as successful account write")
		}
	}
	if requests != 3 {
		t.Fatalf("unexpected request count %d", requests)
	}
}

func TestProbeSpeechFetchesWithoutPlayingAndDoesNotPretendSystemPassed(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["input"] != "Hello." || r.URL.Path != "/v1/audio/speech" {
			t.Error("TTS did not use generated short input")
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(serviceTestWAV())
	}))
	defer server.Close()
	cfg := Config{TTSServices: []TTSService{{ID: "tts-draft", Provider: "openai", URL: server.URL + "/v1"}}}
	p := TestService(context.Background(), HTTPClient(), cfg, "tts")
	if !p.OK || p.ServiceID != "tts-draft" || p.AudioFormat != "wav" || !strings.Contains(p.Message, "未播放") {
		t.Fatalf("bad speech result: %+v", p)
	}
	cfg.TTSServices[0].Provider = "system"
	p = TestService(context.Background(), HTTPClient(), cfg, "tts")
	if p.OK || !strings.Contains(p.Message, "本机检查") || requests != 1 {
		t.Fatalf("system speech incorrectly tested with remote requests: %+v, %d", p, requests)
	}
}

func TestProbeDictionaryAIAndOffline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid dictionary body")
		}
		messages := body["messages"].([]any)
		if messages[1].(map[string]any)["content"] != `{"word":"hello"}` {
			t.Error("dictionary probe did not use fixed word")
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"word\":\"hello\",\"definitions\":[\"provider-private-definition\"]}"}}]}`)
	}))
	defer server.Close()
	cfg := Config{Dictionary: DictionaryConfig{Provider: "openai"}, TranslationServices: []TranslationService{{BaseURL: server.URL, Model: "dictionary-test"}}}
	p := TestService(context.Background(), HTTPClient(), cfg, "dictionary")
	if !p.OK || p.ItemCount != 1 || strings.Contains(p.Message, "provider-private-definition") {
		t.Fatalf("dictionary probe failed or returned entry data: %+v", p)
	}
	for _, test := range []struct{ extension, body string }{
		{".json", `{"quokka":"n. 短尾矮袋鼠"}`},
		{".csv", "word,translation\nquokka,n. 短尾矮袋鼠\n"},
	} {
		path := filepath.Join(t.TempDir(), "dictionary"+test.extension)
		if err := os.WriteFile(path, []byte(test.body), 0600); err != nil {
			t.Fatal(err)
		}
		cfg.Dictionary = DictionaryConfig{Provider: "local", LocalPath: path}
		cfg.TranslationServices = []TranslationService{{Provider: "invalid", BaseURL: "unfinished"}}
		p = TestService(context.Background(), nil, cfg, "dictionary")
		if !p.OK || strings.Contains(p.Message, "quokka") || strings.Contains(p.Message, path) {
			t.Fatalf("custom offline dictionary depends on test word or leaked content: %+v", p)
		}
	}
}

func TestProbeInvalidDraftAndCanceledContextMakeNoRequests(t *testing.T) {
	requests := 0
	client := HTTPClient()
	client.Transport = serviceTestTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		return nil, nil
	})
	cfg := Config{TranslationServices: []TranslationService{{BaseURL: "https://example.test", Model: "test", RequestMode: "custom", BodyTemplate: `{"stream":true}`}}}
	p := TestService(context.Background(), client, cfg, "translation")
	if p.OK || !strings.Contains(p.Message, "stream") || requests != 0 {
		t.Fatalf("invalid request reached service: %+v", p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p = TestService(ctx, client, cfg, "translation")
	if p.OK || p.Message != "测试已取消" || requests != 0 {
		t.Fatalf("canceled test continued: %+v", p)
	}
	p = TestService(context.Background(), client, cfg, "unknown")
	if p.OK || requests != 0 {
		t.Fatalf("unknown service test reached provider: %+v", p)
	}
}
