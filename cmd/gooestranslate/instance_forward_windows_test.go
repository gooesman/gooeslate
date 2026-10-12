package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gooesman/gooeslate/internal/engine"
)

func TestInstanceOpenImageResolvesSenderWorkingDirectory(t *testing.T) {
	for _, input := range []string{filepath.Join(t.TempDir(), "截图 空格.png"), filepath.Join("images", "截图 空格.png")} {
		payload, err := encodeInstanceOpenImage(input)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeInstanceOpenImage(instanceOpenImageID, payload)
		want, absoluteErr := filepath.Abs(input)
		if err != nil || absoluteErr != nil || got != want {
			t.Fatalf("forwarded path = %q, error = %v; want %q", got, err, want)
		}
		if !filepath.IsAbs(got) {
			t.Fatal("receiving process would interpret a relative path in its own working directory")
		}
	}
}

func TestInstanceOpenImageRejectsUnrelatedOrMalformedMessages(t *testing.T) {
	valid, err := encodeInstanceOpenImage(filepath.Join(t.TempDir(), "fixture.png"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name    string
		id      uintptr
		payload []byte
	}{
		{"other message", instanceOpenImageID + 1, valid},
		{"empty", instanceOpenImageID, nil},
		{"too large", instanceOpenImageID, []byte(strings.Repeat(" ", instancePayloadLimit+1))},
		{"invalid JSON", instanceOpenImageID, []byte("invalid")},
		{"other object", instanceOpenImageID, []byte(`{"path":"C:\\fixture.png"}`)},
		{"null", instanceOpenImageID, []byte("null")},
		{"relative path", instanceOpenImageID, []byte(`"fixture.png"`)},
		{"embedded null", instanceOpenImageID, []byte(`"C:\\fixture\u0000.png"`)},
		{"trailing payload", instanceOpenImageID, append(append([]byte(nil), valid...), []byte(` "another"`)...)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if path, err := decodeInstanceOpenImage(fixture.id, fixture.payload); err == nil || path != "" {
				t.Fatalf("untrusted message became an image path: %q, %v", path, err)
			}
		})
	}
	for _, path := range []string{"", "C:\\fixture\x00.png", "C:\\" + strings.Repeat("x", instancePayloadLimit)} {
		if _, err := encodeInstanceOpenImage(path); err == nil {
			t.Fatalf("invalid sender path accepted: length %d", len(path))
		}
	}
}

func TestStartupRefreshPreservesUserToggleAfterExternalChanges(t *testing.T) {
	for _, original := range []bool{false, true} {
		a := &appState{cfg: engine.DefaultConfig()}
		a.cfg.StartAtLogin = original
		external := !original
		status := a.startupState(func() (bool, error) { return external, nil })
		if status["enabled"] != external || a.cfg.StartAtLogin != external {
			t.Fatal("displayed startup state and the next save baseline diverged")
		}
		// The user changes the displayed switch back to its original value.
		// This must remain a change that applyConfiguration can register.
		payload, err := json.Marshal(map[string]any{"start_at_login": original})
		if err != nil {
			t.Fatal(err)
		}
		next, _, err := mergeSettings(a.cfg, payload)
		if err != nil || next.StartAtLogin == a.cfg.StartAtLogin {
			t.Fatalf("user toggle would skip Windows registration: %v", err)
		}
	}
}

func TestStartupRefreshFailureAndUITestDoNotChangeLocalState(t *testing.T) {
	a := &appState{cfg: engine.DefaultConfig()}
	a.cfg.StartAtLogin = true
	status := a.startupState(func() (bool, error) { return false, errors.New("TEST access denied") })
	if !a.cfg.StartAtLogin || status["enabled"] != true || status["error"] == nil {
		t.Fatal("a failed registry read discarded the local startup preference")
	}
	a.testMode = true
	status = a.startupState(func() (bool, error) { t.Fatal("UI self-test read real Windows startup state"); return false, nil })
	if !a.cfg.StartAtLogin || status["enabled"] != true || status["error"] != nil {
		t.Fatal("UI self-test changed the startup preference")
	}
}
