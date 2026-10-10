package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
