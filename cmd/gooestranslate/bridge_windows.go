package main

import (
	"encoding/json"
	"fmt"
	"github.com/gooesman/gooestranslate/internal/engine"
	"image/png"
	"os"
	"strings"
	"unsafe"
)

type apiResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}
type rpcRequest struct {
	ID     int               `json:"id"`
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
}

func decodeArgument(raw json.RawMessage, target any) error {
	var s string
	if len(raw) > 0 && raw[0] == '"' {
		if e := json.Unmarshal(raw, &s); e != nil {
			return e
		}
		raw = []byte(s)
	}
	return json.Unmarshal(raw, target)
}
func (a *appState) rpc(w *webWindow, raw string) {
	if len(raw) > 2<<20 {
		return
	}
	var req rpcRequest
	if json.Unmarshal([]byte(raw), &req) != nil || req.ID <= 0 {
		return
	}
	result, e := a.handleRPC(w, req)
	ok := e == nil
	if e != nil {
		result = e.Error()
	}
	w.browser.Eval(fmt.Sprintf("window.__resolve(%d,%t,%s)", req.ID, ok, jsonString(result)))
}
func (a *appState) state() any {
	cfg := a.cfg
	cfg.APIKey = ""
	cfg.EncryptedKey = ""
	return map[string]any{"config": cfg, "has_api_key": a.cfg.APIKey != "", "load_error": a.loadError, "version": version, "voices": a.voices, "hotkey_errors": a.hotkeyErrors, "integrations": []map[string]string{{"name": "系统语音", "status": fmt.Sprintf("%d 个可用声音", len(a.voices))}, {"name": "离线词典", "status": "JSON / ECDICT CSV"}}}
}
func (a *appState) handleRPC(w *webWindow, r rpcRequest) (any, error) {
	arg := func(i int) json.RawMessage {
		if i >= len(r.Args) {
			return json.RawMessage("null")
		}
		return r.Args[i]
	}
	switch r.Method {
	case "getState":
		return a.state(), nil
	case "getResult":
		return a.data, nil
	case "uiProbe":
		if !a.testMode {
			return nil, fmt.Errorf("此操作仅用于测试")
		}
		var ok bool
		json.Unmarshal(arg(0), &ok)
		a.uiProbe <- ok
		return true, nil
	case "saveSettings":
		if w.kind != "settings" {
			return nil, fmt.Errorf("请在设置窗口修改服务")
		}
		return a.saveSettings(arg(0))
	case "setShortcutRecording":
		if w.kind != "settings" {
			return nil, fmt.Errorf("请在设置中录制快捷键")
		}
		var enabled bool
		json.Unmarshal(arg(0), &enabled)
		a.setShortcutRecording(enabled)
		return true, nil
	case "startCapture":
		if a.testMode {
			return false, nil
		}
		var mode string
		json.Unmarshal(arg(0), &mode)
		return a.startCapture(mode), nil
	case "chooseImage":
		if a.testMode {
			return false, nil
		}
		var mode string
		json.Unmarshal(arg(0), &mode)
		file := fileDialog(w.hwnd, false)
		if file == "" {
			return false, nil
		}
		if e := a.openImage(file, mode); e != nil {
			return nil, e
		}
		return true, nil
	case "hideWindow":
		if w.kind == "settings" && !a.tray {
			return nil, fmt.Errorf("系统托盘未就绪，请暂时保持设置窗口打开")
		}
		a.setShortcutRecording(false)
		call(user, "ShowWindow", w.hwnd, 0)
		return true, nil
	case "quitApp":
		a.quit()
		return true, nil
	case "doResult":
		if w.kind != "result" {
			return nil, fmt.Errorf("请从识别结果窗口执行此操作")
		}
		var action string
		json.Unmarshal(arg(0), &action)
		var p struct {
			Text            string `json:"text"`
			RequestID       int    `json:"request_id"`
			Mode            string `json:"mode"`
			Language        string `json:"language"`
			Enabled         bool   `json:"enabled"`
			ShowTranslation bool   `json:"show_translation"`
		}
		if string(arg(1)) != "null" {
			if e := decodeArgument(arg(1), &p); e != nil {
				return nil, fmt.Errorf("操作参数无效")
			}
		}
		switch action {
		case "translate":
			if p.Mode == "image" || p.Mode == "translate" {
				a.data.Mode = p.Mode
			}
			a.translate(p.Language)
		case "cancel":
			a.stopRequest(true)
		case "copy":
			if strings.TrimSpace(p.Text) == "" {
				p.Text = a.data.OriginalText
			}
			if e := clipboard(w.hwnd, p.Text); e != nil {
				return nil, e
			}
		case "export":
			return a.exportImage(p.ShowTranslation)
		case "lookup":
			if strings.TrimSpace(p.Text) == "" {
				return nil, fmt.Errorf("请先选中文字")
			}
			a.lookup(p.Text, p.RequestID)
		case "speak":
			if strings.TrimSpace(p.Text) == "" {
				return nil, fmt.Errorf("没有可朗读的文字")
			}
			if e := a.speech.Speak(p.Text, a.cfg.TTS.Voice, a.cfg.TTS.Rate); e != nil {
				return nil, e
			}
		case "stop_speech":
			a.speech.Stop()
		case "pin":
			a.pinned = p.Enabled
			position := ^uintptr(1)
			if a.pinned {
				position = ^uintptr(0)
			}
			call(user, "KillTimer", w.hwnd, 7010)
			call(user, "SetWindowPos", w.hwnd, position, 0, 0, 0, 0, 0x13)
		case "settings":
			a.showSettings("")
		case "close":
			call(user, "ShowWindow", w.hwnd, 0)
			a.stopRequest(false)
			a.speech.Stop()
		default:
			return nil, fmt.Errorf("不支持的结果操作")
		}
		return apiResult{OK: true}, nil
	}
	return nil, fmt.Errorf("不支持的界面操作")
}
func (a *appState) saveSettings(raw json.RawMessage) (apiResult, error) {
	cfg, keyChanged, e := mergeSettings(a.cfg, raw)
	if e != nil {
		return apiResult{}, e
	}
	if e := validateSettings(cfg); e != nil {
		return apiResult{}, e
	}
	if a.testMode {
		return apiResult{true, "界面自测未写入个人设置"}, nil
	}
	old := a.cfg
	errs := a.replaceKeys(cfg.Hotkeys)
	if len(errs) > 0 {
		a.replaceKeys(old.Hotkeys)
		return apiResult{}, fmt.Errorf("快捷键保存失败：%s", strings.Join(errs, "；"))
	}
	if e := saveConfig(cfg); e != nil {
		a.replaceKeys(old.Hotkeys)
		return apiResult{}, fmt.Errorf("设置写入失败：%w", e)
	}
	a.cfg = cfg
	if keyChanged {
		a.loadError = ""
	}
	a.hotkeyErrors = nil
	for hwnd := range a.windows {
		applyWindowAppearance(hwnd, cfg.Theme)
	}
	a.data.Theme = cfg.Theme
	a.data.DictionaryEnabled = cfg.Dictionary.Enabled
	if !cfg.Dictionary.Enabled && a.lookupCancel != nil {
		a.lookupCancel()
		a.lookupCancel = nil
		a.lookupGeneration++
	}
	a.pushResult()
	return apiResult{true, "设置已保存，快捷键已生效"}, nil
}

