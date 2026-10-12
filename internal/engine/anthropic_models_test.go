package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func anthropicCatalogConfig(endpoint, key, headers string) Config {
	return Config{TranslationServices: []TranslationService{
		{ID: "unused", Provider: "http", BaseURL: "invalid"},
		{ID: "catalog", Provider: "anthropic", BaseURL: endpoint, APIKey: key, Headers: headers},
	}, ActiveTranslationID: "catalog"}
}

func TestAnthropicListModelsEndpointAndAuthentication(t *testing.T) {
	for _, tc := range []struct{ suffix, path string }{
		{"", "/v1/models"},
		{"/", "/v1/models"},
		{"/v1/", "/v1/models"},
		{"/proxy/claude", "/proxy/claude/models"},
		{"/v1/messages/", "/v1/models"},
		{"/messages", "/models"},
		{"/v1/models/", "/v1/models"},
	} {
		t.Run(tc.suffix, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != tc.path || r.URL.Query().Get("deployment") != "catalog" || r.URL.Query().Get("limit") != "1000" {
					t.Error("catalog request lost method, base path, or query")
				}
				if r.Header.Get("X-Api-Key") != "test-anthropic-key" || r.Header.Get("Anthropic-Version") != "2023-06-01" || r.Header.Get("Authorization") != "" || r.Header.Get("Accept") != "application/json" || r.Header.Get("X-Custom") != "test-anthropic-key" {
					t.Error("catalog request has incorrect default authentication or headers")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || len(body) != 0 {
					t.Error("GET catalog unexpectedly sent a request body")
				}
				io.WriteString(w, `{"data":[{"id":" claude-z "},{"id":"claude-a"},{"id":"claude-a"}],"has_more":false,"last_id":"claude-a"}`)
			}))
			defer server.Close()
			cfg := anthropicCatalogConfig(server.URL+tc.suffix+"?deployment=catalog", "test-anthropic-key", `{"X-Custom":"{{api_key}}"}`)
			models, err := ListModels(context.Background(), server.Client(), cfg, "translation")
			if err != nil || calls != 1 || !reflect.DeepEqual(models, []string{"claude-a", "claude-z"}) {
				t.Fatalf("models=%v calls=%d err=%v", models, calls, err)
			}
		})
	}
}

func TestAnthropicListModelsAllowsHeaderOverridesAndUnauthenticatedService(t *testing.T) {
	for _, tc := range []struct{ key, headers, wantKey, wantVersion, wantAuth, wantAccept string }{
		{"", "{}", "", "2023-06-01", "", "application/json"},
		{"default", `{"x-api-key":"overridden","anthropic-version":"custom-version","Authorization":"Custom catalog-credential","Accept":"application/vnd.catalog+json"}`, "overridden", "custom-version", "Custom catalog-credential", "application/vnd.catalog+json"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Api-Key") != tc.wantKey || r.Header.Get("Anthropic-Version") != tc.wantVersion || r.Header.Get("Authorization") != tc.wantAuth || r.Header.Get("Accept") != tc.wantAccept {
					t.Error("explicit headers did not override the Anthropic defaults")
				}
				// Compatibility services may omit official pagination metadata.
				io.WriteString(w, `{"data":[{"id":"local-claude"}]}`)
			}))
			defer server.Close()
			models, err := ListModels(context.Background(), server.Client(), anthropicCatalogConfig(server.URL, tc.key, tc.headers), "translation")
			if err != nil || !reflect.DeepEqual(models, []string{"local-claude"}) {
				t.Fatalf("models=%v err=%v", models, err)
			}
		})
	}
}

