package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/gooesman/gooestranslate/internal/engine"
	"github.com/jchv/go-webview2/pkg/edge"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const version = "0.3.8"
const child = 0x40000000 | 0x10000000
const taskMessage = 0x8001
const trayMessage = 0x8002

//go:embed ui/*.html
var uiFiles embed.FS

type webWindow struct {
	hwnd            uintptr
	browser         *edge.Chromium
	kind            string
	ready           bool
	navigationOK    bool
	navigationError int32
	dragRegions     bool
}
type resultData struct {
	Image                string               `json:"image,omitempty"`
	Width                int                  `json:"width"`
	Height               int                  `json:"height"`
	Blocks               []engine.Block       `json:"blocks"`
	Lines                []resultLine         `json:"lines"`
	Translations         []engine.Translation `json:"translations"`
	OriginalText         string               `json:"original_text"`
	TranslatedText       string               `json:"translated_text"`
	Mode                 string               `json:"mode"`
	Status               string               `json:"status"`
	Busy                 bool                 `json:"busy"`
	Error                string               `json:"error"`
	Language             string               `json:"language"`
	LanguageSetting      string               `json:"language_setting"`
	SourceLanguage       string               `json:"source_language"`
	VocabularyEnabled    bool                 `json:"vocabulary_enabled"`
	CoordinatesSupported bool                 `json:"coordinates_supported"`
	ClipboardError       string               `json:"clipboard_error,omitempty"`
	Theme                string               `json:"theme"`
	DictionaryEnabled    bool                 `json:"dictionary_enabled"`
}
type resultLine struct {
	ID     int         `json:"id"`
	Text   string      `json:"text"`
	Bounds engine.Rect `json:"bounds"`
}
type appState struct {
	configurationRevision      uint64
	configurationBusy          bool
	pendingConfigurationImport *stagedConfigurationImport
	cfg                        engine.Config
	settings, result           *webWindow
	windows                    map[uintptr]*webWindow
	capture                    *CaptureController
	selection                  *SelectionController
	speech                     *SpeechService
	remoteSpeech               *RemoteSpeechService
	speechCancel               context.CancelFunc
	speechGeneration           uint64
	vocabularyCancel           context.CancelFunc
	vocabularyGeneration       uint64
	serviceProbes              map[string]serviceProbeTask
	serviceProbeResults        map[string]serviceProbeRecord
	captureClipboardError      string
	voices                     []SpeechVoice
	queue                      chan func()
	keys                       map[int]engine.Hotkey
	hotkeyErrors               []string
	shortcutRecording          bool
	loadError                  string
	tray                       bool
	icon                       uintptr
	img                        *image.RGBA
	snapshotID                 uint64
	lastImageSent              string
	data                       resultData
	lines                      []engine.Line
	generation                 uint64
	lookupGeneration           uint64
	cancel                     context.CancelFunc
	lookupCancel               context.CancelFunc
	pinned                     bool
	exiting                    bool
	testMode                   bool
	testExitCode               int
	uiProbe                    chan bool
	captureResultWasVisible    bool
}

var app *appState

func main() {
	if code := run(); code != 0 {
		os.Exit(code)
	}
}

