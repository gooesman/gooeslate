package main

import (
	"fmt"

	"github.com/gooesman/gooestranslate/internal/engine"
)

func effectiveHotkeys(cfg engine.Config) engine.Hotkeys {
	keys := cfg.Hotkeys
	if !cfg.Selection.Enabled {
		keys.Selection = ""
	}
	return keys
}

func (a *appState) hideSelectionButton() {
	if a.selection != nil {
		a.selection.Hide()
	}
}

func (a *appState) configureSelection() {
	if a.selection != nil {
		a.selection.SetEnabled(a.cfg.Selection.Enabled && !a.shortcutRecording && !a.testMode && !a.exiting, a.cfg.Selection.ShowButton)
	}
}

func (a *appState) startSelectionTranslation() {
	if !a.cfg.Selection.Enabled || a.shortcutRecording || a.testMode || a.exiting || a.capture != nil && a.capture.Active() {
		return
	}
	target := call(user, "GetForegroundWindow")
	a.hideSelectionButton()
	a.stopRequest(false)
	token := a.generation
	// Leave focus in the source application until reading (and any clipboard
	// restoration) is complete. Never translate the old clipboard contents.
	go func() {
		text, err := readSelectedText(target, true)
		a.post(func() {
			if a.exiting || token != a.generation || !a.cfg.Selection.Enabled || call(user, "GetForegroundWindow") != target {
				return
			}
			if err != nil {
				a.showSelectionError(err)
				return
			}
			a.receiveSelectedText(text)
		})
	}()
}

// selectedTextResult has no image/OCR dependencies. It preserves paragraphs
// as one translation input while reusing language detection and all result actions.
func selectedTextResult(cfg engine.Config, text string) (resultData, []engine.Line, error) {
	text, err := selectionText(text)
	if err != nil {
		return resultData{}, nil, err
	}
	if text == "" {
		return resultData{}, nil, fmt.Errorf("没有读取到选中文字，请先选择文字，再按划词翻译快捷键")
	}
	lines := []engine.Line{{ID: 0, Text: text}}
	data := resultData{Mode: "selection", LanguageSetting: cfg.Language, Language: engine.ResolveTargetLanguage(cfg.Language, text), SourceLanguage: engine.DetectSourceLanguage(text), OriginalText: text, Status: "已读取选中文字", Theme: cfg.Theme, DictionaryEnabled: cfg.Dictionary.Enabled, VocabularyEnabled: cfg.Vocabulary.Enabled, Blocks: []engine.Block{}, Lines: []resultLine{{ID: 0, Text: text}}, Translations: []engine.Translation{}}
	return data, lines, nil
}

func (a *appState) resetSelectionResult() {
	a.stopRequest(false)
	a.stopSpeech()
	a.hideSelectionButton()
	a.img = nil
	a.snapshotID++ // Invalidate any pending screenshot encoding.
	a.lastImageSent = ""
	a.captureClipboardError = ""
	if a.result != nil && a.result.ready {
		a.result.browser.Eval("window.setResult&&window.setResult({image:'',original_text:'',translated_text:'',blocks:[],lines:[],translations:[]});window.setDictionary&&window.setDictionary({reset:true});window.setVocabularyStatus&&window.setVocabularyStatus({reset:true})")
	}
}

func (a *appState) receiveSelectedText(text string) {
	data, lines, err := selectedTextResult(a.cfg, text)
	if err != nil {
		a.showSelectionError(err)
		return
	}
	a.resetSelectionResult()
	a.data, a.lines = data, lines
	a.showResult()
	a.pushResult()
	a.translate(a.cfg.Language)
}

func (a *appState) showSelectionError(err error) {
	a.resetSelectionResult()
	a.lines = nil
	a.data = resultData{Mode: "selection", Theme: a.cfg.Theme, LanguageSetting: a.cfg.Language, DictionaryEnabled: a.cfg.Dictionary.Enabled, VocabularyEnabled: a.cfg.Vocabulary.Enabled, Blocks: []engine.Block{}, Lines: []resultLine{}, Translations: []engine.Translation{}, Error: err.Error(), Status: err.Error()}
	a.showResult()
	a.pushResult()
}
