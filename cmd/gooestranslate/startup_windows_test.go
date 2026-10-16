package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

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

func startupStringValue(command string) startupValue {
	words := append(utf16.Encode([]rune(command)), 0)
	data := make([]byte, len(words)*2)
	for i, word := range words {
		binary.LittleEndian.PutUint16(data[i*2:], word)
	}
	return startupValue{exists: true, typeID: registry.SZ, data: data}
}

func TestStartupDefaultAndQuotedBackgroundCommand(t *testing.T) {
	if engine.DefaultConfig().StartAtLogin {
		t.Fatal("startup must default to disabled")
	}
	exe := startupTestExecutable(t)
	command, err := startupCommand(exe)
	if err != nil || command != `"`+exe+`" --autostart` {
		t.Fatalf("unexpected startup command: %q, %v", command, err)
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