func TestAnthropicListModelsFollowsPaginationAndDeduplicates(t *testing.T) {
	for _, emptyLastPage := range []bool{false, true} {
		t.Run(fmt.Sprint(emptyLastPage), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Query().Get("deployment") != "test" || r.URL.Query().Get("limit") != "1000" || r.Header.Get("X-Api-Key") != "pagination-test-key" {
					t.Error("pagination lost configuration")
				}
				switch calls {
				case 1:
					if r.URL.Query().Get("after_id") != "" || r.URL.Query().Get("before_id") != "starting-cursor" {
						t.Error("first page lost supplied pagination query")
					}
					io.WriteString(w, `{"data":[{"id":"claude-z"},{"id":"claude-a"}],"has_more":true,"last_id":"cursor-one"}`)
				case 2:
					if r.URL.Query().Get("after_id") != "cursor-one" || r.URL.Query().Get("before_id") != "" {
						t.Error("second page did not use last_id as after_id")
					}
					if emptyLastPage {
						io.WriteString(w, `{"data":[{"id":"claude-z"},{"id":"claude-m"}],"has_more":true,"last_id":"cursor-two"}`)
					} else {
						io.WriteString(w, `{"data":[{"id":"claude-z"},{"id":"claude-m"}],"has_more":false}`)
					}
				case 3:
					if !emptyLastPage || r.URL.Query().Get("after_id") != "cursor-two" {
						t.Error("unexpected extra catalog page")
					}
					io.WriteString(w, `{"data":[],"has_more":false}`)
				default:
					t.Error("catalog made unexpected requests")
				}
			}))
			defer server.Close()
			models, err := ListModels(context.Background(), server.Client(), anthropicCatalogConfig(server.URL+"/v1/messages?deployment=test&before_id=starting-cursor", "pagination-test-key", "{}"), "translation")
			wantCalls := 2
			if emptyLastPage {
				wantCalls = 3
			}
			if err != nil || calls != wantCalls || !reflect.DeepEqual(models, []string{"claude-a", "claude-m", "claude-z"}) {
				t.Fatalf("models=%v calls=%d err=%v", models, calls, err)
			}
		})
	}
}

