package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type templateVariables map[string]any

func validationVariables() templateVariables {
	return templateVariables{"lines": []Line{{ID: 0, Text: "text"}}, "messages": []map[string]any{{"role": "user", "content": "text"}}, "text": "text", "image": "image", "image_base64": "image", "image_data_url": "data:image/jpeg;base64,image", "target_language": "简体中文", "source_language": "英语", "model": "model", "api_key": "key", "voice": "voice", "speed": 1.0}
}

func emptyTemplateVariables() templateVariables {
	return templateVariables{"lines": []Line{}, "messages": []map[string]any{}, "text": "", "image": "", "image_base64": "", "image_data_url": "", "target_language": "", "source_language": "", "model": "", "api_key": "", "voice": "", "speed": 1.0}
}

func renderTemplateText(text string, vars templateVariables) (string, error) {
	var b strings.Builder
	for {
		i := strings.Index(text, "{{")
		if i < 0 {
			b.WriteString(text)
			return b.String(), nil
		}
		b.WriteString(text[:i])
		text = text[i+2:]
		j := strings.Index(text, "}}")
		if j < 0 {
			return "", errors.New("模板变量未闭合")
		}
		key := strings.TrimSpace(text[:j])
		value, ok := vars[key]
		if !ok {
			return "", errors.New("请求模板使用了未知变量")
		}
		if s, ok := value.(string); ok {
			b.WriteString(s)
		} else {
			raw, err := json.Marshal(value)
			if err != nil {
				return "", errors.New("模板变量无法编码")
			}
			b.Write(raw)
		}
		text = text[j+2:]
	}
}
func renderTemplateValue(value any, vars templateVariables, depth int) (any, error) {
	if depth > 32 {
		return nil, errors.New("JSON 模板嵌套过深")
	}
	switch v := value.(type) {
	case string:
		if strings.HasPrefix(v, "{{") && strings.HasSuffix(v, "}}") && strings.Count(v, "{{") == 1 {
			key := strings.TrimSpace(v[2 : len(v)-2])
			if result, ok := vars[key]; ok {
				return result, nil
			}
			return nil, errors.New("请求模板使用了未知变量")
		}
		return renderTemplateText(v, vars)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if strings.Contains(key, "{{") {
				return nil, errors.New("模板变量只能用于 JSON 值，不能用于字段名称")
			}
			rendered, err := renderTemplateValue(item, vars, depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			rendered, err := renderTemplateValue(item, vars, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	default:
		return value, nil
	}
}
func templateObject(raw string, vars templateVariables) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	if len(raw) > 512<<10 {
		return nil, errors.New("JSON 模板过大")
	}
	var value map[string]any
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil || value == nil {
		return nil, errors.New("请求模板需要有效的 JSON 对象")
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return nil, errors.New("JSON 模板包含多余内容")
	}
	out, err := renderTemplateValue(value, vars, 0)
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}
func rejectStream(payload map[string]any) error {
	if v, ok := payload["stream"]; ok {
		if v != false {
			return errors.New("当前客户端不支持流式请求，请将 stream 设为 false")
		}
	}
	return nil
}
func disableThinking(payload map[string]any, enabled bool) error {
	if !enabled {
		return nil
	}
	kwargs := map[string]any{}
	if v, ok := payload["chat_template_kwargs"]; ok {
		var valid bool
		kwargs, valid = v.(map[string]any)
		if !valid {
			return errors.New("chat_template_kwargs 必须是 JSON 对象")
		}
	}
	kwargs["enable_thinking"] = false
	payload["chat_template_kwargs"] = kwargs
	return nil
}
func headerValues(raw string, vars templateVariables) (http.Header, error) {
	object, err := templateObject(raw, vars)
	if err != nil {
		return nil, errors.New("请求头需要有效的 JSON 对象")
	}
	if len(object) > 100 {
		return nil, errors.New("请求头过多")
	}
	result := http.Header{}
	for key, v := range object {
		if !validHeaderName(key) {
			return nil, errors.New("请求头名称无效")
		}
		if strings.EqualFold(key, "Host") || strings.EqualFold(key, "Content-Length") {
			return nil, errors.New("Host 和 Content-Length 由客户端自动设置")
		}
		value, ok := v.(string)
		if !ok {
			return nil, errors.New("请求头的值必须是字符串")
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, errors.New("请求头不能包含换行")
		}
		result.Set(key, value)
	}
	return result, nil
}
func validHeaderName(key string) bool {
	if key == "" {
		return false
	}
	for _, c := range key {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}
func validateCommon(headers, body string, timeout int) error {
	if timeout < 1000 || timeout > 600000 {
		return errors.New("请求超时范围为 1000 到 600000 毫秒")
	}
	if _, err := headerValues(headers, validationVariables()); err != nil {
		return err
	}
	if body != "" {
		object, err := templateObject(body, validationVariables())
		if err != nil {
			return err
		}
		if err = rejectStream(object); err != nil {
			return err
		}
	}
	return nil
}
func pathParts(path string) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "$" {
		return nil, nil
	}
	invalid := errors.New("响应路径请使用 data.items 或 choices.0.message.content 形式")
	if len(path) > 2048 {
		return nil, invalid
	}
	if path[0] == '$' {
		path = path[1:]
		if path[0] == '.' {
			path = path[1:]
		} else if path[0] != '[' {
			return nil, invalid
		}
	}
	var parts []string
	for len(path) > 0 {
		if path[0] == '[' {
			end := strings.IndexByte(path, ']')
			if end < 2 {
				return nil, invalid
			}
			index := path[1:end]
			for _, c := range index {
				if c < '0' || c > '9' {
					return nil, invalid
				}
			}
			if _, err := strconv.ParseUint(index, 10, 63); err != nil {
				return nil, invalid
			}
			parts = append(parts, index)
			path = path[end+1:]
		} else {
			end := strings.IndexAny(path, ".[]")
			if end < 0 {
				end = len(path)
			}
			key := path[:end]
			if key == "" || strings.ContainsAny(key, "$* \t\r\n\"'") {
				return nil, invalid
			}
			parts = append(parts, key)
			path = path[end:]
		}
		if len(parts) > 64 {
			return nil, invalid
		}
		if len(path) == 0 {
			break
		}
		if path[0] == '.' {
			path = path[1:]
			if path == "" || strings.ContainsRune(".[]", rune(path[0])) {
				return nil, invalid
			}
		} else if path[0] != '[' {
			return nil, invalid
		}
	}
	if len(parts) == 0 {
		return nil, invalid
	}
	return parts, nil
}
func validatePath(path string) error { _, err := pathParts(path); return err }
func responseValue(raw []byte, path string) (any, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return nil, errors.New("服务响应不是有效 JSON")
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return nil, errors.New("服务响应包含多余内容")
	}
	return valueAtPath(value, path)
}
func valueAtPath(value any, path string) (any, error) {
	parts, err := pathParts(path)
	if err != nil {
		return nil, err
	}
	for _, p := range parts {
		switch v := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = v[p]
			if !ok {
				return nil, errors.New("服务响应中未找到配置的字段")
			}
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(v) {
				return nil, errors.New("服务响应数组索引无效")
			}
			value = v[i]
		default:
			return nil, errors.New("服务响应字段结构不匹配")
		}
	}
	return value, nil
}
func chatEndpoint(base string) string {
	return appendServicePath(base, "/chat/completions")
}
func appendServicePath(base, path string) string {
	base = strings.TrimSpace(base)
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, path) {
		u.Path += path
	}
	u.RawPath = ""
	return u.String()
}
func makeServiceRequest(ctx context.Context, endpoint, contentType, key, headers string, vars templateVariables, body io.Reader) (*http.Request, error) {
	if err := ValidateURL(endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, errors.New("无法构造服务请求")
	}
	req.Header.Set("Content-Type", contentType)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	custom, err := headerValues(headers, vars)
	if err != nil {
		return nil, err
	}
	for k, values := range custom {
		req.Header[k] = values
	}
	return req, nil
}
func postServiceJSON(ctx context.Context, client *http.Client, endpoint, key, headers string, vars templateVariables, payload map[string]any) ([]byte, error) {
	if err := rejectStream(payload); err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("无法编码服务请求")
	}
	req, err := makeServiceRequest(ctx, endpoint, "application/json", key, headers, vars, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = HTTPClient()
	}
	return response(client, req)
}
func contentJSON(value any) (string, error) {
	if s, ok := value.(string); ok {
		return s, nil
	}
	if value == nil {
		return "", errors.New("服务响应内容为空")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("服务响应无法解析")
	}
	return string(raw), nil
}
