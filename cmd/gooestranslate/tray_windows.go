package main

import (
	"fmt"
	"github.com/gooesman/gooestranslate/internal/engine"
	"syscall"
	"unsafe"
)

func (a *appState) setShortcutRecording(enabled bool) {
	if a.shortcutRecording == enabled {
		return
	}
	a.shortcutRecording = enabled
	a.configureSelection()
	if a.testMode {
		return
	}
	if enabled {
		for id := range a.keys {
			call(user, "UnregisterHotKey", a.settings.hwnd, u(id))
		}
		a.keys = map[int]engine.Hotkey{}
	} else {
		a.hotkeyErrors = a.replaceKeys(effectiveHotkeys(a.cfg))
	}
}

type notifyIcon struct {
	Size                uint32
	Hwnd                uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Version             uint32
	InfoTitle           [64]uint16
	InfoFlags           uint32
	GUID                [16]byte
	BalloonIcon         uintptr
}

func (a *appState) trayIcon(operation uintptr) bool {
	icon := a.icon
	if icon == 0 {
		icon = call(user, "LoadIconW", 0, 32512)
	}
	n := notifyIcon{Hwnd: a.settings.hwnd, ID: 1, Flags: 1 | 2 | 4, Callback: trayMessage, Icon: icon}
	n.Size = uint32(unsafe.Sizeof(n))
	copy(n.Tip[:], syscall.StringToUTF16("gooestranslate · 右键截图，双击打开设置"))
	return call(syscall.NewLazyDLL("shell32.dll"), "Shell_NotifyIconW", operation, uintptr(unsafe.Pointer(&n))) != 0
}
func (a *appState) trayMenu() {
	menu := call(user, "CreatePopupMenu")
	defer call(user, "DestroyMenu", menu)
	for _, v := range []struct {
		id    int
		label string
	}{{1, "截图工具"}, {2, "截图识别"}, {3, "截图翻译"}, {4, "原图翻译"}, {8, "文本翻译"}, {7, "划词翻译设置"}, {5, "打开设置"}, {6, "退出"}} {
		call(user, "AppendMenuW", menu, 0, u(v.id), uintptr(unsafe.Pointer(str(v.label))))
	}
	var p point
	call(user, "GetCursorPos", uintptr(unsafe.Pointer(&p)))
	call(user, "SetForegroundWindow", a.settings.hwnd)
	choice := call(user, "TrackPopupMenu", menu, 0x100|2, u(int(p.X)), u(int(p.Y)), 0, a.settings.hwnd, 0)
	switch choice {
	case 1, 2, 3, 4:
		modes := []string{"choose", "ocr", "translate", "image"}
		a.startCapture(modes[int(choice)-1])
	case 5:
		a.showSettings("")
	case 6:
		a.quit()
	case 8:
		a.showTextInput()
	case 7:
		a.showSettings("selection")
	}
	call(user, "PostMessageW", a.settings.hwnd, 0, 0, 0)
}
func keyEntries(keys engine.Hotkeys) []struct {
	id          int
	name, value string
} {
	return []struct {
		id          int
		name, value string
	}{{10, "截图工具", keys.Capture}, {11, "截图识别", keys.OCR}, {12, "截图翻译", keys.Translate}, {13, "原图翻译", keys.Image}, {14, "划词翻译", keys.Selection}}
}
func (a *appState) registerKeys(keys engine.Hotkeys) []string {
	var errs []string
	for _, v := range keyEntries(keys) {
		parsed, e := engine.ParseHotkey(v.value)
		if e != nil {
			errs = append(errs, v.name+"："+e.Error())
			continue
		}
		if parsed.Key == 0 {
			continue
		}
		if call(user, "RegisterHotKey", a.settings.hwnd, u(v.id), uintptr(parsed.Modifiers)|0x4000, uintptr(parsed.Key)) == 0 {
			errs = append(errs, fmt.Sprintf("%s：%s 已被其他程序占用", v.name, parsed.Canonical))
		} else {
			a.keys[v.id] = parsed
		}
	}
	return errs
}
func (a *appState) replaceKeys(keys engine.Hotkeys) []string {
	for id := range a.keys {
		call(user, "UnregisterHotKey", a.settings.hwnd, u(id))
	}
	a.keys = map[int]engine.Hotkey{}
	return a.registerKeys(keys)
}