func run() int {
	runtime.LockOSThread()
	if len(os.Args) == 3 && os.Args[1] == "--write-icon" {
		if err := writeApplicationIcon(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	if len(os.Args) > 1 && os.Args[1] == "--configure-stdin" {
		configureStdin()
		return 0
	}
	if len(os.Args) > 1 && (os.Args[1] == "--smoke-test" || os.Args[1] == "--live-test") {
		if e := smokeTest(); e != nil {
			fmt.Fprintln(os.Stderr, e)
			return 1
		}
		return 0
	}
	uiTest := len(os.Args) > 1 && (os.Args[1] == "--ui-smoke-test" || os.Args[1] == "--render-smoke-test")
	if !uiTest {
		handle, primary, err := acquireApplicationInstance(instanceMutexName(configFile()))
		if err != nil {
			fatal(err)
			return 1
		}
		defer call(kernel, "CloseHandle", handle)
		if !primary {
			if err := activateApplicationInstance(os.Args); err != nil {
				fatal(err)
				return 1
			}
			return 0
		}
	}
	call(user, "SetProcessDpiAwarenessContext", ^uintptr(3))
	ole32 := syscall.NewLazyDLL("ole32.dll")
	co := call(ole32, "CoInitializeEx", 0, 2)
	if int32(co) >= 0 {
		defer call(ole32, "CoUninitialize")
	}
	cfg := engine.DefaultConfig()
	var e error
	if !uiTest {
		cfg, e = loadConfig()
		if enabled, err := startupStatus(); err == nil {
			cfg.StartAtLogin = enabled
		}
	}
	app = &appState{cfg: cfg, windows: map[uintptr]*webWindow{}, queue: make(chan func(), 128), keys: map[int]engine.Hotkey{}, testMode: uiTest, uiProbe: make(chan bool, 4)}
	if !uiTest {
		app.serviceProbeResults = loadServiceProbeResults()
	}
	if e != nil {
		app.loadError = "设置读取失败，请检查文件；如密钥无法解密，请重新填写或清除密钥。"
	}
	app.speech = NewSpeechService()
	defer app.speech.Close()
	app.remoteSpeech = NewRemoteSpeechService()
	defer app.remoteSpeech.Close()
	app.voices, _ = app.speech.Voices()
	app.icon = createAppIcon()
	if app.icon != 0 {
		defer call(user, "DestroyIcon", app.icon)
	}
	if err := register("GooesTranslateWebV2", syscall.NewCallback(webProcedure), 32512); err != nil {
		fatal(err)
		return 1
	}
	app.settings, e = app.newWebWindow("settings", "gooestranslate · 设置", 1080, 760)
	if e != nil {
		fatal(e)
		return 1
	}
	app.result, e = app.newWebWindow("result", "gooestranslate · 识别与翻译", 840, 700)
	if e != nil {
		fatal(e)
		app.quit()
		return 1
	}
	app.capture = NewCaptureController(app.settings.hwnd, func(img *image.RGBA, mode string) { app.receiveImage(img, mode) }, func() {
		if app.captureResultWasVisible {
			app.showResult()
			app.pushResult()
		}
	})
	app.capture.SetOnComplete(func(img *image.RGBA, _ string) {
		app.captureClipboardError = ""
		if app.cfg.AutoCopyImage {
			if err := clipboardImage(app.settings.hwnd, img); err != nil {
				app.captureClipboardError = "截图已完成，但复制图片失败：" + err.Error()
			}
		}
	})
	if !uiTest {
		app.selection, e = NewSelectionController(app.settings.hwnd, app.post, func(text string, target uintptr) {
			if !app.cfg.Selection.Enabled || app.shortcutRecording || app.exiting || app.capture.Active() || call(user, "GetForegroundWindow") != target {
				return
			}
			app.receiveSelectedText(text)
		})
		if e != nil {
			app.loadError += " 划词按钮初始化失败，可使用划词快捷键：" + e.Error()
		} else {
			app.configureSelection()
		}
		app.hotkeyErrors = app.registerKeys(effectiveHotkeys(cfg))
		app.tray = app.trayIcon(0)
	}
	if uiTest {
		go app.runUIProbe()
	} else if showSettingsAtLaunch(os.Args, app.tray) {
		app.showSettings("")
	}
	if len(os.Args) > 2 && os.Args[1] == "--open" {
		app.openImage(os.Args[2], "ocr")
	}
	var m message
	for {
		r := int32(call(user, "GetMessageW", uintptr(unsafe.Pointer(&m)), 0, 0, 0))
		if r <= 0 {
			break
		}
		call(user, "TranslateMessage", uintptr(unsafe.Pointer(&m)))
		call(user, "DispatchMessageW", uintptr(unsafe.Pointer(&m)))
	}
	return app.testExitCode
}
func configureStdin() {
	c := engine.DefaultConfig()
	var input map[string]json.RawMessage
	e := json.NewDecoder(io.LimitReader(os.Stdin, 65536)).Decode(&input)
	if e == nil {
		raw, _ := json.Marshal(input)
		c, _, e = mergeSettings(c, raw)
	}
	if e == nil {
		e = saveConfig(c)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "配置保存失败")
		os.Exit(1)
	}
	fmt.Println("配置已加密保存")
}
func jsonString(v any) string { b, _ := json.Marshal(v); return string(b) }
func fatal(e error) {
	call(user, "MessageBoxW", 0, uintptr(unsafe.Pointer(str(e.Error()))), uintptr(unsafe.Pointer(str("gooestranslate"))), 0x10)
}
func workArea(h uintptr) rect {
	var m struct {
		Size          uint32
		Monitor, Work rect
		Flags         uint32
	}
	m.Size = uint32(unsafe.Sizeof(m))
	monitor := call(user, "MonitorFromWindow", h, 2)
	if call(user, "GetMonitorInfoW", monitor, uintptr(unsafe.Pointer(&m))) != 0 {
		return m.Work
	}
	return rect{0, 0, int32(call(user, "GetSystemMetrics", 0)), int32(call(user, "GetSystemMetrics", 1))}
}
func (a *appState) newWebWindow(kind, title string, width, height int) (*webWindow, error) {
	w := &webWindow{kind: kind}
	style := uintptr(0xcf0000 | 0x02000000)
	if kind == "result" {
		style = resultWindowStyle
	}
	w.hwnd = create("GooesTranslateWebV2", title, style, 0, 120, 90, width, height, 0, 0)
	if w.hwnd == 0 {
		return nil, fmt.Errorf("无法打开%s窗口", title)
	}
	a.windows[w.hwnd] = w
	applyWindowAppearance(w.hwnd, a.cfg.Theme)
	setWindowAppIcon(w.hwnd, a.icon)
	area := workArea(w.hwnd)
	dpi := float64(call(user, "GetDpiForWindow", w.hwnd)) / 96
	if dpi < 1 {
		dpi = 1
	}
	width = min(int(float64(width)*dpi), int(area.R-area.L)-40)
	height = min(int(float64(height)*dpi), int(area.B-area.T)-40)
	call(user, "SetWindowPos", w.hwnd, 0, u(int(area.L)+(int(area.R-area.L)-width)/2), u(int(area.T)+(int(area.B-area.T)-height)/2), u(width), u(height), 0x14)
	w.browser = edge.NewChromium()
	w.browser.DataPath = filepath.Join(filepath.Dir(configFile()), "WebView")
	if a.testMode {
		profile, err := filepath.Abs(filepath.Join(testDirectory(), "WebView-profile"))
		if err != nil {
			return nil, fmt.Errorf("无法创建独立测试界面目录：%w", err)
		}
		w.browser.DataPath = profile
	}
	w.browser.SetGlobalPermission(edge.CoreWebView2PermissionStateDeny)
	w.browser.NavigationCompletedCallback = func(_ *edge.ICoreWebView2, args *edge.ICoreWebView2NavigationCompletedEventArgs) {
		methods := *(**[5]uintptr)(unsafe.Pointer(args))
		var success, status int32
		syscall.SyscallN(methods[3], uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&success)))
		syscall.SyscallN(methods[4], uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&status)))
		w.navigationOK, w.navigationError = success != 0, status
	}
	// Leave the WebView COM callback before actions that may open native dialogs.
	w.browser.MessageCallback = func(raw string) {
		a.queue <- func() { a.rpc(w, raw) }
		call(user, "PostMessageW", w.hwnd, taskMessage, 0, 0)
	}
	if !w.browser.Embed(w.hwnd) {
		return nil, fmt.Errorf("界面无法启动，请安装 Microsoft Edge WebView2 Runtime")
	}
	w.ready = true
	w.browser.Hide()
	if s, e := w.browser.GetSettings(); e == nil {
		s.PutAreDefaultContextMenusEnabled(false)
		s.PutAreDevToolsEnabled(false)
		s.PutIsStatusBarEnabled(false)
		if kind == "result" {
			w.dragRegions = enableResultDragRegions(s)
		}
	}
	html, e := uiFiles.ReadFile("ui/" + kind + ".html")
	if e != nil {
		return nil, e
	}
	// The bridge must exist before page scripts run. Asynchronous Init followed
	// immediately by NavigateToString can race registration of the script.
	document := strings.Replace(string(html), "<head>", "<head><script>window.__nativeDragRegions="+jsonString(w.dragRegions)+";"+bridgeScript+"</script>", 1)
	w.browser.NavigateToString(document)
	w.browser.Resize()
	return w, nil
}

