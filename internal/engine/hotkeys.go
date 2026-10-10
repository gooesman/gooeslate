package engine

import (
	"fmt"
	"strconv"
	"strings"
)

type Hotkey struct {
	Modifiers uint32
	Key       uint32
	Canonical string
}

func ParseHotkey(value string) (Hotkey, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Hotkey{}, nil
	}
	parts := strings.Split(value, "+")
	var mods uint32
	var labels []string
	for _, p := range parts[:len(parts)-1] {
		var flag uint32
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "ctrl", "control":
			flag = 2
		case "alt":
			flag = 1
		case "shift":
			flag = 4
		case "win", "meta":
			flag = 8
		default:
			return Hotkey{}, fmt.Errorf("不支持快捷键修饰符：%s", p)
		}
		if mods&flag != 0 {
			return Hotkey{}, fmt.Errorf("快捷键包含重复修饰符")
		}
		mods |= flag
	}
	key := strings.ToUpper(strings.TrimSpace(parts[len(parts)-1]))
	var vk uint32
	if len(key) == 1 && ((key[0] >= 'A' && key[0] <= 'Z') || (key[0] >= '0' && key[0] <= '9')) {
		vk = uint32(key[0])
	} else if key == "SPACE" {
		vk = 32
	} else if strings.HasPrefix(key, "F") {
		n, e := strconv.Atoi(key[1:])
		if e == nil && n >= 1 && n <= 24 {
			vk = uint32(111 + n)
		}
	}
	if vk == 0 {
		return Hotkey{}, fmt.Errorf("快捷键需要字母、数字、F1–F24 或 Space")
	}
	if mods == 0 && !(vk >= 112 && vk <= 135) {
		return Hotkey{}, fmt.Errorf("快捷键至少需要 Ctrl、Alt、Shift 或 Win")
	}
	for _, m := range []struct {
		v uint32
		s string
	}{{2, "Ctrl"}, {1, "Alt"}, {4, "Shift"}, {8, "Win"}} {
		if mods&m.v != 0 {
			labels = append(labels, m.s)
		}
	}
	labels = append(labels, key)
	return Hotkey{mods, vk, strings.Join(labels, "+")}, nil
}
func ValidateHotkeys(h Hotkeys) error {
	seen := map[string]string{}
	for _, v := range []struct{ name, key string }{{"截图工具", h.Capture}, {"截图识别", h.OCR}, {"截图翻译", h.Translate}, {"原图翻译", h.Image}, {"划词翻译", h.Selection}} {
		p, e := ParseHotkey(v.key)
		if e != nil {
			return fmt.Errorf("%s：%w", v.name, e)
		}
		if p.Key == 0 {
			continue
		}
		if name, exists := seen[p.Canonical]; exists {
			return fmt.Errorf("%s 与%s的快捷键重复", v.name, name)
		}
		seen[p.Canonical] = v.name
	}
	return nil
}
