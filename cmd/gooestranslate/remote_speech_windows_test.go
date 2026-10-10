package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// All playback commands are mocked. These tests never open an audio device.
type fakeSpeechMCI struct {
	mu                    sync.Mutex
	commands, openedFiles []string
	mode, failAction      string
	onOpen                func()
}

func (m *fakeSpeechMCI) send(command string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commands = append(m.commands, command)
	action := strings.Fields(command)[0]
	if action == "open" {
		parts := strings.SplitN(command, "\"", 3)
		if len(parts) != 3 {
			return "", errors.New("missing quoted local path")
		}
		m.openedFiles = append(m.openedFiles, parts[1])
		if _, err := os.Stat(parts[1]); err != nil {
			return "", errors.New("local audio must exist before MCI open")
		}
		if m.onOpen != nil {
			m.onOpen()
		}
	}
	if m.failAction == action {
		m.failAction = ""
		return "", errors.New("mock device failure")
	}
	if action == "status" {
		return m.mode, nil
	}
	return "", nil
}

func (m *fakeSpeechMCI) snapshot() ([]string, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.commands...), append([]string(nil), m.openedFiles...)
}

func speechFixtureWAV() []byte {
	data := make([]byte, 44)
	copy(data, "RIFF")
	copy(data[8:], "WAVE")
	return data
}

func assertSpeechFilesGone(t *testing.T, files []string) {
	t.Helper()
	for _, file := range files {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("temporary speech audio was not removed: %s (%v)", file, err)
		}
	}
}

func TestRemoteSpeechFormatValidation(t *testing.T) {
	for _, tc := range []struct {
		audio  []byte
		format string
		want   string
	}{
		{speechFixtureWAV(), "audio/wav; charset=binary", "wav"},
		{[]byte("ID3\x04\x00\x00\x00\x00\x00\x00payload"), "audio/mpeg", "mp3"},
		{[]byte{0xff, 0xfb, 0x90, 0}, " MP3 ", "mp3"},
		{[]byte("<html>error</html>"), "mp3", ""},
		{[]byte("ID3"), "mp3", ""},
		{speechFixtureWAV(), "mp3", ""},
		{speechFixtureWAV(), "pcm", ""},
		{nil, "wav", ""},
	} {
		got, err := remoteSpeechFormat(tc.audio, tc.format)
		if got != tc.want || (err == nil) != (tc.want != "") {
			t.Fatalf("format=%s got=%s err=%v want=%s", tc.format, got, err, tc.want)
		}
	}
}

