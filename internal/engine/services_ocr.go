package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
)

func RecognizeConfigured(ctx context.Context, client *http.Client, cfg Config, img image.Image) ([]Block, error) {
	if img == nil || img.Bounds().Empty() {
		return nil, errors.New("请先截图或打开图片")
	}
	p := ActiveOCR(cfg)
	if err := ValidateURL(p.URL); err != nil {
		return nil, err
	}
	var jpegBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, errors.New("图片编码失败")
	}
	encoded := base64.StdEncoding.EncodeToString(jpegBytes.Bytes())
	dataURL := "data:image/jpeg;base64," + encoded
	imageValue := encoded
	if p.ImageEncoding == "data_url" {
		imageValue = dataURL
	}
	vars := emptyTemplateVariables()
	vars["image"] = imageValue
	vars["image_base64"] = encoded
	vars["image_data_url"] = dataURL
	vars["model"] = p.Model
	vars["api_key"] = p.APIKey
	vars["target_language"] = ResolveTargetLanguage(cfg.Language, "")
	ctx, cancel := serviceContext(ctx, p.TimeoutMS)
	defer cancel()
	var raw []byte
	var err error
	switch p.Provider {
	case "multipart":
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": p.ImageField, "filename": "uploaded_image.jpg"}))
		header.Set("Content-Type", "image/jpeg")
		part, e := writer.CreatePart(header)
		if e != nil {
			return nil, e
		}
		if _, e = part.Write(jpegBytes.Bytes()); e != nil {
			return nil, e
		}
		fields, e := templateObject(p.FormFields, vars)
		if e != nil {
			return nil, e
		}
		for key, value := range fields {
			if key == p.ImageField || strings.ContainsAny(key, "\r\n") {
				return nil, errors.New("OCR 表单字段名称重复或无效")
			}
			var text string
			if s, ok := value.(string); ok {
				text = s
			} else {
				v, e := json.Marshal(value)
				if e != nil {
					return nil, e
				}
				text = string(v)
			}
			if e = writer.WriteField(key, text); e != nil {
				return nil, e
			}
		}
		if e = writer.Close(); e != nil {
			return nil, e
		}
		req, e := makeServiceRequest(ctx, p.URL, writer.FormDataContentType(), p.APIKey, p.Headers, vars, &body)
		if e != nil {
			return nil, e
		}
		// Multipart boundaries come from the encoder, not a manually configured
		// Content-Type value that would make the form impossible to parse.
		req.Header.Set("Content-Type", writer.FormDataContentType())
		if client == nil {
			client = HTTPClient()
		}
		raw, err = response(client, req)
	case "json", "openai":
		payload := map[string]any{p.ImageField: imageValue}
		endpoint := p.URL
		if p.Provider == "openai" {
			if p.Model == "" {
				return nil, errors.New("请填写视觉 OCR 模型")
			}
			prompt := "Recognize all text in this image. Return only the recognized plain text, with original line breaks."
			if p.Coordinates {
				prompt = "Recognize all text in this image. Return only a JSON array with text and four pixel-coordinate corner points for each text block: [{\"text\":\"...\",\"box\":[[x,y],[x,y],[x,y],[x,y]]}]. Use the original image pixel coordinates, preserve rotation. No markdown."
			}
			messages := []map[string]any{{"role": "user", "content": []map[string]any{{"type": "text", "text": prompt}, {"type": "image_url", "image_url": map[string]string{"url": dataURL}}}}}
			vars["messages"] = messages
			payload = map[string]any{"model": p.Model, "temperature": 0, "stream": false, "messages": messages}
			endpoint = chatEndpoint(endpoint)
		}
		if p.BodyTemplate != "" {
			payload, err = templateObject(p.BodyTemplate, vars)
			if err != nil {
				return nil, err
			}
		}
		raw, err = postServiceJSON(ctx, client, endpoint, p.APIKey, p.Headers, vars, payload)
	default:
		return nil, errors.New("OCR 服务类型无效")
	}
	if err != nil {
		return nil, err
	}
	value, err := responseValue(raw, p.ResponsePath)
	if err != nil && p.ResponsePath == "" && !p.Coordinates {
		value = string(raw)
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return decodeConfiguredOCR(value, p, img.Bounds().Dx(), img.Bounds().Dy())
}

