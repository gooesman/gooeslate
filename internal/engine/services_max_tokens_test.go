package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestOpenAIConfiguredMaxTokensTranslationAndDictionary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tokens int
	}{{"legacy-service-default", 0}, {"Opus-1024", 1024}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer max-output-test-key" || r.Header.Get("X-Api-Key") != "" {
					t.Error("OpenAI route or authentication changed")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["model"] != "Opus" || body["stream"] != false {
					t.Error("OpenAI model or non-streaming behavior changed")
				}
				value, exists := body["max_tokens"]
				if tc.tokens == 0 {
					if exists {
						t.Error("old zero output limit changed the existing API request")
					}
				} else if !exists || value != float64(tc.tokens) {
					t.Error("configured output limit not sent")
				}
				messages := body["messages"].([]any)
				if len(messages) != 2 || messages[0].(map[string]any)["role"] != "system" || messages[1].(map[string]any)["role"] != "user" {
					t.Error("OpenAI messages format changed")
				}
				content := `[{"id":3,"text":"你好"}]`
				if strings.HasPrefix(messages[0].(map[string]any)["content"].(string), "Act as a concise dictionary.") {
					content = `{"word":"mapping","phonetic":"","definitions":["n. 映射"]}`
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
			}))
			defer server.Close()
			c := Config{Language: "简体中文", Dictionary: DictionaryConfig{Provider: "openai"}, TranslationServices: []TranslationService{{Provider: "openai", BaseURL: server.URL, Model: "Opus", APIKey: "max-output-test-key", MaxTokens: tc.tokens}}}
			if p := ActiveTranslation(c); p.MaxTokens != tc.tokens {
				t.Fatal("OpenAI output limit changed during normalization")
			}
			got, err := Translate(context.Background(), HTTPClient(), c, []Line{{ID: 3, Text: "Hello"}})
			if err != nil || !reflect.DeepEqual(got, []Translation{{ID: 3, Text: "你好"}}) {
				t.Fatalf("OpenAI translation: %+v %v", got, err)
			}
			entry, err := LookupDictionary(context.Background(), HTTPClient(), c, "mapping")
			if err != nil || entry.Word != "mapping" || !reflect.DeepEqual(entry.Definitions, []string{"n. 映射"}) || calls != 2 {
				t.Fatalf("OpenAI dictionary: %+v calls=%d %v", entry, calls, err)
			}
		})
	}
}

func TestOpenAIOutputLimitCustomBodyRemainsAuthoritative(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		maxTokens  any
	}{{"omitted", `{"input":"{{text}}","stream":false}`, nil}, {"explicit", `{"input":"{{text}}","max_tokens":2048,"stream":false}`, float64(2048)}, {"variable", `{"input":"{{text}}","max_tokens":"{{max_tokens}}","stream":false}`, float64(1024)}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				value, exists := body["max_tokens"]
				if tc.maxTokens == nil {
					if exists {
						t.Error("output limit injected into complete custom body")
					}
				} else if value != tc.maxTokens {
					t.Error("complete custom output limit was overwritten")
				}
				json.NewEncoder(w).Encode(map[string]any{"result": []Translation{{ID: 9, Text: "你好"}}})
			}))
			defer server.Close()
			c := Config{TranslationServices: []TranslationService{{Provider: "openai", BaseURL: server.URL, Model: "Opus", MaxTokens: 1024, RequestMode: "custom", BodyTemplate: tc.body, ResponsePath: "result"}}}
			got, err := Translate(context.Background(), HTTPClient(), c, []Line{{ID: 9, Text: "Hello"}})
			if err != nil || !reflect.DeepEqual(got, []Translation{{ID: 9, Text: "你好"}}) {
				t.Fatalf("complete custom request: %+v %v", got, err)
			}
		})
	}
}

func TestTranslationMaxTokensProviderDefaultsAndValidation(t *testing.T) {
	if p := ActiveTranslation(DefaultConfig()); p.MaxTokens != 0 {
		t.Fatal("new OpenAI defaults must leave the limit to the API")
	}
	legacy := Config{BaseURL: "https://service.example/v1", Model: "Opus"}
	if p := ActiveTranslation(legacy); p.MaxTokens != 0 {
		t.Fatal("legacy OpenAI service acquired a new output cap")
	}
	for _, provider := range []string{"openai", "http", "anthropic"} {
		c := Config{TranslationServices: []TranslationService{{Provider: provider}}}
		want := 0
		if provider == "anthropic" {
			want = 4096
		}
		if p := ActiveTranslation(c); p.MaxTokens != want {
			t.Fatalf("%s normalized to wrong limit %d", provider, p.MaxTokens)
		}
		for _, tokens := range []int{0, 1, 1024, 1000000} {
			c.TranslationServices[0].MaxTokens = tokens
			if err := ValidateServiceConfig(c); err != nil {
				t.Fatalf("%s tokens %d: %v", provider, tokens, err)
			}
		}
		for _, tokens := range []int{-1, 1000001} {
			c.TranslationServices[0].MaxTokens = tokens
			if err := ValidateServiceConfig(c); err == nil || !strings.Contains(err.Error(), "最大输出长度") {
				t.Fatalf("%s tokens %d accepted: %v", provider, tokens, err)
			}
		}
	}
}

func TestModelListHeadersUseConfiguredOutputLimit(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("X-Output-Limit") != "tokens=8192" {
					t.Error("model catalog header variables did not use the selected profile")
				}
				json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "test-model"}}, "has_more": false})
			}))
			defer server.Close()
			c := Config{TranslationServices: []TranslationService{{Provider: provider, BaseURL: server.URL, MaxTokens: 8192, Headers: `{"X-Output-Limit":"tokens={{max_tokens}}"}`}}}
			got, err := ListModels(context.Background(), HTTPClient(), c, "translation")
			if err != nil || !reflect.DeepEqual(got, []string{"test-model"}) {
				t.Fatalf("catalog vars: %+v %v", got, err)
			}
		})
	}
}
