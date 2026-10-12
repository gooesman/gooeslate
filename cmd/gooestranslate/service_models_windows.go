package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gooesman/gooeslate/internal/engine"
)

type modelCatalogResult struct {
	OK      bool     `json:"ok"`
	Models  []string `json:"models"`
	Message string   `json:"message,omitempty"`
}

func prepareModelCatalog(current engine.Config, raw json.RawMessage) (engine.Config, string, error) {
	var req serviceProbeRequest
	if err := decodeArgument(raw, &req); err != nil {
		return current, "", fmt.Errorf("模型查询参数无效")
	}
	switch req.Kind {
	case "translation", "ocr", "tts":
	default:
		return current, "", fmt.Errorf("此服务不支持获取模型列表")
	}
	// Reuse profile-specific credential merging. A new profile cannot inherit a
	// different profile's secret, and querying a draft does not save it.
	cfg, kind, err := prepareServiceProbe(current, raw)
	if err != nil {
		return current, "", fmt.Errorf("请先添加并填写要查询模型的服务")
	}
	return cfg, kind, nil
}

func (a *appState) listModels(w *webWindow, req rpcRequest) {
	if w.kind != "settings" {
		a.resolveRPC(w, req.ID, nil, fmt.Errorf("请在服务设置中获取模型列表"))
		return
	}
	if len(req.Args) == 0 {
		a.resolveRPC(w, req.ID, nil, fmt.Errorf("模型查询参数无效"))
		return
	}
	cfg, kind, err := prepareModelCatalog(a.cfg, req.Args[0])
	if err != nil {
		a.resolveRPC(w, req.ID, nil, err)
		return
	}
	if a.testMode {
		a.resolveRPC(w, req.ID, modelCatalogResult{Models: []string{}, Message: "界面自测未发出模型查询请求"}, nil)
		return
	}
	key := "models:" + serviceProbeKey(cfg, kind)
	if a.serviceProbes == nil {
		a.serviceProbes = map[string]serviceProbeTask{}
	}
	if task, exists := a.serviceProbes[key]; exists {
		task.cancel()
	} else if len(a.serviceProbes) >= 3 {
		a.resolveRPC(w, req.ID, nil, fmt.Errorf("已有 3 项服务正在连接，请等待完成"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	a.serviceProbes[key] = serviceProbeTask{cancel: cancel, id: req.ID}
	go func() {
		models, err := engine.ListModels(ctx, engine.HTTPClient(), cfg, kind)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("获取模型列表超时，请检查地址、网络或超时设置")
		} else if errors.Is(err, context.Canceled) {
			err = fmt.Errorf("模型列表查询已取消")
		}
		a.post(func() {
			if task, exists := a.serviceProbes[key]; exists && task.id == req.ID {
				delete(a.serviceProbes, key)
			}
			a.resolveRPC(w, req.ID, modelCatalogResult{OK: err == nil, Models: models}, err)
		})
	}()
}
