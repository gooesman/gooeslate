package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/gooesman/gooeslate/internal/engine"
)

type stagedConfigurationImport struct {
	token    string
	revision uint64
	config   engine.Config
}

type configurationTransferResult struct {
	OK        bool                        `json:"ok"`
	Cancelled bool                        `json:"cancelled,omitempty"`
	Message   string                      `json:"message,omitempty"`
	Token     string                      `json:"token,omitempty"`
	FileName  string                      `json:"file_name,omitempty"`
	Summary   *configurationImportSummary `json:"summary,omitempty"`
}

func (a *appState) exportConfiguration(owner uintptr, raw json.RawMessage) (configurationTransferResult, error) {
	var options struct {
		IncludeCredentials bool `json:"include_credentials"`
	}
	if err := decodeArgument(raw, &options); err != nil {
		return configurationTransferResult{}, fmt.Errorf("导出选项格式无效")
	}
	if a.testMode {
		return configurationTransferResult{OK: true, Cancelled: true, Message: "界面自测不会导出个人配置"}, nil
	}
	if a.configurationBusy {
		return configurationTransferResult{}, fmt.Errorf("请先完成当前配置操作")
	}
	a.configurationBusy = true
	defer func() { a.configurationBusy = false }()
	// Native dialogs pump messages. Encode a saved snapshot before opening one.
	data, err := encodeConfiguration(cloneConfig(a.cfg), options.IncludeCredentials)
	if err != nil {
		return configurationTransferResult{}, err
	}
	file, err := configurationFileDialog(owner, true)
	if err != nil {
		return configurationTransferResult{}, err
	}
	if file == "" {
		return configurationTransferResult{OK: true, Cancelled: true}, nil
	}
	if internalConfigurationPath(file) {
		return configurationTransferResult{}, fmt.Errorf("请选择其他文件，不要覆盖应用正在使用的设置或测试记录")
	}
	if err := writeConfigurationExport(file, data); err != nil {
		return configurationTransferResult{}, fmt.Errorf("配置文件导出失败，请检查文件位置和写入权限")
	}
	return configurationTransferResult{OK: true, Message: "配置已导出", FileName: filepath.Base(file)}, nil
}

func (a *appState) chooseConfigurationImport(owner uintptr) (configurationTransferResult, error) {
	if a.testMode {
		return configurationTransferResult{OK: true, Cancelled: true, Message: "界面自测不会读取个人配置文件"}, nil
	}
	if a.configurationBusy {
		return configurationTransferResult{}, fmt.Errorf("请先完成当前配置操作")
	}
	a.configurationBusy = true
	defer func() { a.configurationBusy = false }()
	a.pendingConfigurationImport = nil
	revision := a.configurationRevision
	file, err := configurationFileDialog(owner, false)
	if err != nil {
		return configurationTransferResult{}, err
	}
	if file == "" {
		return configurationTransferResult{OK: true, Cancelled: true}, nil
	}
	data, err := readConfigurationImport(file)
	if err != nil {
		return configurationTransferResult{}, err
	}
	if revision != a.configurationRevision {
		return configurationTransferResult{}, fmt.Errorf("选取文件期间设置已更改，请重新选择配置文件")
	}
	return a.stageConfigurationImport(data, filepath.Base(file))
}

func (a *appState) stageConfigurationImport(data []byte, name string) (configurationTransferResult, error) {
	a.pendingConfigurationImport = nil
	cfg, summary, err := decodeConfiguration(data)
	if err != nil {
		return configurationTransferResult{}, err
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return configurationTransferResult{}, fmt.Errorf("无法准备配置导入，请重试")
	}
	token := hex.EncodeToString(tokenBytes)
	a.pendingConfigurationImport = &stagedConfigurationImport{token: token, revision: a.configurationRevision, config: cfg}
	// Only handpicked metadata reaches the preview, never the imported config.
	return configurationTransferResult{OK: true, Token: token, FileName: filepath.Base(name), Summary: &summary}, nil
}

func (a *appState) configurationImportCandidate(token string) (engine.Config, error) {
	pending := a.pendingConfigurationImport
	if pending == nil || token == "" || token != pending.token || pending.revision != a.configurationRevision {
		return engine.Config{}, fmt.Errorf("导入预览已失效，请重新选择配置文件")
	}
	return cloneConfig(pending.config), nil
}

