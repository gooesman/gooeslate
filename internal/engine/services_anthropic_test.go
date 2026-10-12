package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAnthropicStandardTranslationAndTextBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" || r.URL.Query().Get("gateway") != "test" {
			t.Error("Anthropic Messages endpoint or query was altered")
		}
		if r.Header.Get("x-api-key") != "anthropic-test-key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("Anthropic authentication defaults incorrect")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if body["model"] != "claude-test" || body["max_tokens"] != float64(8192) || body["stream"] != false || body["system"] != "Translate to 简体中文." {
			t.Error("missing Anthropic standard options")
		}
		for _, field := range []string{"temperature", "top_p", "top_k", "chat_template_kwargs", "group"} {
			if _, exists := body[field]; exists {
				t.Errorf("unexpected standard field %s", field)
			}
		}
		if !reflect.DeepEqual(body["thinking"], map[string]any{"type": "disabled"}) {
			t.Error("Anthropic disable-thinking request incorrect")
		}
		messages, ok := body["messages"].([]any)
		if !ok || len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" || messages[0].(map[string]any)["content"] != `[{"id":42,"text":"Hello"},{"id":7,"text":"World"}]` {
			t.Error("system role sent in Anthropic messages, or source IDs/text changed")
		}
		// Split one JSON result over multiple text blocks, with non-text blocks
		// between them. Translation IDs deliberately arrive in a different order.
		json.NewEncoder(w).Encode(map[string]any{"type": "message", "stop_reason": "end_turn", "content": []any{
			map[string]any{"type": "thinking", "thinking": "ignore this", "text": "invalid-decoy"},
			map[string]any{"type": "text", "text": `[{"id":7,"text":"世界"},`},
			map[string]any{"type": "tool_use", "text": "invalid-decoy", "input": map[string]any{}},
			map[string]any{"type": "text", "text": `{"id":42,"text":"你好"}]`},
		}})
	}))
	defer server.Close()
	c := Config{Language: "auto", APIKey: "unused-legacy-key", TranslationServices: []TranslationService{{ID: "anthropic", Provider: "anthropic", BaseURL: server.URL + "?gateway=test", Model: "claude-test", APIKey: "anthropic-test-key", MaxTokens: 8192, Temperature: 0.2, DisableThinking: true, SystemPrompt: "Translate to {{target_language}}.", UserPrompt: "{{lines}}"}}}
	lines := []Line{{ID: 42, Text: "Hello"}, {ID: 7, Text: "World"}}
	before := append([]Line(nil), lines...)
	got, err := Translate(context.Background(), HTTPClient(), c, lines)
	if err != nil || !reflect.DeepEqual(got, []Translation{{ID: 42, Text: "你好"}, {ID: 7, Text: "世界"}}) {
		t.Fatalf("Anthropic text translation: %+v %v", got, err)
	}
	if !reflect.DeepEqual(lines, before) {
		t.Fatal("source lines were changed")
	}
}

func TestAnthropicDefaultModelDictionary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/claude/v1/messages" || r.Header.Get("x-api-key") != "dictionary-test" || r.Header.Get("Authorization") != "" {
			t.Error("dictionary did not reuse Anthropic endpoint/authentication")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["model"] != "dictionary-model" || body["max_tokens"] != float64(4096) || !strings.HasPrefix(body["system"].(string), "Act as a concise dictionary.") {
			t.Error("dictionary did not reuse payload or its own system prompt")
		}
		messages := body["messages"].([]any)
		if len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" || messages[0].(map[string]any)["content"] != `{"word":"Mappings"}` {
			t.Error("dictionary input or roles incorrect")
		}
		if _, exists := body["thinking"]; exists {
			t.Error("disabled switch added an unsolicited thinking policy")
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []any{
			map[string]any{"type": "redacted_thinking", "data": "ignore"},
			map[string]any{"type": "text", "text": `{"word":"mapping","phonetic":"/ˈmæpɪŋ/","definitions":["n. 映射"]}`},
		}})
	}))
	defer server.Close()
	c := Config{Language: "简体中文", Dictionary: DictionaryConfig{Provider: "openai"}, TranslationServices: []TranslationService{{Provider: "anthropic", BaseURL: server.URL + "/claude/v1/messages", Model: "dictionary-model", APIKey: "dictionary-test"}}}
	entry, err := LookupDictionary(context.Background(), HTTPClient(), c, "Mappings")
	if err != nil || entry.Word != "mapping" || entry.Phonetic != "/ˈmæpɪŋ/" || !reflect.DeepEqual(entry.Definitions, []string{"n. 映射"}) || entry.Source != "AI 词典" {
		t.Fatalf("Anthropic dictionary: %+v %v", entry, err)
	}
}

