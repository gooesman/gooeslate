package main

import (
	"fmt"
	"strings"

	"github.com/gooesman/gooestranslate/internal/engine"
)

// Validate before touching a live translation so an invalid or blank input
// cannot erase its result or cancel the request the user is still waiting for.
func manualTextResult(cfg engine.Config, text, language string) (resultData, []engine.Line, error) {
	clean, err := selectionText(text)
	if err != nil {
		return resultData{}, nil, fmt.Errorf("%s", strings.ReplaceAll(err.Error(), "选中文字", "输入内容"))
	}
	if clean == "" {
		return resultData{}, nil, fmt.Errorf("请先输入要翻译的文本或单词")
	}
	if language != "" {
		cfg.Language = language
	}
	data, lines, err := selectedTextResult(cfg, clean)
	if err != nil {
		return resultData{}, nil, err
	}
	data.ManualInput = true
	data.Status = "准备翻译输入的文字"
	return data, lines, nil
}

func (a *appState) prepareManualText(text, language string, requestID int) error {
	if requestID < 0 {
		return fmt.Errorf("翻译请求编号无效，请重试")
	}
	data, lines, err := manualTextResult(a.cfg, text, language)
	if err != nil {
		return err
	}
	document := a.data.DocumentID
	a.stopRequest(false)
	a.stopSpeech()
	a.hideSelectionButton()
	a.img = nil
	a.snapshotID++ // A late screenshot encoding must not restore the old image.
	a.lastImageSent = ""
	a.captureClipboardError = ""
	if document == 0 {
		document = a.snapshotID
	}
	data.DocumentID, data.TextRequestID = document, requestID
	a.data, a.lines = data, lines
	if a.result != nil && a.result.ready {
		// A manual edit has no reliable connection to old OCR coordinates. Clear
		// only image-related state; leave the user's newer editor draft intact.
		a.result.browser.Eval("window.setResult&&window.setResult({image:'',blocks:[],lines:[],translations:[]});window.setDictionary&&window.setDictionary({reset:true});window.setVocabularyStatus&&window.setVocabularyStatus({reset:true})")
	}
	return nil
}

func (a *appState) showTextInput() {
	a.resetSelectionResult()
	a.lines = nil
	a.data = resultData{DocumentID: a.snapshotID, Mode: "selection", ManualInput: true, Theme: a.cfg.Theme, LanguageSetting: a.cfg.Language, Language: engine.ResolveTargetLanguage(a.cfg.Language, ""), DictionaryEnabled: a.cfg.Dictionary.Enabled, VocabularyEnabled: a.cfg.Vocabulary.Enabled, Blocks: []engine.Block{}, Lines: []resultLine{}, Translations: []engine.Translation{}, Status: "输入文本或单词，点击翻译或按 Ctrl+Enter"}
	a.showResult()
	a.pushResult()
	if a.result != nil && a.result.ready {
		a.result.browser.Eval("requestAnimationFrame(()=>document.getElementById('original')?.focus())")
	}
}
