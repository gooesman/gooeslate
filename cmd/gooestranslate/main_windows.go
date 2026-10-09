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

const version = "0.2.0"
const child = 0x40000000 | 0x10000000
const taskMessage = 0x8001
const trayMessage = 0x8002

//go:embed ui/*.html
var uiFiles embed.FS

type webWindow struct {
	hwnd    uintptr
	browser *edge.Chromium
	kind    string
	ready   bool
}
type resultData struct {
	Image             string               `json:"image,omitempty"`
	Width             int                  `json:"width"`
	Height            int                  `json:"height"`
	Blocks            []engine.Block       `json:"blocks"`
	Lines             []resultLine         `json:"lines"`
	Translations      []engine.Translation `json:"translations"`
	OriginalText      string               `json:"original_text"`
	TranslatedText    string               `json:"translated_text"`
	Mode              string               `json:"mode"`
	Status            string               `json:"status"`
	Busy              bool                 `json:"busy"`
	Error             string               `json:"error"`
	Language          string               `json:"language"`
	Theme             string               `json:"theme"`
	DictionaryEnabled bool                 `json:"dictionary_enabled"`
}
type resultLine struct {
	ID     int         `json:"id"`
	Text   string      `json:"text"`
	Bounds engine.Rect `json:"bounds"`
}
type appState struct {
	cfg                     engine.Config
	settings, result        *webWindow
	windows                 map[uintptr]*webWindow
	capture                 *CaptureController
	speech                  *SpeechService
	voices                  []SpeechVoice
	queue                   chan func()
	keys                    map[int]engine.Hotkey
	hotkeyErrors            []string
	shortcutRecording       bool
	loadError               string
	tray                    bool
	icon                    uintptr
	img                     *image.RGBA
	snapshotID              uint64
	lastImageSent           string
	data                    resultData
	lines                   []engine.Line
	generation              uint64
	lookupGeneration        uint64
	cancel                  context.CancelFunc
	lookupCancel            context.CancelFunc
	pinned                  bool
	exiting                 bool
	testMode                bool
	uiProbe                 chan bool
	captureResultWasVisible bool
}

var app *appState

