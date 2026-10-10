package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIEndpointBareOriginAndExplicitPaths(t *testing.T) {
	for _, tc := range []struct{ base, suffix, want string }{
		{"https://service.example", "/chat/completions", "https://service.example/v1/chat/completions"},
		{" https://service.example/ ", "/chat/completions", "https://service.example/v1/chat/completions"},
		{"http://127.0.0.1:11434?deployment=test", "/chat/completions", "http://127.0.0.1:11434/v1/chat/completions?deployment=test"},
		{"https://service.example/v1/", "/chat/completions", "https://service.example/v1/chat/completions"},
		{"https://service.example/proxy/openai?api-version=2026", "/chat/completions", "https://service.example/proxy/openai/chat/completions?api-version=2026"},
		{"https://service.example/chat/completions/?deployment=test", "/chat/completions", "https://service.example/chat/completions?deployment=test"},
		{"https://service.example/v1/chat/completions", "/chat/completions", "https://service.example/v1/chat/completions"},
		{"https://service.example/", "/audio/speech", "https://service.example/v1/audio/speech"},
		{"https://service.example/custom/audio/speech/?deployment=test", "/audio/speech", "https://service.example/custom/audio/speech?deployment=test"},
	} {
		if got := openAIEndpoint(tc.base, tc.suffix); got != tc.want {
			t.Errorf("base %q: got %q, want %q", tc.base, got, tc.want)
		}
	}
}

func TestTranslationAndDictionaryUseAPIAtBareOrigin(t *testing.T) {
	for _, operation := range []string{"translation", "dictionary"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/chat/completions" {
					w.Header().Set("Content-Type", "text/html")
					io.WriteString(w, "<!DOCTYPE html><html>Gateway web UI</html>")
					return
				}
				if r.Method != http.MethodPost || r.URL.Query().Get("deployment") != "test" || r.Header.Get("Authorization") != "Bearer endpoint-test-key" {
					t.Error("API request lost method, query, or credentials")
				}
				content := `[{"id":0,"text":"你好"}]`
				if operation == "dictionary" {
					content = `{"word":"hello","phonetic":"həˈləʊ","definitions":["int. 你好"]}`
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
			}))
			defer server.Close()
			cfg := Config{Language: "简体中文", TranslationServices: []TranslationService{{ID: "root", Provider: "openai", BaseURL: server.URL + "/?deployment=test", Model: "test", APIKey: "endpoint-test-key"}}}
			if operation == "translation" {
				got, err := Translate(context.Background(), server.Client(), cfg, []Line{{ID: 0, Text: "Hello"}})
				if err != nil || len(got) != 1 || got[0].Text != "你好" {
					t.Fatalf("translation: %+v %v", got, err)
				}
			} else {
				got, err := LookupDictionary(context.Background(), server.Client(), cfg, "hello")
				if err != nil || got.Word != "hello" || len(got.Definitions) != 1 {
					t.Fatalf("dictionary: %+v %v", got, err)
				}
			}
			if calls != 1 {
				t.Fatalf("issued %d requests, want one API request", calls)
			}
		})
	}
}

func TestOpenAISpeechUsesAPIAtBareOrigin(t *testing.T) {
	wav := serviceTestWAV()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/speech" || r.URL.Query().Get("deployment") != "test" {
			t.Error("speech did not use API at origin")
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(wav)
	}))
	defer server.Close()
	audio, format, err := FetchSpeech(context.Background(), server.Client(), TTSService{Provider: "openai", URL: server.URL + "?deployment=test"}, "Hello")
	if err != nil || format != "wav" || !bytes.Equal(audio, wav) {
		t.Fatalf("speech: format=%s err=%v", format, err)
	}
}

func TestOpenAIOCRUsesAPIAtBareOriginWithCoordinates(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer vision-endpoint-test" {
			t.Error("vision OCR did not use API at origin with its own credentials")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Content []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Model != "vision-test" || body.Stream || len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 {
			t.Error("vision OCR request is missing model or image content")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		content := body.Messages[0].Content
		if content[0].Type != "text" || !strings.Contains(content[0].Text, "pixel-coordinate") || content[1].Type != "image_url" || !strings.HasPrefix(content[1].ImageURL.URL, "data:image/jpeg;base64,") {
			t.Error("vision OCR did not request coordinates for a JPEG image")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `[{"text":"Hello","box":[[2,3],[30,3],[30,15],[2,15]]}]`}}}})
	}))
	defer server.Close()
	cfg := Config{OCRServices: []OCRService{{Provider: "openai", URL: server.URL, Model: "vision-test", APIKey: "vision-endpoint-test", Coordinates: true}}}
	blocks, err := RecognizeConfigured(context.Background(), server.Client(), cfg, image.NewRGBA(image.Rect(0, 0, 40, 20)))
	if err != nil || len(blocks) != 1 || blocks[0].Text != "Hello" || blocks[0].Box != ([4][2]float64{{2, 3}, {30, 3}, {30, 15}, {2, 15}}) || blocks[0].Bounds() != (Rect{X: 2, Y: 3, W: 28, H: 12}) {
		t.Fatalf("vision OCR coordinates: %+v %v", blocks, err)
	}
	if calls != 1 {
		t.Fatalf("issued %d requests, want one vision API request", calls)
	}
}

func TestResponseValueExplainsHTMLWithoutExposingResponse(t *testing.T) {
	for _, raw := range []string{
		"<!DOCTYPE html><html>echo-secret-body</html>",
		"  \n<HTML><BODY>echo-secret-body</BODY></HTML>",
		"\xef\xbb\xbf<!-- comment --><html>echo-secret-body</html>",
	} {
		_, err := responseValue([]byte(raw), "choices.0.message.content")
		if err == nil || !strings.Contains(err.Error(), "HTML") || !strings.Contains(err.Error(), "API") || strings.Contains(err.Error(), "echo-secret-body") {
			t.Fatalf("HTML diagnostic missing or echoed provider body: %v", err)
		}
	}
	if _, err := responseValue([]byte("not-json echo-secret-body"), ""); err == nil || strings.Contains(err.Error(), "HTML") || strings.Contains(err.Error(), "echo-secret-body") {
		t.Fatalf("non-HTML malformed response: %v", err)
	}
	got, err := responseValue([]byte(`{"content":"<html>valid JSON data</html>"}`), "content")
	if err != nil || got != "<html>valid JSON data</html>" {
		t.Fatalf("valid JSON containing HTML was rejected: %v %v", got, err)
	}
}