func TestRemoteSpeechReplacesStopsAndCleansUpOnExit(t *testing.T) {
	base := t.TempDir()
	m := &fakeSpeechMCI{mode: "playing"}
	s := newRemoteSpeechService(m.send, nil, base, time.Hour)
	defer s.Close()
	if err := s.Play(speechFixtureWAV(), "wav"); err != nil {
		t.Fatal(err)
	}
	commands, files := m.snapshot()
	if len(files) != 1 || !strings.Contains(commands[0], " type waveaudio alias ") || strings.Contains(commands[0], "\\\\") || !strings.HasPrefix(commands[1], "play ") || strings.Contains(commands[1], "wait") {
		t.Fatalf("MCI must receive a literal local path and asynchronous audio-only play: %v", commands)
	}
	if err := s.Play([]byte("ID3\x04\x00\x00\x00\x00\x00\x00payload"), "mp3"); err != nil {
		t.Fatal(err)
	}
	commands, files = m.snapshot()
	if len(files) != 2 || !strings.HasPrefix(commands[2], "stop ") || !strings.HasPrefix(commands[3], "close ") || !strings.Contains(commands[4], "type mpegvideo") {
		t.Fatalf("replace must close prior device before opening new one: %v", commands)
	}
	assertSpeechFilesGone(t, files[:1])
	s.Stop()
	assertSpeechFilesGone(t, files)
	if err := s.Play(speechFixtureWAV(), "wav"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s.Close() // idempotent and safe after cleanup
	_, files = m.snapshot()
	assertSpeechFilesGone(t, files)
	dirs, err := os.ReadDir(base)
	if err != nil || len(dirs) != 0 {
		t.Fatalf("worker-owned audio directory must be removed on exit: %v %v", dirs, err)
	}
	if err = s.Play(speechFixtureWAV(), "wav"); err == nil {
		t.Fatal("closed speech worker must reject playback")
	}
}

func TestRemoteSpeechCompletedPlaybackAndStartupFailureCleanup(t *testing.T) {
	for _, failure := range []string{"", "open", "play"} {
		t.Run(failure, func(t *testing.T) {
			m := &fakeSpeechMCI{mode: "stopped", failAction: failure}
			s := newRemoteSpeechService(m.send, nil, t.TempDir(), 5*time.Millisecond)
			defer s.Close()
			err := s.Play(speechFixtureWAV(), "wav")
			if (err != nil) != (failure != "") {
				t.Fatalf("startup error must be returned: %v", err)
			}
			_, files := m.snapshot()
			deadline := time.Now().Add(time.Second)
			for len(files) == 1 && time.Now().Before(deadline) {
				if _, err = os.Stat(files[0]); os.IsNotExist(err) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			assertSpeechFilesGone(t, files)
			if failure != "" {
				if err := s.Play(speechFixtureWAV(), "wav"); err != nil {
					t.Fatalf("failed startup must not poison subsequent playback: %v", err)
				}
			}
		})
	}
}

func TestRemoteSpeechCancellationBeforeAndDuringOpenNeverPlays(t *testing.T) {
	for _, stage := range []string{"before", "open"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := t.TempDir()
			m := &fakeSpeechMCI{mode: "playing"}
			if stage == "before" {
				cancel()
			} else {
				m.onOpen = cancel
			}
			s := newRemoteSpeechService(m.send, nil, base, time.Hour)
			defer s.Close()
			if err := s.PlayContext(ctx, speechFixtureWAV(), "wav"); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled request must return context cancellation: %v", err)
			}
			// Stop synchronizes worker cleanup even if cancellation let the caller
			// return before the worker finished closing its newly opened device.
			s.Stop()
			commands, files := m.snapshot()
			for _, command := range commands {
				if strings.HasPrefix(command, "play ") {
					t.Fatalf("canceled speech must not start playback: %v", commands)
				}
			}
			assertSpeechFilesGone(t, files)
			if stage == "before" && len(commands) != 0 {
				t.Fatal("already canceled request must never open a device")
			}
		})
	}
}

func TestRemoteSpeechQueuedCancellationNeverOpensDevice(t *testing.T) {
	firstOpen, releaseOpen := make(chan struct{}), make(chan struct{})
	m := &fakeSpeechMCI{mode: "playing"}
	once := sync.Once{}
	m.onOpen = func() {
		once.Do(func() {
			close(firstOpen)
			<-releaseOpen
		})
	}
	s := newRemoteSpeechService(m.send, nil, t.TempDir(), time.Hour)
	defer s.Close()
	releaseOnce := sync.Once{}
	defer releaseOnce.Do(func() { close(releaseOpen) })
	firstResult := make(chan error, 1)
	go func() { firstResult <- s.Play(speechFixtureWAV(), "wav") }()
	select {
	case <-firstOpen:
	case <-time.After(time.Second):
		t.Fatal("first mock device did not enter open")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reply := make(chan error, 1)
	s.requests <- remoteSpeechRequest{ctx: ctx, audio: speechFixtureWAV(), format: "wav", reply: reply}
	cancel() // The request is queued behind a currently opening device.
	releaseOnce.Do(func() { close(releaseOpen) })
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-reply:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("worker must check canceled request before file/device setup: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled queued request did not complete")
	}
	_, files := m.snapshot()
	if len(files) != 1 {
		t.Fatalf("queued canceled audio must never open another device: %v", files)
	}
}

func TestRemoteSpeechOnlyOwnDirectoryFilesAreRemoved(t *testing.T) {
	base := t.TempDir()
	unrelated := filepath.Join(base, "keep.txt")
	if err := os.WriteFile(unrelated, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	m := &fakeSpeechMCI{mode: "playing"}
	s := newRemoteSpeechService(m.send, nil, base, time.Hour)
	if err := s.Play(speechFixtureWAV(), "wav"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "unrelated" {
		t.Fatalf("cleanup touched an unrelated file: %v", err)
	}
}
