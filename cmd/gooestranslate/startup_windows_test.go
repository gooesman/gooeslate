package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gooesman/gooeslate/internal/engine"
	"golang.org/x/sys/windows/registry"
)

type fakeStartupStore struct {
	value     startupValue
	readErr   error
	writeErr  error
	deleteErr error
	writes    int
	deletes   int
}

func (s *fakeStartupStore) Read() (startupValue, error) {
	return s.value, s.readErr
}
func (s *fakeStartupStore) Write(value startupValue) error {
	s.writes++
	if s.writeErr != nil {
		return s.writeErr
	}
	value.data = append([]byte(nil), value.data...)
	s.value = value
	return nil
}
func (s *fakeStartupStore) Delete() error {
	s.deletes++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.value = startupValue{}
	return nil
}

func startupTestExecutable(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "翻译 程序.exe")
	if err := os.WriteFile(file, []byte("test executable fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestStartupDefaultAndQuotedBackgroundCommand(t *testing.T) {
	if engine.DefaultConfig().StartAtLogin {
		t.Fatal("startup must default to disabled")
	}
	exe := startupTestExecutable(t)
	store := &fakeStartupStore{}
	change, err := prepareStartupChangeWithStore(exe, store)
	if err != nil {
		t.Fatal(err)
	}
	if change.CurrentEnabled() || store.writes != 0 || store.deletes != 0 {
		t.Fatal("preparing must only read the current startup value")
	}
	if change.command != `"`+exe+`" --autostart` {
		t.Fatalf("unexpected startup command: %q", change.command)
	}
	if err := change.Apply(true); err != nil {
		t.Fatal(err)
	}
	if enabled, err := startupValueEnabled(store.value); err != nil || !enabled {
		t.Fatalf("enabled=%v, error=%v", enabled, err)
	}
	if err := change.Rollback(); err != nil {
		t.Fatal(err)
	}
	if store.value.exists {
		t.Fatal("rollback must remove a value absent before the change")
	}
	deletes := store.deletes
	if err := change.Rollback(); err != nil || store.deletes != deletes {
		t.Fatal("a completed rollback must be idempotent")
	}
}

func TestStartupRollbackRestoresOriginalBytesAndType(t *testing.T) {
	for _, original := range []startupValue{
		startupStringValue(`"D:\Existing\older.exe" --autostart`),
		{exists: true, typeID: registry.EXPAND_SZ, data: startupStringValue(`"%LOCALAPPDATA%\gooestranslate.exe" --autostart`).data},
		{exists: true, typeID: registry.BINARY, data: []byte{1, 0, 255, 0, 17}},
	} {
		t.Run(string(rune('0'+original.typeID)), func(t *testing.T) {
			store := &fakeStartupStore{value: original}
			change, err := prepareStartupChangeWithStore(startupTestExecutable(t), store)
			if err != nil {
				t.Fatal(err)
			}
			if err := change.Apply(false); err != nil {
				t.Fatal(err)
			}
			if store.value.exists {
				t.Fatal("disable must remove the owned value")
			}
			if err := change.Rollback(); err != nil {
				t.Fatal(err)
			}
			if !store.value.exists || store.value.typeID != original.typeID || !bytes.Equal(store.value.data, original.data) {
				t.Fatal("rollback did not restore the original registry value exactly")
			}
		})
	}
}

func TestStartupFailuresDoNotMutateOrDiscardRollback(t *testing.T) {
	exe := startupTestExecutable(t)
	accessError := errors.New("access denied")
	store := &fakeStartupStore{readErr: accessError}
	if _, err := prepareStartupChangeWithStore(exe, store); !errors.Is(err, accessError) {
		t.Fatalf("read errors must propagate: %v", err)
	}
	if store.writes != 0 || store.deletes != 0 {
		t.Fatal("preparation failure mutated startup state")
	}
	store = &fakeStartupStore{writeErr: accessError}
	change, err := prepareStartupChangeWithStore(exe, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Apply(true); !errors.Is(err, accessError) {
		t.Fatalf("write errors must propagate: %v", err)
	}
	if err := change.Rollback(); err != nil || store.deletes != 0 {
		t.Fatal("failed Apply must not require rollback")
	}
	store.writeErr = nil
	if err := change.Apply(true); err != nil {
		t.Fatal(err)
	}
	store.deleteErr = accessError
	if err := change.Rollback(); !errors.Is(err, accessError) {
		t.Fatalf("rollback error must propagate: %v", err)
	}
	store.deleteErr = nil
	if err := change.Rollback(); err != nil || store.value.exists {
		t.Fatal("rollback must remain retryable after failure")
	}
}

func TestStartupStatusRejectsMalformedValues(t *testing.T) {
	for _, value := range []startupValue{
		{exists: true, typeID: registry.BINARY, data: []byte{1, 2}},
		{exists: true, typeID: registry.SZ, data: []byte{1}},
	} {
		if enabled, err := startupValueEnabled(value); err == nil || enabled {
			t.Fatalf("malformed value silently became a usable setting: enabled=%v err=%v", enabled, err)
		}
	}
	for _, value := range []startupValue{{}, startupStringValue("  ")} {
		if enabled, err := startupValueEnabled(value); err != nil || enabled {
			t.Fatalf("empty value should be disabled: enabled=%v err=%v", enabled, err)
		}
	}
}

func TestStartupCommandRejectsInvalidExecutable(t *testing.T) {
	for _, path := range []string{"", "relative.exe", `D:\bad"path.exe`, "D:\\bad\x00path.exe", t.TempDir(), filepath.Join(t.TempDir(), "missing.exe")} {
		if command, err := startupCommand(path); err == nil || command != "" {
			t.Fatalf("invalid executable accepted: %q", path)
		}
	}
	command, err := startupCommand(startupTestExecutable(t))
	if err != nil || !strings.HasSuffix(command, " --autostart") {
		t.Fatalf("valid executable rejected: %q, %v", command, err)
	}
}
