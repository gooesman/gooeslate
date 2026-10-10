package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type serviceTestTransport func(*http.Request) (*http.Response, error)

func (f serviceTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConfiguredDeadlineAndPreCanceledOCR(t *testing.T) {
	client := HTTPClient()
	if client.Timeout != 0 {
		t.Fatal("global client timeout overrides configurable request deadlines")
	}
	client.Transport = serviceTestTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if remaining := time.Until(deadline); !ok || remaining < 110*time.Second || remaining > 121*time.Second {
			t.Error("profile timeout not used")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"[{\"id\":0,\"text\":\"你好\"}]"}}]}`))}, nil
	})
	c := Config{TranslationServices: []TranslationService{{BaseURL: "https://service.example/v1", Model: "test", TimeoutMS: 120000}}}
	if _, err := Translate(context.Background(), client, c, []Line{{ID: 0, Text: "Hello"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.OCRServices = []OCRService{{Provider: "json", URL: "https://service.example/ocr", Coordinates: false}}
	if _, err := RecognizeConfigured(ctx, HTTPClient(), c, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != context.Canceled {
		t.Fatalf("pre-canceled OCR ignored context: %v", err)
	}
}

func TestConfiguredTranslationCustomBodyAndActiveCredentials(t *testing.T) {
	text := "你好 \"quoted\"\\path\n{{api_key}}"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/custom/translate" || r.Header.Get("Authorization") != "Bearer profile-test" || r.Header.Get("X-Profile") != "profile-test" {
			t.Error("custom URL or profile credentials not respected")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if body["input"] != text || body["target"] != "英语" || body["source"] != "简体中文" || body["model"] != "selected" {
			t.Errorf("wrong text/language fields: %+v", body)
		}
		lines, ok := body["lines"].([]any)
		if !ok || len(lines) != 1 || lines[0].(map[string]any)["id"] != float64(42) {
			t.Error("typed lines placeholder was flattened")
		}
		messages, ok := body["messages"].([]any)
		if !ok || len(messages) != 2 || messages[0].(map[string]any)["content"] != "Target: 英语" || messages[1].(map[string]any)["content"] != text {
			t.Error("custom prompts did not produce typed messages")
		}
		kwargs := body["chat_template_kwargs"].(map[string]any)
		if kwargs["enable_thinking"] != false || kwargs["keep"] != true {
			t.Error("disable-thinking replaced other chat template options")
		}
		if body["stream"] != false {
			t.Error("streaming request sent")
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"rows": []Translation{{ID: 42, Text: "Hello"}}}})
	}))
	defer server.Close()
	c := DefaultConfig()
	c.APIKey = "unused-legacy-test"
	c.TranslationServices = []TranslationService{{ID: "unused", BaseURL: "https://unused.example", APIKey: "unused-test"}, {ID: "chosen", Provider: "http", BaseURL: server.URL + "/custom/translate", Model: "selected", APIKey: "profile-test", Headers: `{"X-Profile":"{{api_key}}"}`, DisableThinking: true, RequestMode: "custom", SystemPrompt: "Target: {{target_language}}", UserPrompt: "{{text}}", BodyTemplate: `{"input":"{{text}}","target":"{{target_language}}","source":"{{source_language}}","model":"{{model}}","lines":"{{lines}}","messages":"{{messages}}","stream":false,"chat_template_kwargs":{"enable_thinking":true,"keep":true}}`, ResponsePath: "data.rows"}}
	c.ActiveTranslationID = "chosen"
	// English tokens would dominate automatic detection, so use a Chinese-only
	// source for the language assertion while retaining escaping in a second line.
	text = "你好，这是引用和路径测试 \"引号\"\\路径\n{{api_key}}"
	result, err := Translate(context.Background(), HTTPClient(), c, []Line{{ID: 42, Text: text}})
	if err != nil || !reflect.DeepEqual(result, []Translation{{ID: 42, Text: "Hello"}}) {
		t.Fatalf("custom translation: %+v %v", result, err)
	}
}

func TestConfiguredTranslationHTTPPlainFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"translated": "你好\n世界"})
	}))
	defer server.Close()
	c := Config{Language: "简体中文", TranslationServices: []TranslationService{{Provider: "http", BaseURL: server.URL, RequestMode: "custom", BodyTemplate: `{"source":"{{text}}"}`, ResponsePath: "translated"}}}
	got, err := Translate(context.Background(), HTTPClient(), c, []Line{{ID: 3, Text: "Hello"}, {ID: 8, Text: "World"}})
	if err != nil || !reflect.DeepEqual(got, []Translation{{ID: 3, Text: "你好"}, {ID: 8, Text: "世界"}}) {
		t.Fatalf("custom plain response: %+v %v", got, err)
	}
}

func TestConfiguredOCRMultipartFieldsAndXYWH(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/ocr" || r.Header.Get("Authorization") != "Bearer ocr-test" || r.Header.Get("X-OCR-Key") != "ocr-test" {
			t.Error("OCR used another service's credentials")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		if r.FormValue("coords") != "True" || r.FormValue("language") != "auto" || r.FormValue("boolean") != "true" {
			t.Error("custom form fields not encoded")
		}
		f, h, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer f.Close()
		if h.Header.Get("Content-Type") != "image/jpeg" {
			t.Error("OCR file was not JPEG")
		}
		if _, err := jpeg.Decode(f); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"data":{"items":[{"content":{"value":"Hello"},"region":{"x":-1,"y":2,"width":42,"height":18}}]}}`)
	}))
	defer server.Close()
	c := DefaultConfig()
	c.APIKey = "translation-test-do-not-send"
	c.OCRServices = []OCRService{{Provider: "multipart", URL: server.URL + "/ocr", ImageField: "file", APIKey: "ocr-test", Headers: `{"X-OCR-Key":"{{api_key}}","Content-Type":"invalid-boundary"}`, FormFields: `{"coords":"True","language":"auto","boolean":true}`, ResponsePath: "data.items", TextField: "content.value", BoxField: "region", BoxFormat: "xywh", Coordinates: true}}
	blocks, err := RecognizeConfigured(context.Background(), HTTPClient(), c, image.NewRGBA(image.Rect(0, 0, 64, 32)))
	if err != nil || len(blocks) != 1 || blocks[0].Text != "Hello" || blocks[0].Bounds() != (Rect{X: 0, Y: 2, W: 41, H: 18}) {
		t.Fatalf("configured multipart: %+v %v", blocks, err)
	}
}

