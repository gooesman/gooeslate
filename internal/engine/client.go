package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
)

const maxResponse = 16 << 20

func HTTPClient() *http.Client {
	// Request contexts enforce the configured deadline. A global client timeout
	// would silently cap longer service settings at the former 90-second limit.
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("服务返回了重定向，请填写最终接口地址")
	}}
}
func ValidateURL(raw string) error {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u == nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return errors.New("请输入有效的 http:// 或 https:// 接口地址")
	}
	return nil
}
func response(client *http.Client, req *http.Request) ([]byte, error) {
	if client == nil {
		client = HTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, errors.New("无法连接服务，请检查地址、网络和超时设置")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("服务返回 HTTP %d，请检查接口、密钥和模型", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, errors.New("读取服务响应失败")
	}
	if len(b) > maxResponse {
		return nil, errors.New("服务响应过大")
	}
	return b, nil
}
func Recognize(ctx context.Context, client *http.Client, endpoint string, img image.Image) ([]Block, error) {
	ctx, cancel := serviceContext(ctx, 90000)
	defer cancel()
	if err := ValidateURL(endpoint); err != nil {
		return nil, fmt.Errorf("OCR：%w", err)
	}
	if img == nil || img.Bounds().Empty() {
		return nil, errors.New("请先截图或打开图片")
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="image"; filename="uploaded_image.jpg"`)
	header.Set("Content-Type", "image/jpeg")
	part, err := w.CreatePart(header)
	if err != nil {
		return nil, err
	}
	if err = jpeg.Encode(part, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	if err = w.WriteField("return_word_box", "True"); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	raw, err := response(client, req)
	if err != nil {
		return nil, err
	}
	return DecodeOCR(raw)
}
func DecodeOCR(raw []byte) ([]Block, error) {
	var wire []struct {
		Text string      `json:"text"`
		Box  [][]float64 `json:"box"`
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' || json.Unmarshal(raw, &wire) != nil {
		return nil, errors.New("OCR 格式不匹配：需要包含 text 和四角 box 的数组")
	}
	if len(wire) > 10000 {
		return nil, errors.New("识别文字块过多")
	}
	var out []Block
	for _, v := range wire {
		if strings.TrimSpace(v.Text) == "" {
			continue
		}
		if len(v.Box) != 4 {
			return nil, errors.New("OCR 缺少四角坐标")
		}
		b := Block{Text: v.Text}
		for i, p := range v.Box {
			if len(p) != 2 || math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsInf(p[0], 0) || math.IsInf(p[1], 0) {
				return nil, errors.New("OCR 坐标无效")
			}
			b.Box[i] = [2]float64{p[0], p[1]}
		}
		r := b.Bounds()
		if r.W > 0 && r.H > 0 {
			out = append(out, b)
		}
	}
	return out, nil
}
func Translate(ctx context.Context, client *http.Client, cfg Config, lines []Line) ([]Translation, error) {
	return translateConfigured(ctx, client, cfg, lines)
}
func DecodeTranslation(content string, lines []Line) ([]Translation, error) {
	s := strings.TrimSpace(content)
	if strings.HasPrefix(s, "<think>") {
		if i := strings.Index(s, "</think>"); i >= 0 {
			s = strings.TrimSpace(s[i+8:])
		}
	}
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = strings.TrimSpace(s[i+1:])
			s = strings.TrimSpace(strings.TrimSuffix(s, "```"))
		}
	}
	var values []Translation
	if json.Unmarshal([]byte(s), &values) != nil || len(values) != len(lines) {
		return nil, errors.New("模型没有返回完整的逐行 JSON 译文，请重试或更换模型")
	}
	expected := map[int]bool{}
	for _, l := range lines {
		expected[l.ID] = true
	}
	seen := map[int]string{}
	for _, v := range values {
		if !expected[v.ID] || strings.TrimSpace(v.Text) == "" {
			return nil, errors.New("译文行号或内容无效")
		}
		if _, exists := seen[v.ID]; exists {
			return nil, errors.New("译文包含重复行号")
		}
		seen[v.ID] = v.Text
	}
	out := make([]Translation, len(lines))
	for i, l := range lines {
		out[i] = Translation{l.ID, seen[l.ID]}
	}
	return out, nil
}
