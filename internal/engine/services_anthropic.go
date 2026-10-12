package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const defaultAnthropicMaxTokens = 4096
const maxAnthropicMaxTokens = 1000000
const anthropicVersion = "2023-06-01"

// anthropicEndpoint accepts an origin, an explicit API prefix, or a complete
// Messages/Models endpoint. Preserve query parameters used by compatible gateways.
func anthropicEndpoint(base, suffix string) string {
	base = strings.TrimSpace(base)
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	path := strings.TrimRight(u.Path, "/")
	completeEndpoint := false
	for _, endpoint := range []string{"/messages", "/models"} {
		if strings.HasSuffix(path, endpoint) {
			path = strings.TrimSuffix(path, endpoint)
			completeEndpoint = true
			break
		}
	}
	if path == "" && !completeEndpoint {
		path = "/v1"
	}
	u.Path = path + "/" + strings.Trim(suffix, "/")
	u.RawPath = ""
	return u.String()
}

// Callers may change Method to GET for the Models API. Custom headers are applied
// after defaults so a compatible gateway can explicitly choose its authentication.
func makeAnthropicRequest(ctx context.Context, endpoint, apiKey, headers string, vars templateVariables, body io.Reader) (*http.Request, error) {
	if err := ValidateURL(endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, errors.New("无法构造 Anthropic 服务请求")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", anthropicVersion)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	custom, err := headerValues(headers, vars)
	if err != nil {
		return nil, err
	}
	for key, values := range custom {
		req.Header[key] = values
	}
	return req, nil
}

func postAnthropicJSON(ctx context.Context, client *http.Client, endpoint, key, headers string, vars templateVariables, payload map[string]any) ([]byte, error) {
	if err := rejectStream(payload); err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("无法编码 Anthropic 服务请求")
	}
	req, err := makeAnthropicRequest(ctx, endpoint, key, headers, vars, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	return response(client, req)
}

func anthropicResponseValue(raw []byte, path string) (any, error) {
	value, err := responseValue(raw, "")
	if err != nil {
		return nil, err
	}
	if message, ok := value.(map[string]any); ok {
		if message["type"] == "error" {
			return nil, errors.New("Anthropic 服务返回错误，请检查接口、密钥和模型")
		}
		if message["stop_reason"] == "max_tokens" {
			return nil, errors.New("Anthropic 输出达到最大长度，译文可能不完整；请增大最大输出长度或减少文字")
		}
	}
	// Both request modes retain their configured response path. The default
	// content path still joins all text blocks instead of taking only the first one.
	if path != "" && path != "content" {
		return valueAtPath(value, path)
	}
	content, err := valueAtPath(value, "content")
	if err != nil {
		return nil, errors.New("Anthropic 响应缺少文字内容，请检查服务协议或响应路径")
	}
	blocks, ok := content.([]any)
	if !ok {
		return nil, errors.New("Anthropic 响应的 content 需要是内容块数组")
	}
	var text strings.Builder
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok || block["type"] != "text" {
			continue
		}
		part, ok := block["text"].(string)
		if !ok {
			return nil, errors.New("Anthropic 响应的文字内容格式无效")
		}
		text.WriteString(part)
	}
	if strings.TrimSpace(text.String()) == "" {
		return nil, errors.New("Anthropic 没有返回可用文字，请检查模型或请求配置")
	}
	return text.String(), nil
}