func main() {
	runtime.LockOSThread()
	if len(os.Args) > 1 && os.Args[1] == "--configure-stdin" {
		configureStdin()
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "--smoke-test" || os.Args[1] == "--live-test") {
		if e := smokeTest(); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	}
	uiTest := len(os.Args) > 1 && os.Args[1] == "--ui-smoke-test"
	call(user, "SetProcessDpiAwarenessContext", ^uintptr(3))
	ole32 := syscall.NewLazyDLL("ole32.dll")
	co := call(ole32, "CoInitializeEx", 0, 2)
	if int32(co) >= 0 {
		defer call(ole32, "CoUninitialize")
	}
	cfg, e := loadConfig()
	if uiTest {
		cfg = engine.DefaultConfig()
		e = nil
	}
	app = &appState{cfg: cfg, windows: map[uintptr]*webWindow{}, queue: make(chan func(), 128), keys: map[int]engine.Hotkey{}, testMode: uiTest, uiProbe: make(chan bool, 4)}
	if e != nil {
		app.loadError = "设置读取失败，请检查文件；如密钥无法解密，请重新填写或清除密钥。"
	}
	app.speech = NewSpeechService()
	defer app.speech.Close()
	app.voices, _ = app.speech.Voices()
	app.icon = createAppIcon()
	if app.icon != 0 {
		defer call(user, "DestroyIcon", app.icon)
	}
	if err := register("GooesTranslateWebV2", syscall.NewCallback(webProcedure), 32512); err != nil {
		fatal(err)
		return
	}
	app.settings, e = app.newWebWindow("settings", "gooestranslate · 设置", 1080, 760)
	if e != nil {
		fatal(e)
		return
	}
	app.result, e = app.newWebWindow("result", "gooestranslate · 识别与翻译", 840, 700)
	if e != nil {
		fatal(e)
		return
	}
	app.capture = NewCaptureController(app.settings.hwnd, func(img *image.RGBA, mode string) { app.receiveImage(img, mode) }, func() {
		if app.captureResultWasVisible {
			app.showResult()
			app.pushResult()
		}
	})
	if !uiTest {
		app.hotkeyErrors = app.registerKeys(cfg.Hotkeys)
		app.tray = app.trayIcon(0)
	}
	if uiTest {
		go app.runUIProbe()
	} else {
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
}
func configureStdin() {
	c := engine.DefaultConfig()
	var input map[string]json.RawMessage
	e := json.NewDecoder(io.LimitReader(os.Stdin, 65536)).Decode(&input)
	if e == nil {
		raw, _ := json.Marshal(input)
		e = json.Unmarshal(raw, &c)
		if k, ok := input["api_key"]; ok {
			json.Unmarshal(k, &c.APIKey)
		}
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
	w.hwnd = create("GooesTranslateWebV2", title, 0xcf0000|0x02000000, 0, 120, 90, width, height, 0, 0)
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
	w.browser.SetGlobalPermission(edge.CoreWebView2PermissionStateDeny)
	// Leave the WebView COM callback before actions that may open native dialogs.
	w.browser.MessageCallback = func(raw string) {
		a.queue <- func() { a.rpc(w, raw) }
		call(user, "PostMessageW", w.hwnd, taskMessage, 0, 0)
	}
	if !w.browser.Embed(w.hwnd) {
		return nil, fmt.Errorf("界面无法启动，请安装 Microsoft Edge WebView2 Runtime")
	}
	w.ready = true
	if s, e := w.browser.GetSettings(); e == nil {
		s.PutAreDefaultContextMenusEnabled(false)
		s.PutAreDevToolsEnabled(false)
		s.PutIsStatusBarEnabled(false)
	}
	w.browser.Init(bridgeScript)
	html, e := uiFiles.ReadFile("ui/" + kind + ".html")
	if e != nil {
		return nil, e
	}
	w.browser.NavigateToString(string(html))
	w.browser.Resize()
	return w, nil
}

const bridgeScript = `(()=>{const pending=new Map();let seq=0;window.__resolve=(id,ok,value)=>{const p=pending.get(id);if(!p)return;pending.delete(id);ok?p.resolve(value):p.reject(new Error(value));};for(const method of ['getState','saveSettings','startCapture','chooseImage','hideWindow','quitApp','getResult','doResult','uiProbe','setShortcutRecording']){window[method]=(...args)=>new Promise((resolve,reject)=>{const id=++seq;pending.set(id,{resolve,reject});window.chrome.webview.postMessage(JSON.stringify({id,method,args}));});}})();`

func (a *appState) post(f func()) {
	a.queue <- f
	call(user, "PostMessageW", a.settings.hwnd, taskMessage, 0, 0)
}
func (a *appState) showSettings(page string) {
	if a.settings == nil {
		return
	}
	call(user, "ShowWindow", a.settings.hwnd, 9)
	call(user, "SetForegroundWindow", a.settings.hwnd)
	if page != "" {
		a.settings.browser.Eval("window.openSettingsPane&&window.openSettingsPane(" + jsonString(page) + ")")
	}
}
func (a *appState) showResult() {
	if a.result == nil {
		return
	}
	call(user, "ShowWindow", a.result.hwnd, 9)
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
	a.img = img
	a.snapshotID++
	snapshot := a.snapshotID
	cfg := a.cfg
	a.data = resultData{Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), Mode: mode, Language: cfg.Language, Theme: cfg.Theme, DictionaryEnabled: cfg.Dictionary.Enabled, Blocks: []engine.Block{}, Lines: []resultLine{}, Translations: []engine.Translation{}, Status: "正在识别…"}
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
func (a *appState) newTask() (context.Context, uint64) {
	a.generation++
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	ctx, generation := a.newTask()
	img := a.img
	a.data.Status = "正在识别图片中的文字…"
	a.pushResult()
	go func() {
		blocks, e := engine.Recognize(ctx, engine.HTTPClient(), cfg.OCRURL, img)
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
			a.data.Status = fmt.Sprintf("识别完成 · %d 个文字块", len(blocks))
			if cfg.AutoCopyOCR && a.data.OriginalText != "" {
				clipboard(a.result.hwnd, a.data.OriginalText)
			}
			a.pushResult()
			if mode == "translate" || mode == "image" {
				a.translate(a.data.Language)
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
	a.data.Language = cfg.Language
	a.data.Translations = []engine.Translation{}
	a.data.TranslatedText = ""
	ctx, generation := a.newTask()
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
			if cfg.TTS.AutoRead {
				a.speech.Speak(a.data.TranslatedText, cfg.TTS.Voice, cfg.TTS.Rate)
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
	if a.lookupCancel != nil {
		a.lookupCancel()
		a.lookupCancel = nil
	}
	a.generation++
	a.lookupGeneration++
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
	a.setShortcutRecording(false)
	a.speech.Stop()
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
	a.exiting = true
	if a.capture != nil {
		a.capture.Close()
	}
	a.speech.Stop()
	for id := range a.keys {
		call(user, "UnregisterHotKey", a.settings.hwnd, u(id))
	}
	if a.tray {
		a.trayIcon(2)
	}
	if a.result != nil {
		call(user, "DestroyWindow", a.result.hwnd)
	}
	if a.settings != nil {
		call(user, "DestroyWindow", a.settings.hwnd)
	}
	call(user, "PostQuitMessage", 0)
}
