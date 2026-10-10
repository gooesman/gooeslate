package main

import (
	"encoding/json"
	"testing"
)

func TestAuthorizationPageActionIsSettingsOnlyAndFixed(t *testing.T) {
	a := &appState{testMode: true}
	request := rpcRequest{Method: "openEudicAuthorization", Args: []json.RawMessage{json.RawMessage(`"file:///C:/not-allowed.exe"`)}}
	if _, err := a.handleRPC(&webWindow{kind: "result"}, request); err == nil {
		t.Fatal("result window must not open authorization")
	}
	value, err := a.handleRPC(&webWindow{kind: "settings"}, request)
	if err != nil || !value.(apiResult).OK {
		t.Fatal("settings authorization action must be available in tests without opening a browser", err)
	}
	if eudicAuthorizationURL != "https://my.eudic.net/OpenAPI/Authorization" {
		t.Fatal("authorization target must stay the official fixed page")
	}
}
