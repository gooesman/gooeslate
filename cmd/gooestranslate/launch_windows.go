package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/gooesman/gooeslate/internal/engine"
)

var instanceActivationMessage = uint32(call(user, "RegisterWindowMessageW", uintptr(unsafe.Pointer(str("gooestranslate.ActivateSettings.v1")))))

const instanceCopyDataMessage = 0x4a // WM_COPYDATA
const instanceOpenImageID uintptr = 0x47545401
const instancePayloadLimit = 64 * 1024

type instanceCopyData struct {
	ID   uintptr
	Size uint32
	Data uintptr
}

// Resolve relative CLI paths in the sending process, whose working directory
// can differ from that of the already running application.
func encodeInstanceOpenImage(path string) ([]byte, error) {
	if path == "" || strings.ContainsRune(path, '\x00') {
		return nil, fmt.Errorf("图片路径无效")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("图片路径无效：%w", err)
	}
	payload, err := json.Marshal(absolute)
	if err != nil || len(payload) > instancePayloadLimit {
		return nil, fmt.Errorf("图片路径过长")
	}
	return payload, nil
}

func decodeInstanceOpenImage(id uintptr, payload []byte) (string, error) {
	if id != instanceOpenImageID || len(payload) == 0 || len(payload) > instancePayloadLimit {
		return "", fmt.Errorf("图片启动消息无效")
	}
	var path string
	if err := json.Unmarshal(payload, &path); err != nil || !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return "", fmt.Errorf("图片启动消息无效")
	}
	return filepath.Clean(path), nil
}

func autostartLaunch(args []string) bool {
	return len(args) == 2 && args[1] == "--autostart"
}

func showSettingsAtLaunch(args []string, trayAvailable bool) bool {
	return !autostartLaunch(args) || !trayAvailable
}

func instanceMutexName(configPath string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(configPath)))
	return "Local\\gooestranslate-" + hex.EncodeToString(digest[:16])
}

// Keep the handle alive for the lifetime of the primary instance. No mutex
// ownership is requested, so closing the handle is sufficient on exit.
func acquireApplicationInstance(name string) (uintptr, bool, error) {
	handle, _, err := kernel.NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(str(name))))
	if handle == 0 {
		return 0, false, fmt.Errorf("无法检查程序运行状态：%w", err)
	}
	return handle, err != syscall.ERROR_ALREADY_EXISTS, nil
}

func activateApplicationInstance(args []string) error {
	if autostartLaunch(args) {
		return nil
	}
	var payload []byte
	if len(args) > 2 && args[1] == "--open" {
		var err error
		payload, err = encodeInstanceOpenImage(args[2])
		if err != nil {
			return err
		}
	}
	// A first launch may still be creating its WebView controllers. Wait for
	// acknowledgement so a click during login startup is not lost.
	deadline := time.Now().Add(10 * time.Second)
	for {
		hwnd := call(user, "FindWindowW", uintptr(unsafe.Pointer(str("GooesTranslateWebV2"))), uintptr(unsafe.Pointer(str(settingsWindowTitle))))
		if hwnd == 0 {
			// A new launch can activate an earlier version that is still running.
			hwnd = call(user, "FindWindowW", uintptr(unsafe.Pointer(str("GooesTranslateWebV2"))), uintptr(unsafe.Pointer(str("gooestranslate · 设置"))))
		}
		if hwnd != 0 {
			var pid uint32
			call(user, "GetWindowThreadProcessId", hwnd, uintptr(unsafe.Pointer(&pid)))
			if pid != 0 {
				call(user, "AllowSetForegroundWindow", uintptr(pid))
			}
			if payload == nil {
				var accepted uintptr
				if instanceActivationMessage == 0 || call(user, "SendMessageTimeoutW", hwnd, uintptr(instanceActivationMessage), 0, 0, 3, 2000, uintptr(unsafe.Pointer(&accepted))) == 0 {
					return fmt.Errorf("无法打开已运行的程序，请稍后重试")
				}
				if accepted == 1 {
					return nil
				}
			} else {
				data := instanceCopyData{instanceOpenImageID, uint32(len(payload)), uintptr(unsafe.Pointer(&payload[0]))}
				var accepted uintptr
				// WM_COPYDATA transfers a bounded payload synchronously. The receiver
				// copies the path and queues image work before acknowledging it.
				sent := call(user, "SendMessageTimeoutW", hwnd, instanceCopyDataMessage, 0, uintptr(unsafe.Pointer(&data)), 3, 2000, uintptr(unsafe.Pointer(&accepted)))
				runtime.KeepAlive(payload)
				if sent == 0 {
					return fmt.Errorf("已运行的程序暂时无法接收图片，请稍后重试")
				}
				if accepted == 1 {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("已运行的程序尚未准备好，请稍后重试")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type startupTransaction interface {
	Apply(bool) error
	Rollback() error
}

func prepareConfigurationStartup(before, enabled bool, prepare func() (*scheduledStartupChange, error)) (startupTransaction, error) {
	if !before && !enabled {
		return nil, nil
	}
	change, err := prepare()
	if err != nil {
		// Preserve unchanged startup when Windows cannot read it; other settings can still save.
		if before == enabled {
			return nil, nil
		}
		return nil, err
	}
	// An enabled save also refreshes a moved executable or migrates a legacy Run entry.
	if change.NeedsApply(enabled) {
		return change, nil
	}
	return nil, nil
}

func persistConfigurationWithStartup(old, next engine.Config, startup startupTransaction, replace func(engine.Hotkeys) []string, save func(engine.Config) error) error {
	if startup != nil {
		if err := startup.Apply(next.StartAtLogin); err != nil {
			return fmt.Errorf("开机自启设置失败：%w", err)
		}
	}
	if err := persistConfigurationChange(old, next, replace, save); err != nil {
		if startup != nil {
			if restoreErr := startup.Rollback(); restoreErr != nil {
				return fmt.Errorf("%w；开机自启恢复失败：%v", err, restoreErr)
			}
		}
		return err
	}
	return nil
}