func (a *appState) applyConfigurationImport(token string) (apiResult, error) {
	if a.configurationBusy {
		return apiResult{}, fmt.Errorf("请先完成当前配置操作")
	}
	cfg, err := a.configurationImportCandidate(token)
	if err != nil {
		return apiResult{}, err
	}
	// Imported credentials are independent. Never run mergeSettings here.
	result, err := a.applyConfiguration(cfg, true, true)
	if err == nil && !a.testMode {
		result.Message = "配置已导入，快捷键已生效；请测试导入的服务"
		a.cancelServiceProbes()
		a.serviceProbeResults = nil
		if err := saveServiceProbeResults(nil); err != nil {
			result.Message = "配置已导入；旧的测试记录未能清除，请重新测试服务"
		}
	}
	return result, err
}

func (a *appState) cancelConfigurationImport(token string) apiResult {
	if pending := a.pendingConfigurationImport; pending != nil && pending.token == token {
		a.pendingConfigurationImport = nil
	}
	return apiResult{OK: true}
}

func readConfigurationImport(file string) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("无法读取配置文件，请检查文件位置和读取权限")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("请选择普通 JSON 配置文件")
	}
	if info.Size() > configurationFileSizeLimit {
		return nil, fmt.Errorf("配置文件不能超过 2 MB")
	}
	data, err := io.ReadAll(io.LimitReader(f, configurationFileSizeLimit+1))
	if err != nil {
		return nil, fmt.Errorf("配置文件读取失败，请重试")
	}
	if len(data) > configurationFileSizeLimit {
		return nil, fmt.Errorf("配置文件不能超过 2 MB")
	}
	return data, nil
}

func sameConfigurationPath(first, second string) bool {
	firstAbs, firstErr := filepath.Abs(first)
	secondAbs, secondErr := filepath.Abs(second)
	if firstErr != nil || secondErr != nil {
		return false
	}
	if strings.EqualFold(filepath.Clean(firstAbs), filepath.Clean(secondAbs)) {
		return true
	}
	firstInfo, firstErr := os.Stat(firstAbs)
	secondInfo, secondErr := os.Stat(secondAbs)
	return firstErr == nil && secondErr == nil && os.SameFile(firstInfo, secondInfo)
}

func internalConfigurationPath(file string) bool {
	return sameConfigurationPath(file, configFile()) || sameConfigurationPath(file, serviceProbeRecordsFile())
}

func writeConfigurationExport(file string, data []byte) error {
	if internalConfigurationPath(file) {
		return fmt.Errorf("不能覆盖应用正在使用的配置文件")
	}
	// Write beside the destination, then rename; failure keeps the old file.
	f, err := os.CreateTemp(filepath.Dir(file), ".gooestranslate-export-*.tmp")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, file)
}

func configurationFileDialog(owner uintptr, save bool) (string, error) {
	buf := make([]uint16, 32768)
	var filter []uint16
	for _, entry := range []string{"鹅译配置 (JSON)", "*.json", ""} {
		filter = append(filter, syscall.StringToUTF16(entry)...)
	}
	title, proc := "导入配置", "GetOpenFileNameW"
	flags := uint32(0x80000 | 0x800 | 0x1000 | 8) // Explorer, path/file must exist, no CWD change.
	if save {
		title, proc = "导出配置", "GetSaveFileNameW"
		flags = 0x80000 | 0x800 | 2 | 8 // Confirm overwriting a chosen file.
		copy(buf, syscall.StringToUTF16("gooeslate-config-"+time.Now().Format("20060102")+".json"))
	}
	o := openfilename{Owner: owner, File: &buf[0], MaxFile: uint32(len(buf)), Filter: &filter[0], FilterIndex: 1, Title: str(title), Flags: flags, DefaultExt: str("json")}
	o.Size = uint32(unsafe.Sizeof(o))
	if call(dialogs, proc, uintptr(unsafe.Pointer(&o))) == 0 {
		if call(dialogs, "CommDlgExtendedError") != 0 {
			return "", fmt.Errorf("无法打开配置文件选择窗口，请重试")
		}
		return "", nil
	}
	return syscall.UTF16ToString(buf), nil
}

// The caller commits in-memory state only after both steps succeed.
func persistConfigurationChange(old, next engine.Config, replace func(engine.Hotkeys) []string, save func(engine.Config) error) error {
	restore := func(failure error) error {
		if errs := replace(effectiveHotkeys(old)); len(errs) > 0 {
			return fmt.Errorf("%w；原有快捷键未能全部恢复：%s", failure, strings.Join(errs, "；"))
		}
		return failure
	}
	if errs := replace(effectiveHotkeys(next)); len(errs) > 0 {
		return restore(fmt.Errorf("快捷键保存失败：%s", strings.Join(errs, "；")))
	}
	if err := save(next); err != nil {
		return restore(fmt.Errorf("设置写入失败：%w", err))
	}
	return nil
}
