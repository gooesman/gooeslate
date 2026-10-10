package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

// ListModels queries the selected service's OpenAI-compatible model catalog.
// A model is deliberately not required: this is how an unconfigured service
// obtains the names that can be selected in its editor.
func ListModels(ctx context.Context, client *http.Client, cfg Config, kind string) ([]string, error) {
	var provider, endpoint, key, headers string
	var timeout int
	vars := emptyTemplateVariables()
	switch kind {
	case "translation":
		p := ActiveTranslation(cfg)
		provider, endpoint, key, headers, timeout = p.Provider, p.BaseURL, p.APIKey, p.Headers, p.TimeoutMS
		vars["model"] = p.Model
	case "ocr":
		p := ActiveOCR(cfg)
		provider, endpoint, key, headers, timeout = p.Provider, p.URL, p.APIKey, p.Headers, p.TimeoutMS
		vars["model"] = p.Model
	case "tts":
		p := ActiveTTS(cfg)
		provider, endpoint, key, headers, timeout = p.Provider, p.URL, p.APIKey, p.Headers, p.TimeoutMS
		vars["model"], vars["voice"], vars["speed"] = p.Model, p.Voice, p.Speed
	default:
		return nil, errors.New("此类服务不支持获取模型列表")
	}
	if provider != "openai" {
		return nil, errors.New("获取模型列表需要选择 OpenAI 兼容服务，其他服务请手动填写模型")
	}
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("请先填写服务地址，再获取模型列表")
	}
	if err := ValidateURL(endpoint); err != nil {
		return nil, err
	}
	if timeout < 1000 || timeout > 600000 {
		return nil, errors.New("请求超时范围为 1000 到 600000 毫秒")
	}
	vars["api_key"] = key
	ctx, cancel := serviceContext(ctx, timeout)
	defer cancel()
	req, err := makeServiceRequest(ctx, modelsEndpoint(endpoint), "application/json", key, headers, vars, nil)
	if err != nil {
		return nil, err
	}
	req.Method = http.MethodGet
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	// Copy the client so a caller-supplied transport or timeout is retained,
	// while catalog requests never forward credentials through redirects.
	if client == nil {
		client = HTTPClient()
	}
	queryClient := *client
	queryClient.CheckRedirect = HTTPClient().CheckRedirect
	raw, err := response(&queryClient, req)
	if err != nil {
		var status *httpStatusError
		if errors.As(err, &status) {
			switch status.StatusCode {
			case http.StatusNotFound:
				return nil, errors.New("模型列表接口返回 HTTP 404，请检查 API 地址是否正确；如果服务不支持 /models，可手动填写模型名称")
			case http.StatusUnauthorized, http.StatusForbidden:
				return nil, fmt.Errorf("模型列表接口返回 HTTP %d，请检查 API 密钥及模型列表访问权限", status.StatusCode)
			}
		}
		return nil, err
	}
	value, err := responseValue(raw, "")
	if err != nil {
		return nil, err
	}
	return modelIDs(value)
}

func modelsEndpoint(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return base
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(u.Path, "/models") {
		u.RawPath = ""
		return u.String()
	}
	for _, suffix := range []string{"/chat/completions", "/audio/speech"} {
		if strings.HasSuffix(u.Path, suffix) {
			u.Path = strings.TrimSuffix(u.Path, suffix)
			if u.Path == "" {
				// A complete endpoint with no /v1 was explicitly supplied. Its
				// sibling catalog is /models rather than an inferred API prefix.
				u.Path = "/models"
				u.RawPath = ""
				return u.String()
			}
			break
		}
	}
	u.RawPath = ""
	return openAIEndpoint(u.String(), "/models")
}

func modelIDs(value any) ([]string, error) {
	invalid := errors.New("模型列表格式不匹配：需要包含 data 数组及每项的 id，请确认服务支持 OpenAI /models 接口")
	object, ok := value.(map[string]any)
	if !ok {
		return nil, invalid
	}
	items, ok := object["data"].([]any)
	if !ok {
		return nil, invalid
	}
	if len(items) > 10000 {
		return nil, errors.New("服务返回的模型数量过多")
	}
	seen := make(map[string]bool, len(items))
	models := make([]string, 0, len(items))
	for _, item := range items {
		model, ok := item.(map[string]any)
		if !ok {
			return nil, invalid
		}
		id, ok := model["id"].(string)
		if !ok {
			return nil, invalid
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if len(id) > 4096 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
			return nil, errors.New("服务返回了无效的模型名称，请检查 /models 接口")
		}
		if !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	if len(models) == 0 {
		return nil, errors.New("服务未返回可选模型，请检查密钥的模型权限，或手动填写模型名称")
	}
	sort.Strings(models)
	return models, nil
}
