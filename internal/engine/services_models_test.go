package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestModelsEndpointSupportsOriginsPrefixesAndCompleteEndpoints(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"https://service.example", "https://service.example/v1/models"},
		{" https://service.example/ ", "https://service.example/v1/models"},
		{"http://127.0.0.1:11434?deployment=test", "http://127.0.0.1:11434/v1/models?deployment=test"},
		{"https://service.example/v1/", "https://service.example/v1/models"},
		{"https://service.example/proxy/openai/?api-version=2026", "https://service.example/proxy/openai/models?api-version=2026"},
		{"https://service.example/v1/chat/completions/?deployment=test", "https://service.example/v1/models?deployment=test"},
		{"https://service.example/chat/completions", "https://service.example/models"},
		{"https://service.example/custom/audio/speech/?deployment=test", "https://service.example/custom/models?deployment=test"},
		{"https://service.example/audio/speech", "https://service.example/models"},
		{"https://service.example/v1/models/?deployment=test", "https://service.example/v1/models?deployment=test"},
	} {
		if got := modelsEndpoint(tc.base); got != tc.want {
			t.Errorf("base %q: got %q, want %q", tc.base, got, tc.want)
		}
	}
}

func TestListModelsUsesSelectedProfileWithoutConfiguredModel(t *testing.T) {
	for _, kind := range []string{"translation", "ocr", "tts"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/v1/models" || r.URL.Query().Get("deployment") != "test" {
					t.Error("model request lost GET method, API path, or query")
				}
				if r.Header.Get("Authorization") != "Bearer model-catalog-test-key" || r.Header.Get("X-Catalog") != "draft" || r.Header.Get("X-Key") != "model-catalog-test-key" || r.Header.Get("Accept") != "application/json" {
					t.Error("model request lost credentials or custom headers")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || len(body) != 0 {
					t.Error("model query unexpectedly sent a request body")
				}
				json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{map[string]string{"id": " z-model "}, map[string]string{"id": "a-model"}, map[string]string{"id": "a-model"}, map[string]string{"id": " "}}})
			}))
			defer server.Close()
			const headers = `{"X-Catalog":"draft","X-Key":"{{api_key}}"}`
			cfg := Config{
				TranslationServices: []TranslationService{{ID: "unused", Provider: "http", BaseURL: "invalid"}, {ID: "selected", Provider: "openai", BaseURL: server.URL + "?deployment=test", APIKey: "model-catalog-test-key", Headers: headers}}, ActiveTranslationID: "selected",
				OCRServices: []OCRService{{ID: "unused", Provider: "multipart", URL: "invalid"}, {ID: "selected", Provider: "openai", URL: server.URL + "/v1/chat/completions?deployment=test", APIKey: "model-catalog-test-key", Headers: headers}}, ActiveOCRID: "selected",
				TTSServices: []TTSService{{ID: "unused", Provider: "system"}, {ID: "selected", Provider: "openai", URL: server.URL + "/v1/audio/speech?deployment=test", APIKey: "model-catalog-test-key", Headers: headers}}, ActiveTTSID: "selected",
			}
			got, err := ListModels(context.Background(), server.Client(), cfg, kind)
			if err != nil || !reflect.DeepEqual(got, []string{"a-model", "z-model"}) || calls != 1 {
				t.Fatalf("models=%v calls=%d err=%v", got, calls, err)
			}
		})
	}
}

func TestListModelsAllowsCustomAuthorizationAndUnauthenticatedLocalService(t *testing.T) {
	for _, authorization := range []string{"", "Custom catalog-credential"} {
		t.Run(authorization, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != authorization {
					t.Error("unexpected Authorization header")
				}
				io.WriteString(w, `{"data":[{"id":"local-model"}]}`)
			}))
			defer server.Close()
			headers := "{}"
			if authorization != "" {
				headers = `{"Authorization":"Custom catalog-credential"}`
			}
			cfg := Config{TranslationServices: []TranslationService{{Provider: "openai", BaseURL: server.URL, Headers: headers}}}
			got, err := ListModels(context.Background(), server.Client(), cfg, "translation")
			if err != nil || !reflect.DeepEqual(got, []string{"local-model"}) {
				t.Fatalf("models=%v err=%v", got, err)
			}
		})
	}
}