func TestAnthropicListModelsRejectsMalformedCatalogAndPaginationSafely(t *testing.T) {
	for _, body := range []string{
		`<!DOCTYPE html>catalog-secret`,
		`not JSON catalog-secret`,
		`{"data":[{"id":"valid"}]} catalog-secret`,
		`[]`,
		`{"data":null}`,
		`{"data":[null]}`,
		`{"data":[{"id":7}]}`,
		`{"data":[{"id":"catalog-secret\u0000"}]}`,
		`{"data":[]}`,
		`{"data":[{"id":"valid"}],"has_more":"catalog-secret"}`,
		`{"data":[{"id":"valid"}],"has_more":true}`,
		`{"data":[{"id":"valid"}],"has_more":true,"last_id":7}`,
		`{"data":[{"id":"valid"}],"has_more":true,"last_id":" "}`,
		`{"data":[{"id":"valid"}],"has_more":true,"last_id":"catalog-secret\u0000"}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer server.Close()
			models, err := ListModels(context.Background(), server.Client(), anthropicCatalogConfig(server.URL+"?private=secret-query", "catalog-key", "{}"), "translation")
			if err == nil || models != nil || strings.Contains(err.Error(), "catalog-secret") || strings.Contains(err.Error(), "secret-query") || strings.Contains(err.Error(), "catalog-key") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("malformed catalog accepted or leaked details: models=%v err=%v", models, err)
			}
		})
	}
}

func TestAnthropicListModelsBoundsPagination(t *testing.T) {
	t.Run("repeated cursor", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			io.WriteString(w, `{"data":[{"id":"model"}],"has_more":true,"last_id":"repeated-cursor"}`)
		}))
		defer server.Close()
		models, err := ListModels(context.Background(), server.Client(), anthropicCatalogConfig(server.URL, "", "{}"), "translation")
		if err == nil || models != nil || calls != 2 || !strings.Contains(err.Error(), "重复") {
			t.Fatalf("repeated cursor was not rejected: models=%v calls=%d err=%v", models, calls, err)
		}
	})
	t.Run("initial cursor repeated", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			io.WriteString(w, `{"data":[{"id":"model"}],"has_more":true,"last_id":"starting-cursor"}`)
		}))
		defer server.Close()
		models, err := ListModels(context.Background(), server.Client(), anthropicCatalogConfig(server.URL+"?after_id=starting-cursor", "", "{}"), "translation")
		if err == nil || models != nil || calls != 1 {
			t.Fatalf("initial cursor loop was not rejected: models=%v calls=%d err=%v", models, calls, err)
		}
	})
	t.Run("page count", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			fmt.Fprintf(w, `{"data":[{"id":"model"}],"has_more":true,"last_id":"cursor-%d"}`, calls)
		}))
		defer server.Close()
		models, err := ListModels(context.Background(), server.Client(), anthropicCatalogConfig(server.URL, "", "{}"), "translation")
		if err == nil || models != nil || calls != 100 || !strings.Contains(err.Error(), "分页过多") {
			t.Fatalf("catalog page limit was not enforced: models=%v calls=%d err=%v", models, calls, err)
		}
	})
	t.Run("total item count", func(t *testing.T) {
		calls := 0
		items := make([]map[string]string, 1000)
		for i := range items {
			items[i] = map[string]string{"id": fmt.Sprintf("model-%d", i)}
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			json.NewEncoder(w).Encode(map[string]any{"data": items, "has_more": true, "last_id": fmt.Sprintf("cursor-%d", calls)})
		}))
		defer server.Close()
		models, err := ListModels(context.Background(), server.Client(), anthropicCatalogConfig(server.URL, "", "{}"), "translation")
		if err == nil || models != nil || calls != 11 || !strings.Contains(err.Error(), "数量过多") {
			t.Fatalf("total item limit was not enforced: models=%v calls=%d err=%v", models, calls, err)
		}
	})
}

func TestAnthropicListModelsNeverFollowsRedirects(t *testing.T) {
	for _, sameHost := range []bool{false, true} {
		t.Run(fmt.Sprint(sameHost), func(t *testing.T) {
			destinationCalls := 0
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				destinationCalls++
				io.WriteString(w, `{"data":[{"id":"redirected"}]}`)
			}))
			defer destination.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/target" {
					destinationCalls++
					return
				}
				target := destination.URL
				if sameHost {
					target = "/target"
				}
				http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			}))
			defer server.Close()
			client := server.Client()
			models, err := ListModels(context.Background(), client, anthropicCatalogConfig(server.URL, "redirect-test-key", "{}"), "translation")
			if err == nil || models != nil || destinationCalls != 0 || client.CheckRedirect != nil {
				t.Fatal("catalog redirected credentials or mutated caller client")
			}
		})
	}
}

func TestAnthropicListModelsReportsSafeHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "API 密钥"},
		{http.StatusForbidden, "访问权限"},
		{http.StatusNotFound, "手动填写"},
		{http.StatusInternalServerError, "HTTP 500"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, "response-secret")
			}))
			defer server.Close()
			models, err := ListModels(context.Background(), server.Client(), anthropicCatalogConfig(server.URL+"?private=query-secret", "catalog-key-secret", "{}"), "translation")
			if err == nil || models != nil || !strings.Contains(err.Error(), tc.fragment) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("HTTP error was unsafe or not actionable: models=%v err=%v", models, err)
			}
		})
	}
}

func TestAnthropicListModelsHonorsContextAcrossPages(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			io.WriteString(w, `{"data":[{"id":"first"}],"has_more":true,"last_id":"first"}`)
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	models, err := ListModels(ctx, server.Client(), anthropicCatalogConfig(server.URL, "", "{}"), "translation")
	if !errors.Is(err, context.DeadlineExceeded) || models != nil || calls != 2 {
		t.Fatalf("pagination ignored caller deadline: models=%v calls=%d err=%v", models, calls, err)
	}
}

func TestAnthropicListModelsRejectsBadHeadersBeforeRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	for _, headers := range []string{`{"X-Bad":"value\nheader-secret"}`, `{"X-Bad":"{{unknown}}"}`, `[]`} {
		models, err := ListModels(context.Background(), server.Client(), anthropicCatalogConfig(server.URL, "", headers), "translation")
		if err == nil || models != nil || strings.Contains(err.Error(), "header-secret") {
			t.Fatalf("invalid header configuration accepted or leaked: models=%v err=%v", models, err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid configuration sent a request")
	}
}
