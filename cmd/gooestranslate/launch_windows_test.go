package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/gooesman/gooestranslate/internal/engine"
)

type fakeStartupTransaction struct {
	events                *[]string
	applyErr, rollbackErr error
}

func (s fakeStartupTransaction) Apply(enabled bool) error {
	if !enabled {
		panic("expected startup enable in this fixture")
	}
	*s.events = append(*s.events, "startup")
	return s.applyErr
}

func (s fakeStartupTransaction) Rollback() error {
	*s.events = append(*s.events, "restore startup")
	return s.rollbackErr
}

func TestStartupAndConfigurationSaveAreTransactional(t *testing.T) {
	for _, fixture := range []struct {
		name                           string
		applyErr, saveErr, rollbackErr error
		registrationFailure            bool
		want                           []string
		errorText                      string
	}{
		{"success", nil, nil, nil, false, []string{"startup", "hotkeys", "save"}, ""},
		{"startup failure", errors.New("TEST permission denied"), nil, nil, false, []string{"startup"}, "permission denied"},
		{"hotkey failure", nil, nil, nil, true, []string{"startup", "hotkeys", "restore hotkeys", "restore startup"}, "快捷键保存失败"},
		{"save failure", nil, errors.New("TEST disk denied"), nil, false, []string{"startup", "hotkeys", "save", "restore hotkeys", "restore startup"}, "disk denied"},
		{"restore failure reported", nil, errors.New("TEST disk denied"), errors.New("TEST restore denied"), false, []string{"startup", "hotkeys", "save", "restore hotkeys", "restore startup"}, "restore denied"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			old := engine.DefaultConfig()
			next := cloneConfig(old)
			next.StartAtLogin = true
			next.Hotkeys.Capture = "Ctrl+Alt+Q"
			var events []string
			replace := func(keys engine.Hotkeys) []string {
				if keys == effectiveHotkeys(old) {
					events = append(events, "restore hotkeys")
					return nil
				}
				events = append(events, "hotkeys")
				if fixture.registrationFailure {
					return []string{"TEST conflict"}
				}
				return nil
			}
			save := func(c engine.Config) error {
				events = append(events, "save")
				if !c.StartAtLogin {
					t.Fatal("startup preference was lost during save")
				}
				return fixture.saveErr
			}
			startup := fakeStartupTransaction{&events, fixture.applyErr, fixture.rollbackErr}
			err := persistConfigurationWithStartup(old, next, startup, replace, save)
			if !reflect.DeepEqual(events, fixture.want) {
				t.Fatalf("operations = %v; want %v", events, fixture.want)
			}
			if fixture.errorText == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), fixture.errorText) {
				t.Fatalf("error = %v; want %q", err, fixture.errorText)
			}
		})
	}
}

func TestUnchangedStartupDoesNotNeedWindowsRegistration(t *testing.T) {
	cfg := engine.DefaultConfig()
	if cfg.StartAtLogin {
		t.Fatal("login startup must be off on a fresh installation")
	}
	called := false
	err := persistConfigurationWithStartup(cfg, cfg, nil, func(engine.Hotkeys) []string { return nil }, func(engine.Config) error { called = true; return nil })
	if err != nil || !called {
		t.Fatalf("unrelated save failed: %v", err)
	}
}

func TestLaunchVisibility(t *testing.T) {
	for _, fixture := range []struct {
		args          []string
		tray, visible bool
	}{
		{[]string{"app.exe"}, true, true},
		{[]string{"app.exe", "--autostart"}, true, false},
		{[]string{"app.exe", "--autostart"}, false, true},
		{[]string{"app.exe", "--open", "test.png"}, true, true},
		{[]string{"app.exe", "--autostart", "unexpected"}, true, true},
	} {
		if got := showSettingsAtLaunch(fixture.args, fixture.tray); got != fixture.visible {
			t.Fatalf("args %v tray %v: visible = %v", fixture.args, fixture.tray, got)
		}
	}
}

func TestApplicationInstanceMutex(t *testing.T) {
	name := instanceMutexName(t.TempDir())
	first, primary, err := acquireApplicationInstance(name)
	if err != nil || first == 0 || !primary {
		t.Fatalf("first = %v, %v, %v", first, primary, err)
	}
	defer call(kernel, "CloseHandle", first)
	second, primary, err := acquireApplicationInstance(name)
	if err != nil || second == 0 || primary {
		t.Fatalf("second = %v, %v, %v", second, primary, err)
	}
	call(kernel, "CloseHandle", second)
	if instanceMutexName("C:\\TEST\\settings.json") != instanceMutexName("c:\\test\\settings.json") {
		t.Fatal("Windows path case changes created a second instance identity")
	}
	// Verify closing both instances releases the name, using a fresh isolated mutex.
	probeName := name + "-release"
	h, _, err := acquireApplicationInstance(probeName)
	if err != nil {
		t.Fatal(err)
	}
	call(kernel, "CloseHandle", h)
	h, primary, err = acquireApplicationInstance(probeName)
	if err != nil || !primary {
		t.Fatalf("released name remained occupied: %v", err)
	}
	call(kernel, "CloseHandle", h)
}

func TestExportedShellIconLoadsWithWindowsDecoder(t *testing.T) {
	path := t.TempDir() + "\\gooestranslate.ico"
	if err := writeApplicationIcon(path); err != nil {
		t.Fatal(err)
	}
	icon := call(user, "LoadImageW", 0, uintptr(unsafe.Pointer(str(path))), 1, 64, 64, 0x10)
	if icon == 0 {
		t.Fatal("exported icon cannot be decoded by the Windows shell")
	}
	call(user, "DestroyIcon", icon)
	if err := writeApplicationIcon(t.TempDir() + "\\missing\\icon.ico"); err == nil {
		t.Fatal("writing an icon to a missing folder silently succeeded")
	}
}
