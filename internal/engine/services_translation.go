package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func translationVariables(cfg Config, p TranslationService, lines []Line) templateVariables {
	texts := make([]string, len(lines))
	for i, line := range lines {
		texts[i] = line.Text
	}
	text := strings.Join(texts, "\n")
	return templateVariables{"lines": lines, "messages": []map[string]string{}, "text": text, "target_language": ResolveTargetLanguage(cfg.Language, text), "source_language": DetectSourceLanguage(text), "model": p.Model, "api_key": p.APIKey, "image": "", "image_base64": "", "image_data_url": "", "voice": "", "speed": 1.0}
}
func translationPayload(p TranslationService, vars templateVariables, systemPrompt, userPrompt string) (map[string]any, error) {
	system, err := renderTemplateText(systemPrompt, vars)
	if err != nil {
		return nil, err
	}
	user, err := renderTemplateText(userPrompt, vars)
	if err != nil {
		return nil, err
	}
	vars["messages"] = []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}
	payload := map[string]any{"model": p.Model, "temperature": p.Temperature, "stream": false, "messages": vars["messages"]}
	if p.RequestMode == "custom" {
		if strings.TrimSpace(p.BodyTemplate) == "" {
			return nil, errors.New("请填写自定义 JSON 请求体模板")
		}
		payload, err = templateObject(p.BodyTemplate, vars)
		if err != nil {
			return nil, err
		}
	}
	if err = rejectStream(payload); err != nil {
		return nil, err
	}
	if err = disableThinking(payload, p.DisableThinking); err != nil {
		return nil, err
	}
	return payload, nil
}
func translationRequest(ctx context.Context, client *http.Client, cfg Config, lines []Line, system, user string) (any, error) {
	p := ActiveTranslation(cfg)
	if p.BaseURL == "" {
		return nil, errors.New("请先配置当前翻译服务的地址")
	}
	if p.RequestMode != "custom" && strings.TrimSpace(p.Model) == "" {
		return nil, errors.New("请填写翻译模型")
	}
	vars := translationVariables(cfg, p, lines)
	payload, err := translationPayload(p, vars, system, user)
	if err != nil {
		return nil, err
	}
	return executeTranslation(ctx, client, p, vars, payload)
}
func executeTranslation(ctx context.Context, client *http.Client, p TranslationService, vars templateVariables, payload map[string]any) (any, error) {
	endpoint := chatEndpoint(p.BaseURL)
	if p.Provider == "http" {
		endpoint = strings.TrimSpace(p.BaseURL)
	}
	ctx, cancel := serviceContext(ctx, p.TimeoutMS)
	defer cancel()
	raw, err := postServiceJSON(ctx, client, endpoint, p.APIKey, p.Headers, vars, payload)
	if err != nil {
		return nil, err
	}
	return responseValue(raw, p.ResponsePath)
}
func translateConfigured(ctx context.Context, client *http.Client, cfg Config, lines []Line) ([]Translation, error) {
	if len(lines) == 0 || len(lines) > 300 {
		return nil, errors.New("每次支持 1–300 行文字")
	}
	input, err := json.Marshal(lines)
	if err != nil || len(input) > 60000 {
		return nil, errors.New("文字过多，请缩小截图区域")
	}
	p := ActiveTranslation(cfg)
	value, err := translationRequest(ctx, client, cfg, lines, p.SystemPrompt, p.UserPrompt)
	if err != nil {
		return nil, err
	}
	content, err := contentJSON(value)
	if err != nil {
		return nil, err
	}
	if p.RequestMode == "custom" || p.Provider == "http" {
		trimmed := strings.TrimSpace(content)
		if !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "```") && !strings.HasPrefix(trimmed, "<think>") {
			parts := strings.Split(trimmed, "\n")
			if len(parts) == len(lines) {
				out := make([]Translation, len(lines))
				for i, text := range parts {
					if strings.TrimSpace(text) == "" {
						return nil, errors.New("自定义接口返回了空译文")
					}
					out[i] = Translation{ID: lines[i].ID, Text: text}
				}
				return out, nil
			}
		}
	}
	return DecodeTranslation(content, lines)
}
