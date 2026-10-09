package engine

import "testing"

func TestHotkeys(t *testing.T) {
	p, e := ParseHotkey("shift+CTRL+s")
	if e != nil || p.Modifiers != 6 || p.Key != 'S' || p.Canonical != "Ctrl+Shift+S" {
		t.Fatalf("%+v %v", p, e)
	}
	if e = ValidateHotkeys(DefaultConfig().Hotkeys); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"Ctrl+Ctrl+A", "Ctrl", "A", "Alt+??", "Ctrl+F25"} {
		if _, e = ParseHotkey(s); e == nil {
			t.Errorf("accepted %s", s)
		}
	}
	h := DefaultConfig().Hotkeys
	h.OCR = h.Capture
	if ValidateHotkeys(h) == nil {
		t.Fatal("duplicate shortcuts accepted")
	}
	if p, e = ParseHotkey(""); e != nil || p.Key != 0 {
		t.Fatal("empty shortcut should disable action")
	}
}