func TestAnthropicCustomRequestPreservesUserControl(t *testing.T) {
	for _, disable := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve-thinking", true: "disable-thinking"}[disable], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bridge/messages" || r.URL.Query().Get("route") != "claude" || r.Header.Get("x-api-key") != "override-key" || r.Header.Get("anthropic-version") != "gateway-version" || r.Header.Get("Authorization") != "Bearer explicit-gateway-test" || r.Header.Get("X-Profile") != "profile-key" {
					t.Error("custom Anthropic endpoint/header overrides not respected")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["model"] != "manual-model" || body["max_tokens"] != float64(2048) || body["temperature"] != 0.4 || body["system"] != "Target: 英语" || body["configured_limit"] != float64(8192) || body["prompt_copy"] != `你好 "引用"` || body["extra"] != "retained" {
					t.Error("full custom body was replaced or typed variables lost")
				}
				messages := body["messages"].([]any)
				if len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" || messages[0].(map[string]any)["content"] != `你好 "引用"` {
					t.Error("custom Anthropic messages contain a system role or lost escaping")
				}
				wantThinking := map[string]any{"type": "enabled", "budget_tokens": float64(1024)}
				if disable {
					wantThinking = map[string]any{"type": "disabled"}
				}
				if !reflect.DeepEqual(body["thinking"], wantThinking) {
					t.Error("thinking toggle did not preserve or disable custom thinking correctly")
				}
				if _, exists := body["chat_template_kwargs"]; exists {
					t.Error("Qwen-only thinking parameter sent to Anthropic")
				}
				json.NewEncoder(w).Encode(map[string]any{"result": []Translation{{ID: 31, Text: "Hello"}}})
			}))
			defer server.Close()
			c := Config{Language: "auto", TranslationServices: []TranslationService{{Provider: "anthropic", RequestMode: "custom", BaseURL: server.URL + "/bridge/messages?route=claude", Model: "ignored-profile-model", APIKey: "profile-key", MaxTokens: 8192, DisableThinking: disable, SystemPrompt: "Target: {{target_language}}", UserPrompt: "{{text}}", Headers: `{"x-api-key":"override-key","anthropic-version":"gateway-version","Authorization":"Bearer explicit-gateway-test","X-Profile":"{{api_key}}"}`, BodyTemplate: `{"model":"manual-model","max_tokens":2048,"configured_limit":"{{max_tokens}}","system":"{{system}}","messages":"{{messages}}","prompt_copy":"{{user_prompt}}","temperature":0.4,"stream":false,"thinking":{"type":"enabled","budget_tokens":1024},"extra":"retained"}`, ResponsePath: "result"}}}
			if err := ValidateServiceConfig(c); err != nil {
				t.Fatal(err)
			}
			got, err := Translate(context.Background(), HTTPClient(), c, []Line{{ID: 31, Text: `你好 "引用"`}})
			if err != nil || !reflect.DeepEqual(got, []Translation{{ID: 31, Text: "Hello"}}) {
				t.Fatalf("custom Anthropic translation: %+v %v", got, err)
			}
		})
	}
}

