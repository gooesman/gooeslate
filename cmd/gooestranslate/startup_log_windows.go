package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Record lifecycle stages only: never arguments, configuration, or translated text.
func newStartupRecorder(args []string, configPath string) func(string) {
	if len(args) != 1 && !autostartLaunch(args) {
		return func(string) {}
	}
	path := filepath.Join(filepath.Dir(configPath), "startup.log")
	mode := "manual"
	if autostartLaunch(args) {
		mode = "autostart"
	}
	return func(stage string) {
		if os.MkdirAll(filepath.Dir(path), 0700) != nil {
			return
		}
		flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
		if info, err := os.Stat(path); err == nil && info.Size() >= 64*1024 {
			flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		}
		file, err := os.OpenFile(path, flags, 0600)
		if err != nil {
			return
		}
		defer file.Close()
		fmt.Fprintf(file, "%s version=%s pid=%d mode=%s stage=%s\n", time.Now().Format(time.RFC3339), version, os.Getpid(), mode, stage)
	}
}