func TestConfiguredOCRJSONImageEncodings(t *testing.T) {
	for _, encoding := range []string{"base64", "data_url"} {
		t.Run(encoding, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				encoded, ok := body["picture"].(string)
				if !ok {
					t.Error("missing image placeholder")
					w.WriteHeader(400)
					return
				}
				if strings.HasPrefix(encoded, "data:image/jpeg;base64,") != (encoding == "data_url") {
					t.Error("wrong image encoding")
				}
				encoded = strings.TrimPrefix(encoded, "data:image/jpeg;base64,")
				raw, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					t.Error(err)
				}
				img, err := jpeg.Decode(bytes.NewReader(raw))
				if err != nil || img.Bounds().Dx() != 40 {
					t.Error("JSON picture was not a JPEG screenshot")
				}
				if body["return_word_box"] != true || body["stream"] != false || r.Header.Get("Authorization") != "Bearer json-ocr-test" {
					t.Error("OCR template types or credentials incorrect")
				}
				io.WriteString(w, `{"result":[{"text":"文字","box":[[1,2],[20,2],[20,10],[1,10]]}]}`)
			}))
			defer server.Close()
			c := Config{OCRServices: []OCRService{{Provider: "json", URL: server.URL, APIKey: "json-ocr-test", ImageEncoding: encoding, BodyTemplate: `{"picture":"{{image}}","return_word_box":true,"stream":false}`, ResponsePath: "result", Coordinates: true}}}
			blocks, err := RecognizeConfigured(context.Background(), HTTPClient(), c, image.NewRGBA(image.Rect(0, 0, 40, 20)))
			if err != nil || len(blocks) != 1 || blocks[0].Text != "文字" {
				t.Fatalf("JSON OCR: %+v %v", blocks, err)
			}
		})
	}
}

func TestConfiguredOCRVisionAndTextOnlyResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer vision-test" {
			t.Error("vision route or key incorrect")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if body["model"] != "vision-model" || body["stream"] != false {
			t.Error("vision model options missing")
		}
		messages := body["messages"].([]any)
		content := messages[0].(map[string]any)["content"].([]any)
		imageURL := content[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
		if !strings.HasPrefix(imageURL, "data:image/jpeg;base64,") {
			t.Error("vision request did not use image data URL")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "first\nsecond"}}}})
	}))
	defer server.Close()
	c := Config{OCRServices: []OCRService{{Provider: "openai", URL: server.URL + "/v1", Model: "vision-model", APIKey: "vision-test", Coordinates: false, BodyTemplate: `{"model":"{{model}}","messages":"{{messages}}","stream":false}`}}}
	blocks, err := RecognizeConfigured(context.Background(), HTTPClient(), c, image.NewRGBA(image.Rect(0, 0, 40, 20)))
	if err != nil || Text(blocks) != "first\nsecond" || blocks[0].Box != ([4][2]float64{}) {
		t.Fatalf("plain vision OCR: %+v %v", blocks, err)
	}
}

