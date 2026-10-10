package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gooesman/gooestranslate/internal/engine"
)

type serviceProbeRequest struct {
	Kind               string          `json:"kind"`
	Service            json.RawMessage `json:"service"`
	TranslationService json.RawMessage `json:"translation_service"`
	Vocabulary         json.RawMessage `json:"vocabulary"`
	Language           string          `json:"language"`
}

type serviceProbeTask struct {
	cancel context.CancelFunc
	id     int
}

// Probe a snapshot of the draft. Saved credentials are retained only for the
// same profile ID, and neither the draft nor the result changes saved settings.
func prepareServiceProbe(current engine.Config, raw json.RawMessage) (engine.Config, string, error) {
	var req serviceProbeRequest
	if err := decodeArgument(raw, &req); err != nil || len(req.Service) == 0 || string(req.Service) == "null" {
		return current, "", fmt.Errorf("服务测试参数无效")
	}
	patch := map[string]json.RawMessage{}
	profile := func(kind string, service json.RawMessage) error {
		var identity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(service, &identity); err != nil || strings.TrimSpace(identity.ID) == "" {
			return fmt.Errorf("请先添加并填写服务")
		}
		listField, activeField := kind+"_services", "active_"+kind
		if kind == "tts" {
			listField, activeField = "tts_services", "active_tts"
		}
		patch[listField], _ = json.Marshal([]json.RawMessage{service})
		patch[activeField], _ = json.Marshal(identity.ID)
		return nil
	}
	switch req.Kind {
	case "translation", "ocr", "tts":
		if err := profile(req.Kind, req.Service); err != nil {
			return current, "", err
		}
	case "vocabulary":
		patch["vocabulary"] = req.Service
	case "dictionary":
		patch["dictionary"] = req.Service
		if len(req.TranslationService) > 0 && string(req.TranslationService) != "null" {
			if err := profile("translation", req.TranslationService); err != nil {
				return current, "", err
			}
		}
		if len(req.Vocabulary) > 0 && string(req.Vocabulary) != "null" {
			patch["vocabulary"] = req.Vocabulary
		}
		if req.Language != "" {
			patch["language"], _ = json.Marshal(req.Language)
		}
	default:
		return current, "", fmt.Errorf("不支持的服务测试类型")
	}
	if req.Language != "" {
		patch["language"], _ = json.Marshal(req.Language)
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		return current, "", fmt.Errorf("服务测试参数无效")
	}
	cfg, _, err := mergeSettings(current, encoded)
	if err == nil && req.Kind == "tts" && engine.ActiveTTS(cfg).Provider == "system" {
		p := engine.ActiveTTS(cfg)
		if p.Rate < -10 || p.Rate > 10 {
			return current, "", fmt.Errorf("系统语速范围为 -10 到 10")
		}
	}
	return cfg, req.Kind, err
}

func (a *appState) testService(w *webWindow, req rpcRequest) {
	if w.kind != "settings" {
		a.resolveRPC(w, req.ID, nil, fmt.Errorf("请在设置窗口测试服务"))
		return
	}
	if len(req.Args) == 0 {
		a.resolveRPC(w, req.ID, nil, fmt.Errorf("服务测试参数无效"))
		return
	}
	cfg, kind, err := prepareServiceProbe(a.cfg, req.Args[0])
	if err != nil {
		a.resolveRPC(w, req.ID, nil, err)
		return
	}
	if a.testMode {
		a.resolveRPC(w, req.ID, engine.ProbeResult{Kind: kind, OK: false, Message: "界面自测未发出服务请求"}, nil)
		return
	}
	key := serviceProbeKey(cfg, kind)
	if a.serviceProbes == nil {
		a.serviceProbes = map[string]serviceProbeTask{}
	}
	if task, exists := a.serviceProbes[key]; exists {
		task.cancel()
	} else if len(a.serviceProbes) >= 3 {
		a.resolveRPC(w, req.ID, nil, fmt.Errorf("已有 3 项服务正在测试，请等待完成"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	a.serviceProbes[key] = serviceProbeTask{cancel: cancel, id: req.ID}
	go func() {
		var result engine.ProbeResult
		if kind == "tts" && engine.ActiveTTS(cfg).Provider == "system" {
			result = a.probeSystemSpeech(cfg)
		} else {
			result = engine.TestService(ctx, engine.HTTPClient(), cfg, kind)
		}
		canceled := ctx.Err() == context.Canceled
		cancel()
		a.post(func() {
			task, exists := a.serviceProbes[key]
			current := exists && task.id == req.ID
			if current {
				delete(a.serviceProbes, key)
			}
			if !canceled && current {
				if a.serviceProbeResults == nil {
					a.serviceProbeResults = map[string]serviceProbeRecord{}
				}
				a.serviceProbeResults[key] = serviceProbeRecord{Fingerprint: serviceProbeFingerprint(cfg, kind), Result: result}
				if err := saveServiceProbeResults(a.serviceProbeResults); err != nil {
					result.Message += "；本次测试记录未保存"
				}
			}
			a.resolveRPC(w, req.ID, result, nil)
		})
	}()
}

func (a *appState) probeSystemSpeech(cfg engine.Config) engine.ProbeResult {
	start := time.Now()
	p := engine.ActiveTTS(cfg)
	result := engine.ProbeResult{Kind: "tts", ServiceID: p.ID, TestedAt: start.UTC().Format(time.RFC3339)}
	voices, err := a.speech.Voices()
	result.ElapsedMS = time.Since(start).Milliseconds()
	if err != nil || len(voices) == 0 {
		result.Message = "系统语音不可用，请检查 Windows 语音包"
		return result
	}
	if p.Voice != "" {
		found := false
		for _, voice := range voices {
			if voice.ID == p.Voice {
				found = true
				break
			}
		}
		if !found {
			result.Message = "已选发音人不存在，请重新选择"
			return result
		}
	}
	result.OK = true
	result.Message = "系统语音可用，已检查发音人；未播放声音"
	return result
}

func (a *appState) cancelServiceProbes() {
	for key, task := range a.serviceProbes {
		task.cancel()
		delete(a.serviceProbes, key)
	}
}