func TestListModelsRejectsMalformedAndEmptyCatalogsWithoutEchoingBody(t *testing.T) {
	for _, body := range []string{
		`<!DOCTYPE html><html>provider-secret</html>`,
		`not JSON provider-secret`,
		`{"data":[{"id":"valid"}]} trailing-provider-secret`,
		`[{"id":"provider-secret"}]`,
		`{"models":[{"id":"provider-secret"}]}`,
		`{"data":null}`,
		`{"data":"provider-secret"}`,
		`{"data":[null]}`,
		`{"data":[{"id":123}]}`,
		`{"data":[{}]}`,
		`{"data":[]}`,
		`{"data":[{"id":"  "}]}`,
		`{"data":[{"id":"provider-secret\u0000"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer server.Close()
			cfg := Config{BaseURL: server.URL}
			got, err := ListModels(context.Background(), server.Client(), cfg, "translation")
			if err == nil || got != nil || strings.Contains(err.Error(), "provider-secret") {
				t.Fatalf("invalid response was accepted or exposed: models=%v err=%v", got, err)
			}
		})
	}
}

func TestListModelsRejectsUnsupportedOrInvalidServiceBeforeRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, `{"data":[{"id":"test"}]}`) }))
	defer server.Close()
	for _, tc := range []struct {
		kind string
		cfg  Config
	}{
		{"dictionary", Config{BaseURL: server.URL}},
		{"translation", Config{TranslationServices: []TranslationService{{Provider: "http", BaseURL: server.URL}}}},
		{"ocr", Config{OCRServices: []OCRService{{Provider: "multipart", URL: server.URL}}}},
		{"tts", Config{TTSServices: []TTSService{{Provider: "system", URL: server.URL}}}},
		{"translation", Config{}},
		{"translation", Config{BaseURL: "https://username:password@service.example"}},
		{"translation", Config{TranslationServices: []TranslationService{{BaseURL: server.URL, TimeoutMS: 999}}}},
		{"translation", Config{TranslationServices: []TranslationService{{BaseURL: server.URL, TimeoutMS: 600001}}}},
		{"translation", Config{TranslationServices: []TranslationService{{BaseURL: server.URL, Headers: `{"X-Bad":"value\nsecret"}`}}}},
		{"translation", Config{TranslationServices: []TranslationService{{BaseURL: server.URL, Headers: `{"X-Bad":"{{unknown}}"}`}}}},
	} {
		if _, err := ListModels(context.Background(), server.Client(), tc.cfg, tc.kind); err == nil {
			t.Errorf("unsupported or invalid %s service was accepted", tc.kind)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid configurations issued %d requests", calls)
	}
}

func TestListModelsDoesNotFollowRedirectsEvenWithCallerClient(t *testing.T) {
	destinationCalls := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls++
		io.WriteString(w, `{"data":[{"id":"redirected"}]}`)
	}))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer server.Close()
	client := server.Client()
	cfg := Config{BaseURL: server.URL, APIKey: "redirect-test-key"}
	if _, err := ListModels(context.Background(), client, cfg, "translation"); err == nil {
		t.Fatal("model query followed redirect")
	}
	if destinationCalls != 0 || client.CheckRedirect != nil {
		t.Fatal("redirect reached destination or mutated caller client")
	}
}

func TestListModelsBoundsResponseAndHonorsContext(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.CopyN(w, strings.NewReader(strings.Repeat("x", maxResponse+1)), maxResponse+1)
		}))
		defer server.Close()
		if _, err := ListModels(context.Background(), server.Client(), Config{BaseURL: server.URL}, "translation"); err == nil || !strings.Contains(err.Error(), "过大") {
			t.Fatalf("response limit was not enforced: %v", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if _, err := ListModels(ctx, server.Client(), Config{BaseURL: server.URL}, "translation"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("caller deadline was not honored: %v", err)
		}
	})
}

func TestListModelsExplainsCatalogHTTPStatusWithoutExposingBodyOrURL(t *testing.T) {
	for _, tc := range []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "API 密钥"},
		{http.StatusForbidden, "访问权限"},
		{http.StatusNotFound, "手动填写"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, "provider-secret")
			}))
			defer server.Close()
			cfg := Config{BaseURL: server.URL + "?private=secret-query"}
			if _, err := ListModels(context.Background(), server.Client(), cfg, "translation"); err == nil || !strings.Contains(err.Error(), tc.fragment) || strings.Contains(err.Error(), "provider-secret") || strings.Contains(err.Error(), "secret-query") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("HTTP error was not safe or actionable: %v", err)
			}
		})
	}
}

type modelCatalogTransport func(*http.Request) (*http.Response, error)

func (f modelCatalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestListModelsEnforcesConfiguredDeadline(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: modelCatalogTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		deadline, ok := r.Context().Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining <= 0 || remaining > 1500*time.Millisecond {
			t.Error("model query did not apply the configured service deadline")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"deadline-test"}]}`)), Header: http.Header{}}, nil
	})}
	cfg := Config{TranslationServices: []TranslationService{{BaseURL: "https://service.example", TimeoutMS: 1000}}}
	got, err := ListModels(context.Background(), client, cfg, "translation")
	if err != nil || calls != 1 || !reflect.DeepEqual(got, []string{"deadline-test"}) {
		t.Fatalf("models=%v calls=%d err=%v", got, calls, err)
	}
}

func TestModelIDsRejectsOversizedCatalogAndNames(t *testing.T) {
	for _, value := range []any{
		map[string]any{"data": make([]any, 10001)},
		map[string]any{"data": []any{map[string]any{"id": strings.Repeat("x", 4097)}}},
	} {
		if _, err := modelIDs(value); err == nil {
			t.Fatal("oversized model catalog or ID was accepted")
		}
	}
}