func TestAnthropicCustomGatewayGroupAndConversation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != "gateway-test-key" || r.Header.Get("Authorization") != "" {
			t.Error("custom gateway request lost Anthropic routing or authentication")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["model"] != "Fable" || body["group"] != "vip" || body["max_tokens"] != float64(4096) || body["stream"] != false || body["system"] != "Target: 简体中文" {
			t.Error("custom gateway model/group/system/output limit was changed")
		}
		wantMessages := []any{
			map[string]any{"role": "user", "content": "previous question"},
			map[string]any{"role": "assistant", "content": "previous answer"},
			map[string]any{"role": "user", "content": `[{"id":8,"text":"Hello"}]`},
		}
		if !reflect.DeepEqual(body["messages"], wantMessages) {
			t.Error("custom user/assistant history or current source lines were overwritten")
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": `[{"id":8,"text":"你好"}]`}}, "stop_reason": "end_turn"})
	}))
	defer server.Close()
	c := Config{Language: "简体中文", TranslationServices: []TranslationService{{Provider: "anthropic", BaseURL: server.URL, APIKey: "gateway-test-key", Model: "Fable", RequestMode: "custom", SystemPrompt: "Target: {{target_language}}", UserPrompt: "{{lines}}", BodyTemplate: `{"model":"{{model}}","group":"vip","max_tokens":"{{max_tokens}}","system":"{{system}}","messages":[{"role":"user","content":"previous question"},{"role":"assistant","content":"previous answer"},{"role":"user","content":"{{user_prompt}}"}],"stream":false}`}}}
	if err := ValidateServiceConfig(c); err != nil {
		t.Fatal(err)
	}
	got, err := Translate(context.Background(), HTTPClient(), c, []Line{{ID: 8, Text: "Hello"}})
	if err != nil || !reflect.DeepEqual(got, []Translation{{ID: 8, Text: "你好"}}) {
		t.Fatalf("custom gateway translation: %+v %v", got, err)
	}
}

func TestAnthropicResponseFailuresAreSafe(t *testing.T) {
	for _, tc := range []struct {
		name, body, part string
		status           int
	}{
		{"truncated", `{"content":[{"type":"text","text":"provider-secret-test"}],"stop_reason":"max_tokens"}`, "最大长度", 200},
		{"no-text", `{"content":[{"type":"thinking","thinking":"provider-secret-test"}]}`, "没有返回可用文字", 200},
		{"empty", `{"content":[]}`, "没有返回可用文字", 200},
		{"missing", `{"data":"provider-secret-test"}`, "缺少文字内容", 200},
		{"invalid-content", `{"content":"provider-secret-test"}`, "内容块数组", 200},
		{"invalid-text", `{"content":[{"type":"text","text":{"secret":"provider-secret-test"}}]}`, "文字内容格式无效", 200},
		{"error-envelope", `{"type":"error","error":{"message":"provider-secret-test"}}`, "服务返回错误", 200},
		{"html", `<html>provider-secret-test</html>`, "HTML", 200},
		{"json-trailing", `{} {"secret":"provider-secret-test"}`, "多余内容", 200},
		{"http-status", `{"error":{"message":"provider-secret-test"}}`, "429", 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			c := Config{TranslationServices: []TranslationService{{Provider: "anthropic", BaseURL: server.URL, Model: "test", APIKey: "client-secret-test"}}}
			_, err := Translate(context.Background(), HTTPClient(), c, []Line{{ID: 0, Text: "Hello"}})
			if err == nil || !strings.Contains(err.Error(), tc.part) || strings.Contains(err.Error(), "secret-test") {
				t.Fatalf("wrong or unsafe Anthropic error: %v", err)
			}
		})
	}
}