const bridgeScript = `(()=>{const pending=new Map();let seq=0;window.__resolve=(id,ok,value)=>{const p=pending.get(id);if(!p)return;pending.delete(id);ok?p.resolve(value):p.reject(new Error(value));};for(const method of ['getState','saveSettings','exportConfiguration','chooseConfigurationImport','applyConfigurationImport','cancelConfigurationImport','startCapture','chooseImage','hideWindow','quitApp','getResult','doResult','uiProbe','setShortcutRecording','vocabularyLists','openEudicAuthorization','testService','listModels']){window[method]=(...args)=>new Promise((resolve,reject)=>{const id=++seq;pending.set(id,{resolve,reject});window.chrome.webview.postMessage(JSON.stringify({id,method,args}));});}})();`

func (a *appState) post(f func()) {
	a.queue <- f
	call(user, "PostMessageW", a.settings.hwnd, taskMessage, 0, 0)
}
func (a *appState) showSettings(page string) {
	a.hideSelectionButton()
	if a.settings == nil {
		return
	}
	call(user, "ShowWindow", a.settings.hwnd, 9)
	a.settings.syncVisibility()
	a.settings.browser.Focus()
	call(user, "SetForegroundWindow", a.settings.hwnd)
	if page != "" {
		a.settings.browser.Eval("window.openSettingsPane&&window.openSettingsPane(" + jsonString(page) + ")")
	}
}
func (a *appState) showResult() {
	a.hideSelectionButton()
	if a.result == nil {
		return
	}
	command := uintptr(5)
	if call(user, "IsIconic", a.result.hwnd) != 0 {
		command = 9
	}
	call(user, "ShowWindow", a.result.hwnd, command)
	a.result.syncVisibility()
	a.result.browser.Focus()
	call(user, "SetWindowPos", a.result.hwnd, ^uintptr(0), 0, 0, 0, 0, 0x43)
	call(user, "SetForegroundWindow", a.result.hwnd)
	if !a.pinned {
		call(user, "SetTimer", a.result.hwnd, 7010, 1200, 0)
	}
}
func (a *appState) pushResult() {
	if a.result != nil && a.result.ready {
		update := a.data
		if update.Image == a.lastImageSent {
			update.Image = ""
		} else {
			a.lastImageSent = update.Image
		}
		a.result.browser.Eval("window.setResult&&window.setResult(" + jsonString(update) + ")")
	}
}
func (a *appState) receiveImage(img *image.RGBA, mode string) {
	a.stopRequest(false)
	a.stopSpeech()
	a.img = img
	a.snapshotID++
	snapshot := a.snapshotID
	cfg := a.cfg
	a.data = resultData{Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), Mode: mode, Language: cfg.Language, LanguageSetting: cfg.Language, VocabularyEnabled: cfg.Vocabulary.Enabled, CoordinatesSupported: engine.ActiveOCR(cfg).Coordinates, ClipboardError: a.captureClipboardError, Theme: cfg.Theme, DictionaryEnabled: cfg.Dictionary.Enabled, Blocks: []engine.Block{}, Lines: []resultLine{}, Translations: []engine.Translation{}, Status: "正在识别…"}
	a.captureClipboardError = ""
	a.lastImageSent = ""
	a.lines = nil
	a.result.browser.Eval("window.setResult&&window.setResult({image:'',original_text:'',translated_text:'',blocks:[],lines:[],translations:[]})")
	a.showResult()
	a.pushResult()
	// Display the result window immediately; large-image encoding stays off the UI thread.
	go func() {
		var b bytes.Buffer
		encoder := png.Encoder{CompressionLevel: png.BestSpeed}
		if e := encoder.Encode(&b, img); e != nil {
			return
		}
		encoded := "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
		a.post(func() {
			if snapshot != a.snapshotID {
				return
			}
			a.data.Image = encoded
			a.pushResult()
		})
	}()
	a.recognize(mode)
}
func (a *appState) newTask(timeoutMS int) (context.Context, uint64) {
	a.generation++
	if timeoutMS <= 0 {
		timeoutMS = 90000
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	a.cancel = cancel
	a.data.Busy = true
	a.data.Error = ""
	return ctx, a.generation
}
func (a *appState) recognize(mode string) {
	if a.img == nil {
		return
	}
	cfg := a.cfg
	ctx, generation := a.newTask(engine.ActiveOCR(cfg).TimeoutMS)
	img := a.img
	a.data.Status = "正在识别图片中的文字…"
	a.pushResult()
	go func() {
		blocks, e := engine.RecognizeConfigured(ctx, engine.HTTPClient(), cfg, img)
		a.post(func() {
			if generation != a.generation {
				return
			}
			a.cancel()
			a.cancel = nil
			a.data.Busy = false
			if e != nil {
				a.fail(e)
				return
			}
			a.lines = engine.GroupLines(blocks)
			a.data.Blocks = blocks
			a.data.Lines = []resultLine{}
			for _, l := range a.lines {
				a.data.Lines = append(a.data.Lines, resultLine{l.ID, l.Text, l.Bounds})
			}
			a.data.OriginalText = engine.Text(blocks)
			a.data.SourceLanguage = engine.DetectSourceLanguage(a.data.OriginalText)
			a.data.Language = engine.ResolveTargetLanguage(a.data.LanguageSetting, a.data.OriginalText)
			a.data.Status = fmt.Sprintf("识别完成 · %d 个文字块", len(blocks))
			if cfg.AutoCopyOCR && a.data.OriginalText != "" {
				clipboard(a.result.hwnd, a.data.OriginalText)
			}
			a.pushResult()
			if mode == "translate" || mode == "image" {
				a.translate(a.data.LanguageSetting)
			}
		})
	}()
}
func (a *appState) translate(language string) {
	if a.data.Busy {
		return
	}
	if len(a.lines) == 0 {
		a.fail(fmt.Errorf("没有可翻译的文字，请先截图识别"))
		return
	}
	cfg := a.cfg
	if language != "" {
		cfg.Language = language
	}
	a.data.LanguageSetting = cfg.Language
	a.data.Language = engine.ResolveTargetLanguage(cfg.Language, a.data.OriginalText)
	a.data.Translations = []engine.Translation{}
	a.data.TranslatedText = ""
	ctx, generation := a.newTask(engine.ActiveTranslation(cfg).TimeoutMS)
	lines := append([]engine.Line(nil), a.lines...)
	a.data.Status = "正在翻译…"
	a.pushResult()
	go func() {
		values, e := engine.Translate(ctx, engine.HTTPClient(), cfg, lines)
		a.post(func() {
			if generation != a.generation {
				return
			}
			a.cancel()
			a.cancel = nil
			a.data.Busy = false
			if e != nil {
				a.fail(e)
				return
			}
			a.data.Translations = values
			var texts []string
			for _, v := range values {
				texts = append(texts, v.Text)
			}
			a.data.TranslatedText = strings.Join(texts, "\n")
			a.data.Status = "翻译完成"
			a.pushResult()
			if engine.ActiveTTS(cfg).AutoRead {
				a.speak(a.data.TranslatedText)
			}
		})
	}()
}
func (a *appState) fail(e error) {
	a.data.Busy = false
	a.data.Error = e.Error()
	a.data.Status = e.Error()
	a.pushResult()
}
func (a *appState) stopRequest(notify bool) {
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	a.cancelWordRequests()
	a.generation++
	a.data.Busy = false
	if notify {
		a.data.Status = "已取消"
		a.pushResult()
	}
}
func (a *appState) lookup(word string, requestID int) {
	if !a.cfg.Dictionary.Enabled {
		a.result.browser.Eval("window.setDictionary&&window.setDictionary(" + jsonString(map[string]any{"request_id": requestID, "error": "请先在设置中启用词典"}) + ")")
		return
	}
	if a.lookupCancel != nil {
		a.lookupCancel()
	}
	a.lookupGeneration++
	generation := a.lookupGeneration
	timeout := engine.ActiveTranslation(a.cfg).TimeoutMS
	if a.cfg.Dictionary.Provider == "eudic" {
		timeout = a.cfg.Vocabulary.TimeoutMS
	}
	if timeout <= 0 {
		timeout = 45000
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
	a.lookupCancel = cancel
	cfg := a.cfg
	go func() {
		entry, e := engine.LookupDictionary(ctx, engine.HTTPClient(), cfg, word)
		a.post(func() {
			if generation != a.lookupGeneration {
				return
			}
			cancel()
			a.lookupCancel = nil
			if e != nil {
				a.result.browser.Eval("window.setDictionary&&window.setDictionary(" + jsonString(map[string]any{"request_id": requestID, "word": word, "error": e.Error()}) + ")")
			} else {
				a.result.browser.Eval("window.setDictionary&&window.setDictionary(" + jsonString(map[string]any{"request_id": requestID, "word": entry.Word, "phonetic": entry.Phonetic, "definitions": entry.Definitions, "source": entry.Source}) + ")")
			}
		})
	}()
}
func (a *appState) startCapture(mode string) bool {
	if a.capture == nil {
		return false
	}
	if mode == "" {
		mode = a.cfg.DefaultAction
	}
	if mode != "choose" && mode != "ocr" && mode != "translate" && mode != "image" {
		return false
	}
	if a.capture.Active() {
		return false
	}
	a.hideSelectionButton()
	a.stopRequest(false)
	a.setShortcutRecording(false)
	a.stopSpeech()
	a.captureResultWasVisible = a.result != nil && call(user, "IsWindowVisible", a.result.hwnd) != 0
	if a.result != nil {
		call(user, "ShowWindow", a.result.hwnd, 0)
	}
	if e := a.capture.Start(mode); e != nil {
		a.fail(e)
		if a.captureResultWasVisible {
			a.showResult()
		}
		return false
	}
	return true
}
func (a *appState) openImage(file, mode string) error {
	f, e := os.Open(file)
	if e != nil {
		return fmt.Errorf("图片打开失败")
	}
	defer f.Close()
	cfg, _, e := image.DecodeConfig(f)
	if e != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40000000 {
		return fmt.Errorf("请使用不超过 4000 万像素的 PNG 或 JPEG 图片")
	}
	f.Seek(0, 0)
	img, _, e := image.Decode(f)
	if e != nil {
		return fmt.Errorf("图片损坏或格式不支持")
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Over)
	if mode == "choose" {
		mode = "ocr"
	}
	a.receiveImage(rgba, mode)
	return nil
}
func (a *appState) quit() {
	if a.exiting {
		return
	}
	a.stopRequest(false)
	a.cancelServiceProbes()
	a.exiting = true
	if a.selection != nil {
		a.selection.Close()
	}
	if a.capture != nil {
		a.capture.Close()
	}
	a.stopSpeech()
	for id := range a.keys {
		call(user, "UnregisterHotKey", a.settings.hwnd, u(id))
	}
	if a.tray {
		a.trayIcon(2)
	}
	// Close both WebView controllers before destroying their parent windows.
	for _, w := range a.windows {
		if w.ready && w.browser.GetController() != nil {
			controller := w.browser.GetController()
			w.ready = false
			methods := *(**[26]uintptr)(unsafe.Pointer(controller))
			syscall.SyscallN(methods[24], uintptr(unsafe.Pointer(controller)))
		}
	}
	if a.result != nil {
		call(user, "DestroyWindow", a.result.hwnd)
	}
	if a.settings != nil {
		call(user, "DestroyWindow", a.settings.hwnd)
	}
	call(user, "PostQuitMessage", 0)
}
