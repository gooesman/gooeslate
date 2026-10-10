package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gooesman/gooestranslate/internal/engine"
)

// Configuration changes must invalidate completions from the previous service.
func (a *appState) cancelWordRequests() {
	if a.lookupCancel != nil {
		a.lookupCancel()
		a.lookupCancel = nil
	}
	if a.vocabularyCancel != nil {
		a.vocabularyCancel()
		a.vocabularyCancel = nil
	}
	a.lookupGeneration++
	a.vocabularyGeneration++
}

func (a *appState) vocabularyLists(w *webWindow, req rpcRequest) {
	if w.kind != "settings" || a.testMode {
		a.resolveRPC(w, req.ID, nil, fmt.Errorf("请在生词本设置中获取列表"))
		return
	}
	cfg := a.cfg
	if len(req.Args) > 0 && string(req.Args[0]) != "null" {
		patch, _ := json.Marshal(map[string]json.RawMessage{"vocabulary": req.Args[0]})
		var err error
		cfg, _, err = mergeSettings(cfg, patch)
		if err != nil {
			a.resolveRPC(w, req.ID, nil, err)
			return
		}
	}
	go func() {
		categories, err := engine.ListVocabulary(context.Background(), engine.HTTPClient(), cfg.Vocabulary)
		a.post(func() { a.resolveRPC(w, req.ID, categories, err) })
	}()
}

func (a *appState) addVocabulary(word string, requestID int) {
	if a.vocabularyCancel != nil {
		a.vocabularyCancel()
	}
	a.vocabularyGeneration++
	generation := a.vocabularyGeneration
	cfg := a.cfg.Vocabulary
	timeout := cfg.TimeoutMS
	if timeout <= 0 {
		timeout = 30000
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
	a.vocabularyCancel = cancel
	go func() {
		message, err := engine.AddVocabulary(ctx, engine.HTTPClient(), cfg, word)
		cancel()
		a.post(func() {
			if generation != a.vocabularyGeneration {
				return
			}
			a.vocabularyCancel = nil
			status := map[string]any{"request_id": requestID, "ok": err == nil, "message": message}
			if err != nil {
				status["error"] = err.Error()
			}
			a.result.browser.Eval("window.setVocabularyStatus&&window.setVocabularyStatus(" + jsonString(status) + ")")
		})
	}()
}

func (a *appState) speechStatus(busy bool, err error) {
	if a.result == nil || !a.result.ready || a.exiting {
		return
	}
	status := map[string]any{"busy": busy, "ok": err == nil}
	if busy {
		status["message"] = "正在获取语音…"
	} else if err != nil {
		status["error"] = err.Error()
	} else {
		status["message"] = ""
	}
	a.result.browser.Eval("window.setSpeechStatus&&window.setSpeechStatus(" + jsonString(status) + ")")
}

func (a *appState) stopSpeech() {
	a.speechGeneration++
	if a.speechCancel != nil {
		a.speechCancel()
		a.speechCancel = nil
	}
	if a.speech != nil {
		a.speech.Stop()
	}
	if a.remoteSpeech != nil {
		a.remoteSpeech.Stop()
	}
	a.speechStatus(false, nil)
}

func (a *appState) speak(text string) error {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 50000 || strings.ContainsRune(text, '\x00') {
		return fmt.Errorf("请选择不超过 50000 个字符的朗读文本")
	}
	if a.testMode {
		return fmt.Errorf("界面自测不会播放音频")
	}
	a.stopSpeech()
	cfg := engine.ActiveTTS(a.cfg)
	if cfg.Provider == "system" {
		return a.speech.Speak(text, cfg.Voice, cfg.Rate)
	}
	timeout := cfg.TimeoutMS
	if timeout <= 0 {
		timeout = 90000
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
	a.speechCancel = cancel
	generation := a.speechGeneration
	a.speechStatus(true, nil)
	go func() {
		audio, format, err := engine.FetchSpeech(ctx, engine.HTTPClient(), cfg, text)
		if err == nil {
			err = a.remoteSpeech.PlayContext(ctx, audio, format)
		}
		cancel()
		a.post(func() {
			if generation != a.speechGeneration {
				return
			}
			a.speechCancel = nil
			a.speechStatus(false, err)
		})
	}()
	return nil
}