func mergeSettings(current engine.Config, raw json.RawMessage) (engine.Config, bool, error) {
	var req struct {
		engine.Config
		APIKey      string `json:"api_key"`
		ClearAPIKey bool   `json:"clear_api_key"`
	}
	req.Config = current
	if e := decodeArgument(raw, &req); e != nil {
		return current, false, fmt.Errorf("设置格式无效")
	}
	cfg := req.Config
	cfg.APIKey = current.APIKey
	cfg.EncryptedKey = current.EncryptedKey
	keyChanged := false
	if req.ClearAPIKey {
		cfg.APIKey = ""
		cfg.EncryptedKey = ""
		keyChanged = true
	}
	if strings.TrimSpace(req.APIKey) != "" {
		cfg.APIKey = strings.TrimSpace(req.APIKey)
		cfg.EncryptedKey = ""
		keyChanged = true
	}
	return cfg, keyChanged, nil
}
func validateSettings(c engine.Config) error {
	if c.OCRURL != "" {
		if e := engine.ValidateURL(c.OCRURL); e != nil {
			return fmt.Errorf("OCR 地址：%w", e)
		}
	}
	if c.BaseURL != "" {
		if e := engine.ValidateURL(c.BaseURL); e != nil {
			return fmt.Errorf("翻译地址：%w", e)
		}
	}
	if e := engine.ValidateHotkeys(c.Hotkeys); e != nil {
		return e
	}
	if c.TTS.Rate < -5 || c.TTS.Rate > 5 {
		return fmt.Errorf("语速范围为 -5 到 5")
	}
	if c.Theme != "dark" && c.Theme != "light" && c.Theme != "system" {
		return fmt.Errorf("主题设置无效")
	}
	if c.DefaultAction != "choose" && c.DefaultAction != "ocr" && c.DefaultAction != "translate" && c.DefaultAction != "image" {
		return fmt.Errorf("默认截图操作无效")
	}
	if c.Dictionary.Provider != "openai" && c.Dictionary.Provider != "local" {
		return fmt.Errorf("词典服务无效")
	}
	if c.Dictionary.Enabled && c.Dictionary.Provider == "local" && strings.TrimSpace(c.Dictionary.LocalPath) == "" {
		return fmt.Errorf("请选择本地词典文件")
	}
	return nil
}
func (a *appState) exportImage(show bool) (apiResult, error) {
	if a.img == nil {
		return apiResult{}, fmt.Errorf("没有可导出的图片")
	}
	// A native save dialog runs a nested message loop; snapshot before opening it.
	source := a.img
	lines := append([]engine.Line(nil), a.lines...)
	translations := map[int]string{}
	if show {
		for _, v := range a.data.Translations {
			translations[v.ID] = v.Text
		}
	}
	file := fileDialog(a.result.hwnd, true)
	if file == "" {
		return apiResult{false, "已取消"}, nil
	}
	img, e := renderExport(source, lines, translations)
	if e != nil {
		return apiResult{}, e
	}
	f, e := os.Create(file)
	if e != nil {
		return apiResult{}, fmt.Errorf("保存图片失败")
	}
	e = png.Encode(f, img)
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return apiResult{}, fmt.Errorf("保存图片失败")
	}
	return apiResult{true, "图片已保存"}, nil
}
func webProcedure(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	if app == nil {
		return call(user, "DefWindowProcW", hwnd, uintptr(msg), wp, lp)
	}
	a := app
	w := a.windows[hwnd]
	switch msg {
	case taskMessage:
		for {
			select {
			case f := <-a.queue:
				if !a.exiting {
					f()
				}
			default:
				return 0
			}
		}
	case trayMessage:
		if lp == 0x202 || lp == 0x203 {
			a.showSettings("")
		} else if lp == 0x205 {
			a.trayMenu()
		}
		return 0
	case 0x312:
		if wp >= 10 && wp <= 13 {
			modes := []string{"", "ocr", "translate", "image"}
			a.startCapture(modes[int(wp)-10])
		}
		return 0
	case 0x113:
		if wp == 7001 && a.capture != nil {
			a.capture.HandleTimer()
			if e := a.capture.LastError(); e != nil {
				a.fail(e)
				a.showResult()
			}
		} else if wp == 7010 && !a.pinned {
			call(user, "KillTimer", hwnd, 7010)
			call(user, "SetWindowPos", hwnd, ^uintptr(1), 0, 0, 0, 0, 0x13)
		}
		return 0
	case 5:
		if w != nil && w.ready {
			w.browser.Resize()
		}
		return 0
	case 3:
		if w != nil && w.ready {
			w.browser.NotifyParentWindowPositionChanged()
		}
		return 0
	case 0x2e0:
		var r rect
		call(ntdll, "RtlMoveMemory", uintptr(unsafe.Pointer(&r)), lp, unsafe.Sizeof(r))
		call(user, "SetWindowPos", hwnd, 0, u(int(r.L)), u(int(r.T)), u(int(r.R-r.L)), u(int(r.B-r.T)), 0x14)
		if w != nil && w.ready {
			w.browser.Resize()
		}
		return 0
	case 0x24:
		var p [5]point
		call(ntdll, "RtlMoveMemory", uintptr(unsafe.Pointer(&p)), lp, unsafe.Sizeof(p))
		dpi := call(user, "GetDpiForWindow", hwnd)
		if dpi == 0 {
			dpi = 96
		}
		area := workArea(hwnd)
		p[3] = point{int32(min(660*int(dpi)/96, int(area.R-area.L))), int32(min(480*int(dpi)/96, int(area.B-area.T)))}
		call(ntdll, "RtlMoveMemory", lp, uintptr(unsafe.Pointer(&p)), unsafe.Sizeof(p))
		return 0
	case 0x10:
		if w != nil && w.kind == "result" {
			call(user, "ShowWindow", hwnd, 0)
			a.stopRequest(false)
			a.speech.Stop()
			return 0
		}
		a.setShortcutRecording(false)
		if a.cfg.CloseToTray && a.tray && !a.testMode {
			call(user, "ShowWindow", hwnd, 0)
		} else {
			a.quit()
		}
		return 0
	case 0x1c:
		if wp == 0 && w != nil && w.ready && w.kind == "settings" {
			a.setShortcutRecording(false)
			w.browser.Eval("window.cancelShortcutRecording&&window.cancelShortcutRecording()")
		}
	case 0x1a, 0x31a:
		if a.cfg.Theme == "system" && w != nil && w.kind == "settings" {
			for h := range a.windows {
				applyWindowAppearance(h, a.cfg.Theme)
			}
			a.pushResult()
		}
	case 2:
		delete(a.windows, hwnd)
		return 0
	case 0x14:
		return 1
	}
	taskbarCreated := call(user, "RegisterWindowMessageW", uintptr(unsafe.Pointer(str("TaskbarCreated"))))
	if uintptr(msg) == taskbarCreated && a.settings != nil && hwnd == a.settings.hwnd && !a.testMode {
		a.tray = a.trayIcon(0)
		return 0
	}
	return call(user, "DefWindowProcW", hwnd, uintptr(msg), wp, lp)
}