func serviceTestWAV() []byte {
	b := make([]byte, 46)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	copy(b[8:16], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:20], 16)
	binary.LittleEndian.PutUint16(b[20:22], 1)
	binary.LittleEndian.PutUint16(b[22:24], 1)
	binary.LittleEndian.PutUint32(b[24:28], 8000)
	binary.LittleEndian.PutUint32(b[28:32], 16000)
	binary.LittleEndian.PutUint16(b[32:34], 2)
	binary.LittleEndian.PutUint16(b[34:36], 16)
	copy(b[36:40], "data")
	binary.LittleEndian.PutUint32(b[40:44], 2)
	return b
}

func TestConfiguredSpeechOpenAIAndCustomHTTP(t *testing.T) {
	for _, provider := range []string{"openai", "http"} {
		t.Run(provider, func(t *testing.T) {
			wav := serviceTestWAV()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer speech-test" || r.Header.Get("X-Speech") != "speech-test" {
					t.Error("wrong speech credentials")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if body["voice"] != "voice-test" || body["speed"] != 1.25 {
					t.Error("speech template did not preserve typed speed/voice")
				}
				if provider == "openai" {
					if r.URL.Path != "/v1/audio/speech" || body["model"] != "tts-test" || body["input"] != "你好" || body["response_format"] != "wav" {
						t.Error("wrong OpenAI audio request")
					}
					w.Header().Set("Content-Type", "audio/wav")
					w.Write(wav)
				} else {
					if r.URL.Path != "/v1" || body["text"] != "你好" {
						t.Error("custom audio endpoint or body was altered")
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]string{"audio": "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(wav)})
				}
			}))
			defer server.Close()
			p := TTSService{Provider: provider, URL: server.URL + "/v1", Model: "tts-test", Voice: "voice-test", Speed: 1.25, APIKey: "speech-test", Headers: `{"X-Speech":"{{api_key}}"}`}
			if provider == "http" {
				p.BodyTemplate = `{"text":"{{text}}","voice":"{{voice}}","speed":"{{speed}}"}`
			}
			got, format, err := FetchSpeech(context.Background(), HTTPClient(), p, "你好")
			if err != nil || format != "wav" || !bytes.Equal(got, wav) {
				t.Fatalf("speech response: format=%s err=%v", format, err)
			}
		})
	}
}

func TestConfiguredSpeechRejectsUnsupportedAudioAndErrors(t *testing.T) {
	for _, payload := range []string{"OggSunsupported", "fLaCunsupported", "not audio", `{"error":"echo-secret-test"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(payload, "{") {
				w.Header().Set("Content-Type", "application/json")
			}
			io.WriteString(w, payload)
		}))
		got, format, err := FetchSpeech(context.Background(), HTTPClient(), TTSService{Provider: "http", URL: server.URL}, "test")
		server.Close()
		if err == nil || got != nil || format != "" || strings.Contains(err.Error(), "echo-secret-test") {
			t.Fatalf("invalid audio accepted or response exposed: %s %v", format, err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, "echo-secret-test")
	}))
	defer server.Close()
	_, _, err := FetchSpeech(context.Background(), HTTPClient(), TTSService{Provider: "http", URL: server.URL}, "test")
	if err == nil || !strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "echo-secret-test") {
		t.Fatal("speech HTTP error exposed provider body")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = FetchSpeech(ctx, HTTPClient(), TTSService{Provider: "http", URL: server.URL}, "test")
	if err != context.Canceled {
		t.Fatal("speech ignored cancellation")
	}
	if _, _, err = FetchSpeech(context.Background(), HTTPClient(), TTSService{Provider: "http", URL: server.URL, BodyTemplate: `{"stream":true}`}, "test"); err == nil || !strings.Contains(err.Error(), "stream") {
		t.Fatal("streaming speech request accepted")
	}
}

func TestConfiguredSpeechAcceptsMP3WithoutTrustingContentType(t *testing.T) {
	mp3 := []byte("ID3mock-mp3-payload")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(mp3)
	}))
	defer server.Close()
	got, format, err := FetchSpeech(context.Background(), HTTPClient(), TTSService{Provider: "http", URL: server.URL}, "Hello")
	if err != nil || format != "mp3" || !bytes.Equal(got, mp3) {
		t.Fatalf("MP3 not detected: %s %v", format, err)
	}
}
