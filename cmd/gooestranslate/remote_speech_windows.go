package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var speechWinMM = syscall.NewLazyDLL("winmm.dll")
var remoteSpeechSequence atomic.Uint64

type remoteSpeechRequest struct {
	ctx    context.Context
	audio  []byte
	format string
	stop   bool
	reply  chan error
}

// RemoteSpeechService owns its MCI device on one worker thread. Play waits only
// for file/device setup and starts playback without "wait". Call Play from the
// HTTP speech worker, not the UI thread. Stop and Close release the device and
// temporary audio; completed playback is also cleaned up automatically.
type RemoteSpeechService struct {
	requests chan remoteSpeechRequest
	closing  chan struct{}
	done     chan struct{}
	once     sync.Once
	send     func(string) (string, error)
	pump     func()
	baseDir  string
	poll     time.Duration
}

func NewRemoteSpeechService() *RemoteSpeechService {
	return newRemoteSpeechService(nativeSpeechMCI, pumpSpeechMessages, "", 100*time.Millisecond)
}

func newRemoteSpeechService(send func(string) (string, error), pump func(), baseDir string, poll time.Duration) *RemoteSpeechService {
	s := &RemoteSpeechService{
		requests: make(chan remoteSpeechRequest, 8), closing: make(chan struct{}), done: make(chan struct{}),
		send: send, pump: pump, baseDir: baseDir, poll: poll,
	}
	go s.run()
	return s
}

func remoteSpeechFormat(audio []byte, format string) (string, error) {
	if len(audio) == 0 || len(audio) > 64*1024*1024 {
		return "", errors.New("朗读音频为空或超过 64 MB")
	}
	format = strings.ToLower(strings.TrimSpace(strings.SplitN(format, ";", 2)[0]))
	switch format {
	case "wav", "wave", "audio/wav", "audio/wave", "audio/x-wav":
		if len(audio) < 12 || !bytes.Equal(audio[:4], []byte("RIFF")) || !bytes.Equal(audio[8:12], []byte("WAVE")) {
			return "", errors.New("朗读服务未返回有效 WAV 音频")
		}
		return "wav", nil
	case "mp3", "audio/mpeg", "audio/mp3":
		if len(audio) >= 10 && bytes.Equal(audio[:3], []byte("ID3")) {
			return "mp3", nil
		}
		for i := 0; i+3 < len(audio) && i < 4096; i++ {
			if audio[i] == 0xff && audio[i+1]&0xe0 == 0xe0 && audio[i+1]&0x18 != 0x08 && audio[i+1]&0x06 != 0 && audio[i+2]&0xf0 != 0xf0 && audio[i+2]&0x0c != 0x0c {
				return "mp3", nil
			}
		}
		return "", errors.New("朗读服务未返回有效 MP3 音频")
	default:
		return "", errors.New("本机播放支持 MP3 或 WAV 音频")
	}
}

func (s *RemoteSpeechService) Play(audio []byte, format string) error {
	return s.PlayContext(context.Background(), audio, format)
}

func (s *RemoteSpeechService) PlayContext(ctx context.Context, audio []byte, format string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	format, err := remoteSpeechFormat(audio, format)
	if err != nil {
		return err
	}
	// The caller may reuse its HTTP buffer as soon as Play returns.
	return s.submit(remoteSpeechRequest{ctx: ctx, audio: append([]byte(nil), audio...), format: format})
}

func (s *RemoteSpeechService) submit(request remoteSpeechRequest) error {
	if s == nil {
		return errors.New("远程语音播放尚未初始化")
	}
	request.reply = make(chan error, 1)
	if request.ctx == nil {
		request.ctx = context.Background()
	}
	select {
	case <-request.ctx.Done():
		return request.ctx.Err()
	case <-s.closing:
		return errors.New("远程语音播放已关闭")
	case s.requests <- request:
	}
	select {
	case <-request.ctx.Done():
		return request.ctx.Err()
	case err := <-request.reply:
		return err
	case <-s.done:
		return errors.New("远程语音播放已关闭")
	}
}

func (s *RemoteSpeechService) Stop() {
	if s != nil {
		_ = s.submit(remoteSpeechRequest{stop: true})
	}
}

func (s *RemoteSpeechService) Close() {
	if s != nil {
		s.once.Do(func() { close(s.closing) })
		<-s.done
	}
}

type remoteSpeechPlayer struct {
	send                func(string) (string, error)
	baseDir, dir, alias string
	file                string
	files               map[string]bool
}

