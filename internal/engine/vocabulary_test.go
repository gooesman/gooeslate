package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type vocabularyGuardTransport func(*http.Request) (*http.Response, error)

func (transport vocabularyGuardTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func TestVocabularyWebsiteIsRejectedBeforeSendingAuthorization(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: vocabularyGuardTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("no website request expected")
	})}
	for _, endpoint := range []string{"https://my.eudic.net/OpenAPI/Authorization", "https://MY.EUDIC.NET./OpenAPI/Authorization", "https://dict.eudic.net/account/login", "https://www.eudic.net"} {
		cfg := DefaultConfig()
		cfg.Vocabulary.BaseURL = endpoint
		cfg.Vocabulary.APIKey = "TEST-GUARD-TOKEN"
		if err := ValidateServiceConfig(cfg); err == nil || !strings.Contains(err.Error(), "网页地址") {
			t.Fatal("saving a website as API base must explain the mistake", err)
		}
		_, err := ListVocabulary(context.Background(), client, cfg.Vocabulary)
		if err == nil || !strings.Contains(err.Error(), DefaultVocabularyBaseURL) {
			t.Fatal("website request must point to correct API base", err)
		}
	}
	if calls != 0 {
		t.Fatal("authorization must not be sent to a known webpage endpoint")
	}
	for _, endpoint := range []string{DefaultVocabularyBaseURL, "http://localhost:18080/eudic/v1"} {
		if err := ValidateVocabularyBaseURL(endpoint); err != nil {
			t.Fatal("official and custom API bases must remain supported", err)
		}
	}
}

func TestVocabularyHTTPStatusExplainsAuthorizationAndAddress(t *testing.T) {
	for _, item := range []struct {
		code        int
		explanation string
	}{{401, "授权"}, {400, "生词本语言和 ID"}, {404, "API 基础地址"}, {403, "授权与连接配置"}} {
		t.Run(item.explanation, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(item.code)
				w.Write([]byte("TEST-TOKEN PRIVATE ACCOUNT PAYLOAD"))
			}))
			defer server.Close()
			cfg := VocabularyConfig{BaseURL: server.URL, Language: "en", APIKey: "TEST-TOKEN"}
			_, err := ListVocabulary(context.Background(), server.Client(), cfg)
			if err == nil || !strings.Contains(err.Error(), item.explanation) || strings.Contains(err.Error(), "模型") || strings.Contains(err.Error(), "TEST-TOKEN") || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("wrong vocabulary-specific error", err)
			}
		})
	}
}

func TestVocabularyOfficialProtocolAndStringIDs(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "NIS TEST-TOKEN" {
			t.Error("wrong authorization normalization")
		}
		switch r.URL.Path {
		case "/studylist/category":
			if r.Method != "GET" || r.URL.Query().Get("language") != "en" {
				t.Error("wrong category request")
			}
			w.Write([]byte(`{"data":[{"id":"123456789012345678","name":"学习","language":"en"},{"id":123456789012345679,"name":"阅读","language":"en"}]}`))
		case "/studylist/words":
			var body struct {
				Category string   `json:"category_id"`
				Language string   `json:"language"`
				Words    []string `json:"words"`
			}
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Category != "123456789012345678" || body.Language != "en" || len(body.Words) != 1 || body.Words[0] != "workflow" {
				t.Error("invalid word payload or ID rounding")
			}
			w.WriteHeader(201)
			w.Write([]byte(`{"message":"ok"}`))
		case "/studylist/word":
			if r.Method != "GET" || r.URL.Query().Get("word") != "workflow" {
				t.Error("wrong lookup")
			}
			w.Write([]byte(`{"word":"workflow","exp":"<b>n.</b> 工作流程<br>工作流"}`))
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := VocabularyConfig{Enabled: true, Provider: "eudic", BaseURL: server.URL, APIKey: "NIS TEST-TOKEN", CategoryID: "123456789012345678", Language: "en", TimeoutMS: 1000}
	items, err := ListVocabulary(context.Background(), server.Client(), cfg)
	if err != nil || len(items) != 2 || items[0].ID != cfg.CategoryID || items[1].ID != "123456789012345679" {
		t.Fatal("category IDs must remain exact strings", err)
	}
	if _, err = AddVocabulary(context.Background(), server.Client(), cfg, "workflow"); err != nil {
		t.Fatal(err)
	}
	entry, err := LookupVocabulary(context.Background(), server.Client(), cfg, "workflow")
	if err != nil || len(entry.Definitions) != 2 || entry.Definitions[0] != "n. 工作流程" {
		t.Fatal("word lookup must use plain text", err)
	}
	if calls != 3 {
		t.Fatal("unexpected implicit account requests")
	}
	if _, err = AddVocabulary(context.Background(), server.Client(), cfg, "工作流程"); err == nil || calls != 3 {
		t.Fatal("English book must reject Chinese translations before request")
	}
	cfg.Enabled = false
	if _, err = AddVocabulary(context.Background(), server.Client(), cfg, "workflow"); err == nil || calls != 3 {
		t.Fatal("disabled book must not write")
	}
}

func TestVocabularyErrorsNeverExposeAuthOrProviderPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte("TEST-TOKEN PRIVATE ACCOUNT PAYLOAD"))
	}))
	defer server.Close()
	cfg := VocabularyConfig{Enabled: true, BaseURL: server.URL, Language: "en", APIKey: "TEST-TOKEN"}
	_, err := AddVocabulary(context.Background(), server.Client(), cfg, "hello")
	if err == nil || strings.Contains(err.Error(), "TEST-TOKEN") || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("provider credentials or payload leaked", err)
	}
}
