package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const startupRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const startupValueName = "gooestranslate"

type startupValue struct {
	exists bool
	typeID uint32
	data   []byte
}

// A store owns exactly one value. Its interface cannot modify neighboring
// applications' startup entries, and tests can use an entirely in-memory store.
type startupStore interface {
	Read() (startupValue, error)
	Write(startupValue) error
	Delete() error
}

type windowsStartupStore struct{}

func (windowsStartupStore) Read() (startupValue, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, startupRunKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return startupValue{}, nil
	}
	if err != nil {
		return startupValue{}, err
	}
	defer key.Close()
	data := make([]byte, 256)
	for attempt := 0; attempt < 4; attempt++ {
		n, typ, err := key.GetValue(startupValueName, data)
		if errors.Is(err, registry.ErrNotExist) {
			return startupValue{}, nil
		}
		if errors.Is(err, registry.ErrShortBuffer) {
			if n <= len(data) || n > 1024*1024 {
				return startupValue{}, fmt.Errorf("开机自启注册表值长度异常")
			}
			data = make([]byte, n)
			continue
		}
		if err != nil {
			return startupValue{}, err
		}
		return startupValue{exists: true, typeID: typ, data: append([]byte(nil), data[:n]...)}, nil
	}
	return startupValue{}, fmt.Errorf("开机自启注册表值正在变化，请重试")
}

var startupSetValue = syscall.NewLazyDLL("advapi32.dll").NewProc("RegSetValueExW")

func (windowsStartupStore) Write(value startupValue) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, startupRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	name, err := syscall.UTF16PtrFromString(startupValueName)
	if err != nil {
		return err
	}
	var data *byte
	if len(value.data) > 0 {
		data = &value.data[0]
	}
	// Preserve the original value type and bytes when rolling back, including
	// uncommon registry types that SetStringValue would otherwise overwrite.
	status, _, _ := startupSetValue.Call(uintptr(key), uintptr(unsafe.Pointer(name)), 0, uintptr(value.typeID), uintptr(unsafe.Pointer(data)), uintptr(len(value.data)))
	if status != 0 {
		return syscall.Errno(status)
	}
	return nil
}

func (windowsStartupStore) Delete() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, startupRunKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer key.Close()
	err = key.DeleteValue(startupValueName)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

func startupValueEnabled(value startupValue) (bool, error) {
	if !value.exists {
		return false, nil
	}
	if value.typeID != registry.SZ && value.typeID != registry.EXPAND_SZ {
		return false, fmt.Errorf("开机自启注册表值类型异常")
	}
	if len(value.data)%2 != 0 {
		return false, fmt.Errorf("开机自启注册表值格式异常")
	}
	words := make([]uint16, len(value.data)/2)
	for i := range words {
		words[i] = binary.LittleEndian.Uint16(value.data[i*2:])
	}
	return strings.TrimSpace(syscall.UTF16ToString(words)) != "", nil
}

func startupStatus() (bool, error) {
	sid, name, err := startupTaskIdentity()
	if err != nil {
		return false, err
	}
	return startupStatusWithStores(windowsStartupTaskStore{name: name, sid: sid}, windowsStartupStore{})
}

func startupCommand(executable string) (string, error) {
	if executable == "" || !filepath.IsAbs(executable) || strings.ContainsAny(executable, "\x00\"") {
		return "", fmt.Errorf("开机自启需要有效的程序绝对路径")
	}
	info, err := os.Stat(executable)
	if err != nil {
		return "", fmt.Errorf("找不到开机自启程序：%w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("开机自启路径必须指向程序文件")
	}
	return `"` + filepath.Clean(executable) + `" --autostart`, nil
}

func prepareStartupChange(executable string) (*scheduledStartupChange, error) {
	sid, name, err := startupTaskIdentity()
	if err != nil {
		return nil, err
	}
	return prepareScheduledStartupChange(executable, sid, windowsStartupTaskStore{name: name, sid: sid}, windowsStartupStore{})
}