func (p *remoteSpeechPlayer) removeFiles() {
	for file := range p.files {
		if err := os.Remove(file); err == nil || os.IsNotExist(err) {
			delete(p.files, file)
		}
	}
}

func (p *remoteSpeechPlayer) stop() error {
	var err error
	if p.alias != "" {
		_, _ = p.send("stop " + p.alias)
		_, err = p.send("close " + p.alias)
		if err == nil {
			p.alias, p.file = "", ""
		}
	}
	p.removeFiles()
	return err
}

func (p *remoteSpeechPlayer) play(ctx context.Context, audio []byte, format string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.stop(); err != nil {
		return fmt.Errorf("无法停止上一次朗读：%w", err)
	}
	if p.dir == "" {
		dir, err := os.MkdirTemp(p.baseDir, "gooestranslate-speech-")
		if err != nil {
			return errors.New("无法创建朗读音频临时目录")
		}
		p.dir = dir
	}
	f, err := os.CreateTemp(p.dir, "speech-*."+format)
	if err != nil {
		return errors.New("无法创建朗读音频临时文件")
	}
	file := f.Name()
	if p.files == nil {
		p.files = map[string]bool{}
	}
	p.files[file] = true
	_, err = f.Write(audio)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		p.removeFiles()
		return errors.New("无法保存朗读音频")
	}
	if err = ctx.Err(); err != nil {
		p.removeFiles()
		return err
	}
	p.alias = fmt.Sprintf("gooestranslate_speech_%d_%d", os.Getpid(), remoteSpeechSequence.Add(1))
	p.file = file
	typeName := "waveaudio"
	if format == "mp3" {
		typeName = "mpegvideo"
	}
	// Only internally generated local paths and aliases enter MCI commands.
	if _, err = p.send(fmt.Sprintf("open \"%s\" type %s alias %s", file, typeName, p.alias)); err != nil {
		// An unsuccessful open owns no device. Try closing a partially opened
		// driver but do not retain an invalid alias and block future playback.
		_, _ = p.send("close " + p.alias)
		p.alias, p.file = "", ""
		p.removeFiles()
		return fmt.Errorf("无法打开朗读音频：%w", err)
	}
	if err = ctx.Err(); err != nil {
		_ = p.stop()
		return err
	}
	_, err = p.send("play " + p.alias)
	if err != nil {
		_ = p.stop()
		return fmt.Errorf("无法播放朗读音频：%w", err)
	}
	return nil
}

func (p *remoteSpeechPlayer) poll() {
	if p.alias != "" {
		mode, err := p.send("status " + p.alias + " mode")
		if err != nil || strings.EqualFold(strings.TrimSpace(mode), "stopped") {
			_ = p.stop()
		}
	} else {
		p.removeFiles()
	}
}

func (p *remoteSpeechPlayer) close() {
	_ = p.stop()
	if p.dir != "" {
		// Every file in this private directory was created by this service.
		// Avoid recursive removal; only tracked audio and the empty directory.
		p.removeFiles()
		_ = os.Remove(p.dir)
	}
}

func (s *RemoteSpeechService) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(s.done)
	player := &remoteSpeechPlayer{send: s.send, baseDir: s.baseDir}
	defer player.close()
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		select {
		case <-s.closing:
			return
		case request := <-s.requests:
			select {
			case <-s.closing:
				return
			default:
			}
			var err error
			if request.stop {
				err = player.stop()
			} else {
				err = player.play(request.ctx, request.audio, request.format)
			}
			request.reply <- err
		case <-ticker.C:
			if s.pump != nil {
				s.pump()
			}
			player.poll()
		}
	}
}

func nativeSpeechMCI(command string) (string, error) {
	buffer := make([]uint16, 256)
	code := call(speechWinMM, "mciSendStringW", uintptr(unsafe.Pointer(str(command))), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
	if code != 0 {
		if call(speechWinMM, "mciGetErrorStringW", code, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer))) == 0 {
			return "", fmt.Errorf("Windows 音频错误 %d", code)
		}
		return "", fmt.Errorf("Windows 音频错误 %d：%s", code, syscall.UTF16ToString(buffer))
	}
	return syscall.UTF16ToString(buffer), nil
}

func pumpSpeechMessages() {
	for i := 0; i < 32; i++ {
		var msg message
		if call(user, "PeekMessageW", uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 1) == 0 {
			return
		}
		call(user, "TranslateMessage", uintptr(unsafe.Pointer(&msg)))
		call(user, "DispatchMessageW", uintptr(unsafe.Pointer(&msg)))
	}
}
