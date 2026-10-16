package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupRecorderSeparatesLaunchModesAndSkipsTests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	logPath := filepath.Join(filepath.Dir(path), "startup.log")
	newStartupRecorder([]string{"app.exe", "--render-smoke-test", "private-path"}, path)("started")
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatal("self-tests must not create startup logs")
	}
	newStartupRecorder([]string{"app.exe", "--autostart"}, path)("ready")
	newStartupRecorder([]string{"app.exe"}, path)("started")
	data, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(data), "mode=autostart stage=ready") || !strings.Contains(string(data), "mode=manual stage=started") {
		t.Fatalf("missing launch stages: %s, %v", data, err)
	}
	if strings.Contains(string(data), "app.exe") || strings.Contains(string(data), "private-path") {
		t.Fatal("startup diagnostics must not record arguments")
	}
}

func TestStartupRecorderBoundsLogAndToleratesUnwritableDirectory(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "startup.log")
	if err := os.WriteFile(logPath, []byte(strings.Repeat("x", 64*1024)), 0600); err != nil {
		t.Fatal(err)
	}
	newStartupRecorder([]string{"app.exe"}, filepath.Join(dir, "settings.json"))("started")
	info, err := os.Stat(logPath)
	if err != nil || info.Size() > 1024 {
		t.Fatalf("startup log was not bounded: %v", err)
	}
	// A blocked diagnostic path must not prevent the application from starting.
	newStartupRecorder([]string{"app.exe"}, filepath.Join(logPath, "settings.json"))("started")
}
