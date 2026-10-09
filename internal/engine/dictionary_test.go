package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDictionaryRequestAndDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer local-test" {
			t.Error("wrong dictionary request")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false || payload["stream"] != false {
			t.Error("thinking/stream flags not respected")
		}
		messages := payload["messages"].([]any)
		user := messages[1].(map[string]any)["content"].(string)
		if user != `{"word":"hello"}` || strings.Contains(user, "image") {
			t.Error("dictionary must send only selected word text")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "<think>internal</think>\n```json\n{\"word\":\"hello\",\"phonetic\":\"həˈləʊ\",\"definitions\":[\"int. 你好\"]}\n```"}}}})
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.BaseURL, cfg.Model, cfg.APIKey = server.URL+"/v1", "test", "local-test"
	entry, err := LookupDictionary(context.Background(), server.Client(), cfg, "hello")
	if err != nil || entry.Word != "hello" || len(entry.Definitions) != 1 || entry.Definitions[0] != "int. 你好" || entry.Source != "AI 词典" {
		t.Fatalf("entry=%+v error=%v", entry, err)
	}
}

func TestLocalDictionaryFormats(t *testing.T) {
	for _, tc := range []struct{ name, content, word, meaning string }{
		{"strings.json", `{"Hello":"你好"}`, "hello", "你好"},
		{"arrays.json", `{"hello":["你好","喂"]}`, "hello", "你好"},
		{"entries.json", `{"hello":{"word":"hello","phonetic":"həˈləʊ","definitions":["你好"]}}`, "hello", "你好"},
		{"ecdict.csv", "word,phonetic,definition,translation\nhello,həˈləʊ,greeting,int. 你好\\n喂\n", "HELLO", "int. 你好"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name)
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := DefaultConfig()
			cfg.Dictionary.Provider, cfg.Dictionary.LocalPath = "local", path
			entry, err := LookupDictionary(context.Background(), nil, cfg, tc.word)
			if err != nil || len(entry.Definitions) == 0 || entry.Definitions[0] != tc.meaning {
				t.Fatalf("entry=%+v error=%v", entry, err)
			}
			if _, err = LookupDictionary(context.Background(), nil, cfg, "missing"); err == nil {
				t.Fatal("missing local word must fail")
			}
		})
	}
}

func TestDictionaryRejectsInvalidEntriesAndInput(t *testing.T) {
	for _, content := range []string{`[]`, `{"definitions":[]}`, `{"definitions":[""]}`, `{"definitions":[7]}`, `{"definitions":["` + strings.Repeat("x", 8001) + `"]}`} {
		if _, err := decodeDictionary(content, "word", "test"); err == nil {
			t.Fatal("invalid dictionary accepted")
		}
	}
	for _, word := range []string{"", "first\nsecond", strings.Repeat("词", 151)} {
		if _, err := LookupDictionary(context.Background(), nil, DefaultConfig(), word); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LookupDictionary(ctx, nil, DefaultConfig(), "hello"); err != context.Canceled {
		t.Fatal("canceled lookup ignored context")
	}
}
