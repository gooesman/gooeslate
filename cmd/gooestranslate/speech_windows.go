package main

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

type SpeechVoice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Language string `json:"language"`
}

type speechReply struct {
	voices []SpeechVoice
	err    error
}
type speechRequest struct {
	action, text, voice string
	rate                int
	reply               chan speechReply
}

// SpeechService owns a single COM apartment on its worker thread. Speech starts
// asynchronously, so Stop and Close can purge the current speech immediately.
type SpeechService struct {
	requests chan speechRequest
	closing  chan struct{}
	done     chan struct{}
	once     sync.Once
}

func NewSpeechService() *SpeechService {
	s := &SpeechService{requests: make(chan speechRequest, 8), closing: make(chan struct{}), done: make(chan struct{})}
	go s.run()
	return s
}

func (s *SpeechService) submit(request speechRequest) speechReply {
	if s == nil {
		return speechReply{err: errors.New("语音服务尚未初始化")}
	}
	request.reply = make(chan speechReply, 1)
	select {
	case <-s.closing:
		return speechReply{err: errors.New("语音服务已关闭")}
	case s.requests <- request:
	}
	select {
	case result := <-request.reply:
		return result
	case <-s.done:
		return speechReply{err: errors.New("语音服务已关闭")}
	}
}

func (s *SpeechService) Voices() ([]SpeechVoice, error) {
	r := s.submit(speechRequest{action: "voices"})
	return r.voices, r.err
}

func (s *SpeechService) Speak(text, voice string, rate int) error {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 50000 {
		return errors.New("请选择不超过 50000 个字符的朗读文本")
	}
	if rate < -10 || rate > 10 {
		return errors.New("语速必须在 -10 到 10 之间")
	}
	if strings.ContainsRune(text, '\x00') {
		return errors.New("朗读文本包含无效字符")
	}
	return s.submit(speechRequest{action: "speak", text: text, voice: voice, rate: rate}).err
}

func (s *SpeechService) Stop() {
	if s != nil {
		s.submit(speechRequest{action: "stop"})
	}
}

func (s *SpeechService) Close() {
	if s != nil {
		s.once.Do(func() { close(s.closing) })
		<-s.done
	}
}

func (s *SpeechService) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(s.done)
	var speaker *ole.IDispatch
	var setupErr error
	err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	var oleErr *ole.OleError
	// S_FALSE means this apartment was already initialized on the thread; it
	// still requires a matching CoUninitialize.
	if err != nil && !(errors.As(err, &oleErr) && oleErr.Code() == 1) {
		setupErr = errors.New("无法初始化系统语音服务")
	} else {
		defer ole.CoUninitialize()
		unknown, err := oleutil.CreateObject("SAPI.SpVoice")
		if err != nil {
			setupErr = errors.New("系统未提供 SAPI 朗读服务")
		} else {
			speaker, err = unknown.QueryInterface(ole.IID_IDispatch)
			unknown.Release()
			if err != nil || speaker == nil {
				setupErr = errors.New("无法连接系统语音服务")
			} else {
				defer speaker.Release()
				defer sapiCall(speaker, "Speak", "", 3|16) // async + purge + plain text
			}
		}
	}
	for {
		select {
		case <-s.closing:
			return
		case request := <-s.requests:
			result := speechReply{err: setupErr}
			if setupErr == nil {
				switch request.action {
				case "voices":
					result.voices, result.err = sapiVoices(speaker)
				case "stop":
					result.err = sapiCall(speaker, "Speak", "", 3|16)
				case "speak":
					result.err = sapiSpeak(speaker, request)
				}
			}
			request.reply <- result
		}
	}
}

func sapiCall(d *ole.IDispatch, method string, args ...any) error {
	v, err := oleutil.CallMethod(d, method, args...)
	if v != nil {
		v.Clear()
	}
	if err != nil {
		return errors.New("系统语音调用失败")
	}
	return nil
}

func sapiVoices(speaker *ole.IDispatch) ([]SpeechVoice, error) {
	collection, err := oleutil.CallMethod(speaker, "GetVoices")
	if err != nil || collection == nil {
		return nil, errors.New("无法读取系统声音列表")
	}
	defer collection.Clear()
	tokens := collection.ToIDispatch()
	if tokens == nil {
		return nil, errors.New("系统声音列表无效")
	}
	count, err := oleutil.GetProperty(tokens, "Count")
	if err != nil || count == nil {
		return nil, errors.New("无法读取系统声音列表")
	}
	n := int(count.Val)
	count.Clear()
	if n < 0 || n > 512 {
		return nil, errors.New("系统声音列表无效")
	}
	voices := make([]SpeechVoice, 0, n)
	for i := 0; i < n; i++ {
		item, err := oleutil.CallMethod(tokens, "Item", i)
		if err != nil || item == nil {
			continue
		}
		token := item.ToIDispatch()
		if token != nil {
			voice := SpeechVoice{}
			if id, err := oleutil.GetProperty(token, "Id"); err == nil && id != nil {
				voice.ID = id.ToString()
				id.Clear()
			}
			if name, err := oleutil.CallMethod(token, "GetDescription"); err == nil && name != nil {
				voice.Name = name.ToString()
				name.Clear()
			}
			if language, err := oleutil.CallMethod(token, "GetAttribute", "Language"); err == nil && language != nil {
				voice.Language = language.ToString()
				language.Clear()
			}
			if voice.ID != "" {
				voices = append(voices, voice)
			}
		}
		item.Clear()
	}
	return voices, nil
}

func sapiSpeak(speaker *ole.IDispatch, request speechRequest) error {
	if request.voice != "" {
		collection, err := oleutil.CallMethod(speaker, "GetVoices")
		if err != nil || collection == nil {
			return errors.New("无法读取系统声音列表")
		}
		defer collection.Clear()
		tokens := collection.ToIDispatch()
		if tokens == nil {
			return errors.New("系统声音列表无效")
		}
		count, err := oleutil.GetProperty(tokens, "Count")
		if err != nil || count == nil {
			return errors.New("无法读取系统声音列表")
		}
		n := int(count.Val)
		count.Clear()
		found := false
		for i := 0; i < n && i < 512; i++ {
			item, err := oleutil.CallMethod(tokens, "Item", i)
			if err != nil || item == nil {
				continue
			}
			token := item.ToIDispatch()
			if token != nil {
				id, err := oleutil.GetProperty(token, "Id")
				if err == nil && id != nil {
					found = strings.EqualFold(id.ToString(), request.voice)
					id.Clear()
				}
				if found {
					v, err := oleutil.PutPropertyRef(speaker, "Voice", token)
					if v != nil {
						v.Clear()
					}
					item.Clear()
					if err != nil {
						return errors.New("无法选择指定的声音")
					}
					break
				}
			}
			item.Clear()
		}
		if !found {
			return errors.New("指定的系统声音未安装，请在设置中重新选择")
		}
	}
	rate, err := oleutil.PutProperty(speaker, "Rate", request.rate)
	if rate != nil {
		rate.Clear()
	}
	if err != nil {
		return errors.New("无法调整朗读语速")
	}
	// SVSFIsNotXML prevents recognized text from being interpreted as SAPI XML.
	return sapiCall(speaker, "Speak", request.text, 3|16)
}