func cleanJSONText(text string) string {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "<think>") {
		if i := strings.Index(s, "</think>"); i >= 0 {
			s = strings.TrimSpace(s[i+8:])
		}
	}
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s[i+1:]), "```"))
		}
	}
	return s
}
func decodeConfiguredOCR(value any, p OCRService, width, height int) ([]Block, error) {
	if text, ok := value.(string); ok {
		// A text-only service can recognize source code that itself looks like
		// JSON. Only coordinate-enabled services return encoded block arrays.
		if !p.Coordinates {
			if strings.TrimSpace(text) == "" {
				return []Block{}, nil
			}
			return []Block{{Text: text}}, nil
		}
		s := cleanJSONText(text)
		if strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{") {
			parsed, err := responseValue([]byte(s), "")
			if err != nil {
				return nil, errors.New("OCR 返回的文字块 JSON 无效")
			}
			value = parsed
		} else {
			return nil, errors.New("当前 OCR 未返回文字坐标，请检查服务和响应字段设置")
		}
	}
	items, ok := value.([]any)
	if !ok {
		if object, valid := value.(map[string]any); valid {
			items = []any{object}
		} else {
			return nil, errors.New("OCR 响应需要文字块数组或文字")
		}
	}
	if len(items) > 10000 {
		return nil, errors.New("识别文字块过多")
	}
	out := make([]Block, 0, len(items))
	for _, item := range items {
		var text string
		if direct, ok := item.(string); ok && !p.Coordinates {
			text = direct
		} else {
			v, err := valueAtPath(item, p.TextField)
			if err != nil {
				return nil, errors.New("OCR 文字字段未找到，请检查 text_field")
			}
			var ok bool
			text, ok = v.(string)
			if !ok {
				return nil, errors.New("OCR 文字字段必须是字符串")
			}
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		b := Block{Text: text}
		if p.Coordinates {
			box, err := valueAtPath(item, p.BoxField)
			if err != nil {
				return nil, errors.New("OCR 坐标字段未找到，请检查 box_field")
			}
			b.Box, err = decodeServiceBox(box, p.BoxFormat)
			if err != nil {
				return nil, err
			}
			for i, point := range b.Box {
				b.Box[i] = [2]float64{math.Max(0, math.Min(float64(width), point[0])), math.Max(0, math.Min(float64(height), point[1]))}
			}
			if b.Bounds().W <= 0 || b.Bounds().H <= 0 {
				continue
			}
		}
		out = append(out, b)
	}
	return out, nil
}
func coordinateNumber(value any) (float64, error) {
	var n float64
	var err error
	switch v := value.(type) {
	case json.Number:
		n, err = v.Float64()
	case float64:
		n = v
	case int:
		n = float64(v)
	case string:
		n, err = strconv.ParseFloat(v, 64)
	default:
		err = errors.New("invalid")
	}
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 100000000 {
		return 0, errors.New("OCR 坐标无效")
	}
	return n, nil
}
func decodeServiceBox(value any, format string) ([4][2]float64, error) {
	var box [4][2]float64
	if format == "xywh" {
		var values []any
		switch v := value.(type) {
		case []any:
			values = v
		case map[string]any:
			w, h := v["w"], v["h"]
			if w == nil {
				w = v["width"]
			}
			if h == nil {
				h = v["height"]
			}
			values = []any{v["x"], v["y"], w, h}
		}
		if len(values) != 4 {
			return box, errors.New("xywh 坐标需要 x、y、width、height")
		}
		var numbers [4]float64
		for i, v := range values {
			n, err := coordinateNumber(v)
			if err != nil {
				return box, err
			}
			numbers[i] = n
		}
		x, y, w, h := numbers[0], numbers[1], numbers[2], numbers[3]
		if w <= 0 || h <= 0 {
			return box, errors.New("OCR 坐标尺寸无效")
		}
		return [4][2]float64{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}, nil
	}
	points, ok := value.([]any)
	if !ok || len(points) != 4 {
		return box, errors.New("quad 坐标需要四个角点")
	}
	for i, point := range points {
		var x, y any
		switch v := point.(type) {
		case []any:
			if len(v) == 2 {
				x, y = v[0], v[1]
			}
		case map[string]any:
			x, y = v["x"], v["y"]
		}
		nx, err := coordinateNumber(x)
		if err != nil {
			return box, err
		}
		ny, err := coordinateNumber(y)
		if err != nil {
			return box, err
		}
		box[i] = [2]float64{nx, ny}
	}
	return box, nil
}
