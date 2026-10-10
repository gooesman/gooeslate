package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"
)

// FetchSpeech returns audio for the desktop player. System speech is handled by
// SAPI and never makes an HTTP request.
func FetchSpeech(ctx context.Context, client *http.Client, p TTSService, text string) (audio []byte, format string, err error) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 50000 {
		return nil, "", errors.New("请选择不超过 50000 个字符的朗读文本")
	}
	if p.Provider == "system" {
		return nil, "", errors.New("系统朗读由本机语音服务处理")
	}
	if p.Provider != "openai" && p.Provider != "http" {
		return nil, "", errors.New("朗读服务类型无效")
	}
	if p.Speed == 0 {
		p.Speed = 1
	}
	if math.IsNaN(p.Speed) || math.IsInf(p.Speed, 0) || p.Speed < .25 || p.Speed > 4 {
		return nil, "", errors.New("音频生成速度范围为 0.25 到 4")
	}
	if p.Model == "" && p.Provider == "openai" {
		p.Model = "tts-1"
	}
	if p.Voice == "" && p.Provider == "openai" {
		p.Voice = "alloy"
	}
	vars := emptyTemplateVariables()
	vars["text"] = text
	vars["voice"] = p.Voice
	vars["model"] = p.Model
	vars["api_key"] = p.APIKey
	vars["speed"] = p.Speed
	vars["source_language"] = DetectSourceLanguage(text)
	payload := map[string]any{"text": text, "voice": p.Voice, "speed": p.Speed}
	endpoint := strings.TrimSpace(p.URL)
	if p.Provider == "openai" {
		endpoint = openAIEndpoint(endpoint, "/audio/speech")
		payload = map[string]any{"model": p.Model, "input": text, "voice": p.Voice, "response_format": "wav", "speed": p.Speed}
	}
	if p.BodyTemplate != "" {
		payload, err = templateObject(p.BodyTemplate, vars)
		if err != nil {
			return nil, "", err
		}
	}
	if err = rejectStream(payload); err != nil {
		return nil, "", err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", errors.New("无法构造朗读请求")
	}
	ctx, cancel := serviceContext(ctx, p.TimeoutMS)
	defer cancel()
	req, err := makeServiceRequest(ctx, endpoint, "application/json", p.APIKey, p.Headers, vars, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	if client == nil {
		client = HTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", errors.New("无法连接朗读服务")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("朗读服务返回 HTTP %d，请检查接口、密钥和模型", resp.StatusCode)
	}
	audio, err = io.ReadAll(io.LimitReader(resp.Body, (64<<20)+1))
	if err != nil {
		return nil, "", errors.New("读取朗读音频失败")
	}
	if len(audio) > 64<<20 {
		return nil, "", errors.New("朗读音频超过 64 MB")
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		var result map[string]json.RawMessage
		if json.Unmarshal(audio, &result) != nil {
			return nil, "", errors.New("朗读接口未返回有效音频")
		}
		var encoded string
		for _, key := range []string{"base64", "audio", "data"} {
			if json.Unmarshal(result[key], &encoded) == nil && encoded != "" {
				break
			}
		}
		if strings.HasPrefix(encoded, "data:") {
			if i := strings.IndexByte(encoded, ','); i >= 0 {
				encoded = encoded[i+1:]
			}
		}
		audio, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, "", errors.New("朗读接口的 base64 音频无效")
		}
	}
	format = audioFormat(audio)
	if format != "wav" && format != "mp3" {
		return nil, "", errors.New("当前播放器只支持 WAV 或 MP3，请修改朗读接口的输出格式")
	}
	return audio, format, nil
}
func audioFormat(audio []byte) string {
	if len(audio) >= 12 && string(audio[:4]) == "RIFF" && string(audio[8:12]) == "WAVE" {
		return "wav"
	}
	if len(audio) >= 3 && string(audio[:3]) == "ID3" {
		return "mp3"
	}
	if len(audio) >= 2 && audio[0] == 255 && audio[1]&224 == 224 {
		return "mp3"
	}
	if len(audio) >= 4 && string(audio[:4]) == "OggS" {
		return "ogg"
	}
	if len(audio) >= 4 && string(audio[:4]) == "fLaC" {
		return "flac"
	}
	return ""
}