func TestAnthropicResponsePathAppliesToBothRequestModes(t *testing.T) {
	for _, mode := range []string{"standard", "custom"} {
		for _, path := range []string{"gateway.reply", "$.gateway.reply"} {
			t.Run(mode+"-"+path, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					json.NewEncoder(w).Encode(map[string]any{
						"content": []any{map[string]any{"type": "text", "text": "decoy content must not be translated"}},
						"gateway": map[string]any{"reply": []Translation{{ID: 27, Text: "你好"}}},
					})
				}))
				defer server.Close()
				c := Config{TranslationServices: []TranslationService{{Provider: "anthropic", BaseURL: server.URL, Model: "test", RequestMode: mode, BodyTemplate: `{"input":"{{text}}","stream":false}`, ResponsePath: path}}}
				got, err := Translate(context.Background(), HTTPClient(), c, []Line{{ID: 27, Text: "Hello"}})
				if err != nil || !reflect.DeepEqual(got, []Translation{{ID: 27, Text: "你好"}}) {
					t.Fatalf("configured Anthropic response path ignored: %+v %v", got, err)
				}
			})
		}
	}
}

func TestAnthropicConfigLimitsAndCustomStreamRejection(t *testing.T) {
	c := NormalizeConfig(Config{TranslationServices: []TranslationService{{Provider: "anthropic"}}})
	p := c.TranslationServices[0]
	if p.MaxTokens != 4096 || p.ResponsePath != "content" || p.RequestMode != "standard" {
		t.Fatal("Anthropic defaults incomplete")
	}
	if err := ValidateServiceConfig(c); err != nil {
		t.Fatal(err)
	}
	for _, tokens := range []int{-1, 1000001} {
		c.TranslationServices[0].MaxTokens = tokens
		if err := ValidateServiceConfig(c); err == nil || !strings.Contains(err.Error(), "最大输出长度") {
			t.Fatalf("invalid output limit %d accepted: %v", tokens, err)
		}
	}
	for _, tokens := range []int{1, 4096, 1000000} {
		c.TranslationServices[0].MaxTokens = tokens
		if err := ValidateServiceConfig(c); err != nil {
			t.Fatal(err)
		}
	}
	var saved Config
	raw, _ := json.Marshal(c)
	if err := json.Unmarshal(raw, &saved); err != nil || saved.TranslationServices[0].MaxTokens != 1000000 {
		t.Fatal("output limit did not round-trip in saved/imported configuration")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unsupported streaming request reached service")
	}))
	defer server.Close()
	c.TranslationServices[0] = TranslationService{Provider: "anthropic", BaseURL: server.URL, RequestMode: "custom", BodyTemplate: `{"stream":true}`}
	if err := ValidateServiceConfig(c); err == nil || !strings.Contains(err.Error(), "stream") {
		t.Fatal("streaming custom body was saved")
	}
	_, err := Translate(context.Background(), HTTPClient(), c, []Line{{ID: 0, Text: "Hello"}})
	if err == nil || !strings.Contains(err.Error(), "stream") {
		t.Fatal("streaming custom body was sent")
	}
}

func TestAnthropicDeadlineAndCancellation(t *testing.T) {
	client := HTTPClient()
	client.Transport = serviceTestTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if remaining := time.Until(deadline); !ok || remaining < 110*time.Second || remaining > 121*time.Second {
			t.Error("Anthropic profile timeout ignored")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"content":[{"type":"text","text":"[{\"id\":0,\"text\":\"你好\"}]"}]}`))}, nil
	})
	c := Config{TranslationServices: []TranslationService{{Provider: "anthropic", BaseURL: "https://service.example/v1", Model: "test", TimeoutMS: 120000}}}
	if _, err := Translate(context.Background(), client, c, []Line{{ID: 0, Text: "Hello"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Translate(ctx, HTTPClient(), c, []Line{{ID: 0, Text: "Hello"}})
	if err != context.Canceled {
		t.Fatalf("Anthropic cancellation lost: %v", err)
	}
}
