package engine

import (
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOCRMultipart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "" {
			t.Error("OCR must not carry translation credentials")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		if r.FormValue("return_word_box") != "True" {
			t.Error("missing coordinates request")
		}
		f, h, err := r.FormFile("image")
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		if h.Filename != "uploaded_image.jpg" || h.Header.Get("Content-Type") != "image/jpeg" {
			t.Error("wrong image MIME")
		}
		if _, err = jpeg.Decode(f); err != nil {
			t.Error(err)
		}
		w.Write([]byte(`[{"text":"Hello","box":[[1,2],[40,2],[40,20],[1,20]]}]`))
	}))
	defer server.Close()
	blocks, err := Recognize(context.Background(), HTTPClient(), server.URL, image.NewRGBA(image.Rect(0, 0, 64, 32)))
	if err != nil || len(blocks) != 1 || blocks[0].Text != "Hello" {
		t.Fatalf("%v %#v", err, blocks)
	}
}
func TestTranslationThinkingAndOrdering(t *testing.T) {
	for _, disable := range []bool{true, false} {
		t.Run(map[bool]string{true: "off", false: "default"}[disable], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-only" {
					t.Error("invalid route or auth")
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				extra, exists := body["chat_template_kwargs"]
				if disable {
					if !exists || extra.(map[string]any)["enable_thinking"] != false {
						t.Error("thinking was not disabled")
					}
				} else if exists {
					t.Error("thinking field must be omitted")
				}
				if body["stream"] != false {
					t.Error("nonstreaming expected")
				}
				encoded, _ := json.Marshal(body)
				if strings.Contains(string(encoded), "image_url") {
					t.Error("must send text only")
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "```json\n[{\"id\":1,\"text\":\"世界\"},{\"id\":0,\"text\":\"你好\"}]\n```"}}}})
			}))
			defer server.Close()
			lines := []Line{{ID: 0, Text: "Hello"}, {ID: 1, Text: "World"}}
			result, err := Translate(context.Background(), HTTPClient(), Config{BaseURL: server.URL + "/v1", Model: "test", APIKey: "test-only", DisableThinking: disable}, lines)
			if err != nil || len(result) != 2 || result[0].Text != "你好" {
				t.Fatalf("%v %#v", err, result)
			}
		})
	}
}
func TestMalformedResponses(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `[{"text":"x","box":[[1,2]]}]`, `[{"text":"x"}]`, `[{"text":"x","box":[[1,2],[3],[4,5],[6,7]]}]`} {
		if _, err := DecodeOCR([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if b, e := DecodeOCR([]byte(`[]`)); e != nil || len(b) != 0 {
		t.Error("empty OCR is valid")
	}
	lines := []Line{{ID: 0, Text: "A"}, {ID: 1, Text: "B"}}
	for _, raw := range []string{`[]`, `[{"id":0,"text":"x"},{"id":0,"text":"y"}]`, `[{"id":0,"text":"x"},{"id":8,"text":"y"}]`, `[{"id":0,"text":""},{"id":1,"text":"y"}]`} {
		if _, err := DecodeTranslation(raw, lines); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
func TestRedirectAndCancellation(t *testing.T) {
	leaked := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 307) }))
	defer server.Close()
	_, err := Translate(context.Background(), HTTPClient(), Config{BaseURL: server.URL, Model: "test", APIKey: "test"}, []Line{{ID: 0, Text: "x"}})
	if err == nil || leaked {
		t.Error("redirects must not forward credentials")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if _, err = Recognize(ctx, HTTPClient(), server.URL, image.NewRGBA(image.Rect(0, 0, 10, 10))); err == nil {
		t.Error("cancelled operation succeeded")
	}
}
