package main

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

const selectionMaxBytes = 60000
const selectionMaxUTF16 = 65536
const selectionClipboardMaxBytes = 64 << 20

var errSelectionUnavailable = errors.New("此程序未提供可直接读取的选中文字")
var errSelectionPassword = errors.New("密码输入框不支持划词翻译")
var errSelectionFocus = errors.New("选中文字的窗口已切换，请重新选择文字后重试")
var errSelectionPending = errors.New("复制尚未完成")
var selectionReaderMu sync.Mutex
var selectionUIAGate = make(chan struct{}, 1)
var selectionOLE = syscall.NewLazyDLL("ole32.dll")
var selectionOLEAut = syscall.NewLazyDLL("oleaut32.dll")

type selectionClipboardBackup interface {
	Sequence() uint32
	Restore(expected uint32) error
	Release()
}

// Function injection keeps all ordering, stale clipboard and restoration tests
// away from the user's desktop, keyboard and clipboard.
type selectionReadAPI struct {
	foreground    func() uintptr
	ownWindow     func(uintptr) bool
	accessible    func(uintptr) (string, error)
	modifiersDown func() bool
	snapshot      func() (selectionClipboardBackup, error)
	sequence      func() uint32
	copy          func(uintptr) error
	readClipboard func(uintptr) (string, uint32, error)
	sleep         func(time.Duration)
}

// readSelectedText reads only the selection of the still-foreground target.
// Passive reads never send input or inspect/modify the clipboard. Explicit
// reads may use Ctrl+C after fully backing up every existing clipboard format.
// It does not raise or activate windows and never logs selected text.
func readSelectedText(target uintptr, copyFallback bool) (string, error) {
	selectionReaderMu.Lock()
	defer selectionReaderMu.Unlock()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return readSelectedTextWithAPI(target, copyFallback, selectionReadAPI{
		foreground: func() uintptr { return call(user, "GetForegroundWindow") },
		ownWindow:  selectionOwnWindow, accessible: selectionUIAText,
		modifiersDown: selectionModifiersDown, snapshot: selectionSnapshotClipboard,
		sequence: selectionClipboardSequence, copy: selectionSendCopy,
		readClipboard: selectionReadClipboard, sleep: time.Sleep,
	})
}

func selectionText(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return "", errors.New("选中文字包含无效字符，请重新选择")
	}
	if len(s) > selectionMaxBytes {
		return "", errors.New("选中文字过长，请缩小选择范围（最多 60000 字节）")
	}
	return s, nil
}

func readSelectedTextWithAPI(target uintptr, fallback bool, api selectionReadAPI) (string, error) {
	if target == 0 || api.foreground() != target || api.ownWindow(target) {
		return "", errSelectionFocus
	}
	selected, err := api.accessible(target)
	if api.foreground() != target {
		return "", errSelectionFocus
	}
	if err == nil {
		selected, err = selectionText(selected)
		if err == nil && selected == "" && fallback {
			err = errors.New("没有选中文字，请先选择要翻译的文字")
		}
		return selected, err
	}
	if err != nil && !errors.Is(err, errSelectionUnavailable) {
		return "", err
	}
	if !fallback {
		return "", nil
	}
	// The hotkey can still have Ctrl/Alt/Shift/Win physically held. Do not
	// manufacture key-up events for keys held by the user.
	for attempt := 0; api.modifiersDown(); attempt++ {
		if attempt >= 50 || api.foreground() != target {
			return "", errors.New("请松开快捷键，再重新触发划词翻译")
		}
		api.sleep(10 * time.Millisecond)
	}
	if api.foreground() != target {
		return "", errSelectionFocus
	}
	backup, err := api.snapshot()
	if err != nil {
		return "", err
	}
	defer backup.Release()
	if api.foreground() != target || api.sequence() != backup.Sequence() || api.modifiersDown() {
		return "", errSelectionFocus
	}
	if err = api.copy(target); err != nil {
		return "", err
	}
	// An unchanged sequence means no copy happened. Reading the old clipboard
	// here would translate unrelated text, so never read it in that case.
	var copiedSequence uint32
	for attempt := 0; attempt < 60; attempt++ {
		if api.foreground() != target {
			if copiedSequence != 0 {
				if restoreErr := backup.Restore(copiedSequence); restoreErr != nil {
					return "", restoreErr
				}
			}
			return "", errSelectionFocus
		}
		if api.sequence() != backup.Sequence() {
			selected, copiedSequence, err = api.readClipboard(target)
			if !errors.Is(err, errSelectionPending) {
				break
			}
		}
		api.sleep(15 * time.Millisecond)
	}
	if copiedSequence == 0 {
		if err != nil && !errors.Is(err, errSelectionPending) {
			return "", err
		}
		return "", errors.New("没有读取到选中文字；请确认文字已选中且该程序允许复制")
	}
	// Restoration verifies the sequence again with the clipboard held open;
	// a newer copy after the confirmed read is left untouched.
	if restoreErr := backup.Restore(copiedSequence); restoreErr != nil {
		return "", restoreErr
	}
	if err != nil {
		if errors.Is(err, errSelectionPending) {
			return "", errors.New("没有读取到可复制的文字，请重新选择文字后重试")
		}
		return "", err
	}
	selected, err = selectionText(selected)
	if err == nil && selected == "" {
		err = errors.New("没有读取到选中文字，请重新选择文字后重试")
	}
	return selected, err
}

func selectionWindowPID(hwnd uintptr) uint32 {
	var pid uint32
	call(user, "GetWindowThreadProcessId", hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

func selectionOwnWindow(hwnd uintptr) bool {
	return selectionWindowPID(hwnd) == uint32(call(kernel, "GetCurrentProcessId"))
}

func selectionModifiersDown() bool {
	for _, key := range []uintptr{0x10, 0x11, 0x12, 0x5b, 0x5c} {
		if call(user, "GetAsyncKeyState", key)&0x8000 != 0 {
			return true
		}
	}
	return false
}

type selectionKeyboardInput struct {
	Type        uint32
	_           uint32 // INPUT union has pointer alignment on Windows x64.
	VK, Scan    uint16
	Flags, Time uint32
	Extra       uintptr
	_           [8]byte // sizeof(INPUT) = 40, including the larger MOUSEINPUT union.
}

func selectionSendCopy(target uintptr) error {
	if call(user, "GetForegroundWindow") != target || selectionModifiersDown() {
		return errSelectionFocus
	}
	if selectionNativePassword(target) {
		return errSelectionPassword
	}
	inputs := []selectionKeyboardInput{
		{Type: 1, VK: 0x11}, {Type: 1, VK: 0x43},
		{Type: 1, VK: 0x43, Flags: 2}, {Type: 1, VK: 0x11, Flags: 2},
	}
	if unsafe.Sizeof(inputs[0]) != 40 {
		return errors.New("此系统架构不支持兼容复制，请使用文字识别")
	}
	if call(user, "SendInput", uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(inputs[0])) != uintptr(len(inputs)) {
		// Release only the two keys this operation itself pressed, in case a
		// partially accepted batch otherwise leaves them logically down.
		up := inputs[2:]
		call(user, "SendInput", uintptr(len(up)), uintptr(unsafe.Pointer(&up[0])), unsafe.Sizeof(up[0]))
		return errors.New("无法复制选中文字；管理员权限程序可能需要相同权限")
	}
	return nil
}

type selectionCOM struct{ VTable *[85]uintptr }

//go:uintptrescapes
func selectionCOMCall(obj *selectionCOM, index int, args ...uintptr) uintptr {
	params := append([]uintptr{uintptr(unsafe.Pointer(obj))}, args...)
	hr, _, _ := syscall.SyscallN(obj.VTable[index], params...)
	runtime.KeepAlive(obj)
	return hr
}

func selectionHRFailed(hr uintptr) bool { return int32(hr) < 0 }
func selectionCOMRelease(obj *selectionCOM) {
	if obj != nil {
		selectionCOMCall(obj, 2)
	}
}

// UIA must run on a non-window-owning MTA thread. The selected-text pattern is
// read-only: no SetFocus, Select or DocumentRange calls are made.
func selectionUIAText(target uintptr) (string, error) {
	select {
	case selectionUIAGate <- struct{}{}:
	default:
		return "", errors.New("当前程序仍在读取选中文字，请稍后重试")
	}
	type reply struct {
		text string
		err  error
	}
	result := make(chan reply, 1)
	go func() {
		defer func() { <-selectionUIAGate }()
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		hr := call(selectionOLE, "CoInitializeEx", 0, 0)
		if selectionHRFailed(hr) {
			result <- reply{err: errSelectionUnavailable}
			return
		}
		defer call(selectionOLE, "CoUninitialize")
		txt, err := selectionUIAThread(target)
		result <- reply{txt, err}
	}()
	select {
	case r := <-result:
		return r.text, r.err
	case <-time.After(3 * time.Second):
		// A hung third-party accessibility provider must not lead to a second,
		// simultaneous Ctrl+C fallback or leave the app waiting indefinitely.
		return "", errors.New("当前程序读取选中文字超时，请重试或使用截图识别")
	}
}

func selectionUIAThread(target uintptr) (string, error) {
	class := syscall.GUID{Data1: 0xe22ad333, Data2: 0xb25f, Data3: 0x460c, Data4: [8]byte{0x83, 0xd0, 0x05, 0x81, 0x10, 0x73, 0x95, 0xc9}}
	iid := syscall.GUID{Data1: 0x34723aff, Data2: 0x0c9d, Data3: 0x49d0, Data4: [8]byte{0x98, 0x96, 0x7a, 0xb5, 0x2d, 0xf8, 0xcd, 0x8a}}
	var automation *selectionCOM
	if selectionHRFailed(call(selectionOLE, "CoCreateInstance", uintptr(unsafe.Pointer(&class)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&automation)))) || automation == nil {
		return "", errSelectionUnavailable
	}
	defer selectionCOMRelease(automation)
	selectionCOMCall(automation, 61, 300) // ConnectionTimeout, milliseconds.
	selectionCOMCall(automation, 63, 300) // TransactionTimeout.
	var element *selectionCOM
	if selectionHRFailed(selectionCOMCall(automation, 8, uintptr(unsafe.Pointer(&element)))) || element == nil {
		return "", errSelectionUnavailable
	}
	defer func() { selectionCOMRelease(element) }()
	var walker *selectionCOM
	selectionCOMCall(automation, 16, uintptr(unsafe.Pointer(&walker))) // RawViewWalker.
	defer selectionCOMRelease(walker)
	for depth := 0; depth < 10 && element != nil; depth++ {
		if call(user, "GetForegroundWindow") != target {
			return "", errSelectionFocus
		}
		var password int32
		if selectionHRFailed(selectionCOMCall(element, 35, uintptr(unsafe.Pointer(&password)))) {
			return "", errSelectionUnavailable
		}
		if password != 0 {
			return "", errSelectionPassword
		}
		var pid int32
		if !selectionHRFailed(selectionCOMCall(element, 20, uintptr(unsafe.Pointer(&pid)))) && uint32(pid) == uint32(call(kernel, "GetCurrentProcessId")) {
			return "", errSelectionFocus
		}
		var hwnd uintptr
		selectionCOMCall(element, 36, uintptr(unsafe.Pointer(&hwnd)))
		if hwnd != 0 && call(user, "GetAncestor", hwnd, 2) != target {
			return "", errSelectionFocus
		}
		pattern := selectionUIATextPattern(element)
		if pattern != nil {
			txt, err := selectionUIAPattern(pattern)
			selectionCOMRelease(pattern)
			if err == nil {
				return txt, nil
			}
			if !errors.Is(err, errSelectionUnavailable) {
				return "", err
			}
		}
		if walker == nil || hwnd == target {
			break
		}
		var parent *selectionCOM
		if selectionHRFailed(selectionCOMCall(walker, 3, uintptr(unsafe.Pointer(element)), uintptr(unsafe.Pointer(&parent)))) || parent == nil {
			break
		}
		selectionCOMRelease(element)
		element = parent
	}
	return "", errSelectionUnavailable
}

func selectionUIAPattern(pattern *selectionCOM) (string, error) {
	var ranges *selectionCOM
	if selectionHRFailed(selectionCOMCall(pattern, 5, uintptr(unsafe.Pointer(&ranges)))) || ranges == nil {
		return "", errSelectionUnavailable
	}
	defer selectionCOMRelease(ranges)
	var count int32
	if selectionHRFailed(selectionCOMCall(ranges, 3, uintptr(unsafe.Pointer(&count)))) {
		return "", errSelectionUnavailable
	}
	if count > 32 {
		return "", errors.New("选中文字范围过多，请缩小选择范围")
	}
	var result strings.Builder
	for i := int32(0); i < count; i++ {
		var textRange *selectionCOM
		if selectionHRFailed(selectionCOMCall(ranges, 4, uintptr(i), uintptr(unsafe.Pointer(&textRange)))) || textRange == nil {
			return "", errSelectionUnavailable
		}
		var bstr uintptr
		hr := selectionCOMCall(textRange, 12, selectionMaxUTF16+1, uintptr(unsafe.Pointer(&bstr)))
		selectionCOMRelease(textRange)
		if selectionHRFailed(hr) {
			return "", errSelectionUnavailable
		}
		if bstr == 0 {
			continue
		}
		n := call(selectionOLEAut, "SysStringLen", bstr)
		if n > selectionMaxUTF16 {
			call(selectionOLEAut, "SysFreeString", bstr)
			return "", errors.New("选中文字过长，请缩小选择范围")
		}
		var txt string
		if n > 0 {
			units := make([]uint16, int(n))
			call(ntdll, "RtlMoveMemory", uintptr(unsafe.Pointer(&units[0])), bstr, n*2)
			txt = string(utf16.Decode(units))
		}
		call(selectionOLEAut, "SysFreeString", bstr)
		if result.Len() != 0 && txt != "" {
			result.WriteByte('\n')
		}
		result.WriteString(txt)
		if result.Len() > selectionMaxBytes {
			return "", errors.New("选中文字过长，请缩小选择范围（最多 60000 字节）")
		}
	}
	return selectionText(result.String())
}

func selectionUIATextPattern(element *selectionCOM) *selectionCOM {
	// GetCurrentPattern returns only IUnknown; an arbitrary provider is not
	// required to put its TextPattern methods on that interface's vtable.
	// GetCurrentPatternAs returns the explicitly requested TextPattern IID.
	iid := syscall.GUID{Data1: 0x32eba289, Data2: 0x3583, Data3: 0x42c9, Data4: [8]byte{0x9c, 0x59, 0x3b, 0x6d, 0x9a, 0x1e, 0x9b, 0x6a}}
	var pattern *selectionCOM
	if selectionHRFailed(selectionCOMCall(element, 14, 10014, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&pattern)))) {
		return nil
	}
	return pattern
}

func selectionClipboardSequence() uint32 { return uint32(call(user, "GetClipboardSequenceNumber")) }

func selectionNativePassword(target uintptr) bool {
	type guiThreadInfo struct {
		Size, Flags                                        uint32
		Active, Focus, Capture, MenuOwner, MoveSize, Caret uintptr
		CaretRect                                          rect
	}
	var pid uint32
	tid := call(user, "GetWindowThreadProcessId", target, uintptr(unsafe.Pointer(&pid)))
	info := guiThreadInfo{}
	info.Size = uint32(unsafe.Sizeof(info))
	if call(user, "GetGUIThreadInfo", tid, uintptr(unsafe.Pointer(&info))) == 0 || info.Focus == 0 {
		return false
	}
	var class [64]uint16
	call(user, "GetClassNameW", info.Focus, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
	return strings.EqualFold(syscall.UTF16ToString(class[:]), "Edit") && call(user, "GetWindowLongPtrW", info.Focus, ^uintptr(15))&0x20 != 0
}

func selectionNextClipboardFormat(previous uint32) (uint32, error) {
	call(kernel, "SetLastError", 0)
	next := uint32(call(user, "EnumClipboardFormats", uintptr(previous)))
	if next == 0 && call(kernel, "GetLastError") != 0 {
		return 0, errors.New("无法完整备份剪贴板，已停止兼容复制")
	}
	return next, nil
}

type selectionClipboardItem struct {
	format uint32
	handle uintptr
	kind   uint8
}
type selectionNativeBackup struct {
	seq   uint32
	owner uintptr
	items []selectionClipboardItem
}

func (b *selectionNativeBackup) Sequence() uint32 { return b.seq }
func (b *selectionNativeBackup) Release() {
	for i := range b.items {
		it := &b.items[i]
		if it.handle == 0 {
			continue
		}
		switch it.kind {
		case 1:
			call(gdi, "DeleteObject", it.handle)
		case 2:
			call(gdi, "DeleteEnhMetaFile", it.handle)
		default:
			call(kernel, "GlobalFree", it.handle)
		}
		it.handle = 0
	}
	if b.owner != 0 {
		call(user, "DestroyWindow", b.owner)
		b.owner = 0
	}
}

func selectionOpenClipboard(owner uintptr) bool {
	for attempt := 0; attempt < 15; attempt++ {
		if call(user, "OpenClipboard", owner) != 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func selectionSnapshotClipboard() (selectionClipboardBackup, error) {
	b := &selectionNativeBackup{}
	ok := false
	defer func() {
		if !ok {
			b.Release()
		}
	}()
	// SetClipboardData after EmptyClipboard requires a non-null owner. This
	// short-lived message-only window belongs to this locked worker thread;
	// it never activates or displays a window on the desktop.
	b.owner = call(user, "CreateWindowExW", 0, uintptr(unsafe.Pointer(str("STATIC"))), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, call(kernel, "GetModuleHandleW", 0), 0)
	if b.owner == 0 {
		return nil, errors.New("无法准备剪贴板备份，请重试")
	}
	if !selectionOpenClipboard(b.owner) {
		return nil, errors.New("剪贴板被其他程序占用，请重试")
	}
	defer call(user, "CloseClipboard")
	total := uintptr(0)
	for format, enumErr := selectionNextClipboardFormat(0); ; format, enumErr = selectionNextClipboardFormat(format) {
		if enumErr != nil {
			return nil, enumErr
		}
		if format == 0 {
			break
		}
		if len(b.items) >= 128 {
			return nil, errors.New("剪贴板格式过多，无法安全备份；请使用截图识别")
		}
		// Owner display, old metafile, palettes, private handles and OLE object
		// references cannot safely be restored by copying their raw bytes.
		if format == 3 || format == 9 || format == 0x80 || format == 0x83 || (format >= 0x200 && format <= 0x3ff) {
			return nil, errors.New("剪贴板含有无法安全恢复的格式，已停止兼容复制；请使用截图识别")
		}
		if format >= 0xc000 {
			var name [256]uint16
			call(user, "GetClipboardFormatNameW", uintptr(format), uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
			nameText := strings.ToLower(syscall.UTF16ToString(name[:]))
			if strings.Contains(nameText, "ole private") || nameText == "dataobject" || nameText == "object descriptor" || nameText == "link source" {
				return nil, errors.New("剪贴板含有程序专用对象，无法安全备份；请使用截图识别")
			}
		}
		handle := call(user, "GetClipboardData", uintptr(format))
		if handle == 0 {
			return nil, errors.New("无法备份现有剪贴板，已停止兼容复制；请重试")
		}
		item := selectionClipboardItem{format: format}
		switch format {
		case 2, 0x82:
			item.kind = 1
			item.handle = call(user, "CopyImage", handle, 0, 0, 0, 0x2000) // LR_CREATEDIBSECTION; no RETURNORG.
		case 14, 0x8e:
			item.kind = 2
			item.handle = call(gdi, "CopyEnhMetaFileW", handle, 0)
		default:
			size := call(kernel, "GlobalSize", handle)
			if size == 0 || size > selectionClipboardMaxBytes || total+size > selectionClipboardMaxBytes {
				return nil, errors.New("现有剪贴板过大或格式无法安全恢复；请使用截图识别")
			}
			total += size
			source := call(kernel, "GlobalLock", handle)
			if source == 0 {
				return nil, errors.New("无法备份现有剪贴板，已停止兼容复制")
			}
			item.handle = call(kernel, "GlobalAlloc", 0x42, size)
			if item.handle != 0 {
				dest := call(kernel, "GlobalLock", item.handle)
				if dest != 0 {
					call(ntdll, "RtlMoveMemory", dest, source, size)
					call(kernel, "GlobalUnlock", item.handle)
				} else {
					call(kernel, "GlobalFree", item.handle)
					item.handle = 0
				}
			}
			call(kernel, "GlobalUnlock", handle)
		}
		if item.handle == 0 {
			return nil, errors.New("无法分配剪贴板备份，已停止兼容复制")
		}
		b.items = append(b.items, item)
	}
	// Delayed rendering during GetClipboardData can itself advance the
	// sequence. Take the baseline after all formats have been materialized.
	b.seq = selectionClipboardSequence()
	if b.seq == 0 {
		return nil, errors.New("无法读取剪贴板状态，已停止兼容复制")
	}
	ok = true
	return b, nil
}

func (b *selectionNativeBackup) Restore(expected uint32) error {
	if !selectionOpenClipboard(b.owner) {
		return errors.New("恢复原剪贴板时被其他程序占用，请重试")
	}
	defer call(user, "CloseClipboard")
	if selectionClipboardSequence() != expected {
		return nil
	} // New user copy wins.
	if call(user, "EmptyClipboard") == 0 {
		return errors.New("无法恢复原剪贴板，请重试")
	}
	failed := false
	for i := range b.items {
		item := &b.items[i]
		if call(user, "SetClipboardData", uintptr(item.format), item.handle) != 0 {
			item.handle = 0
		} else {
			failed = true
		}
	}
	if failed {
		return errors.New("部分原剪贴板格式恢复失败，请重新复制原内容")
	}
	return nil
}

func selectionClipboardOwnedByTarget(target, owner uintptr) bool {
	if owner == 0 {
		return false
	}
	root := call(user, "GetAncestor", owner, 2)
	if root == target {
		return true
	}
	// Several applications use a hidden clipboard owner window. Accept it
	// only for the target process; a different visible window in that process
	// is a different copy source, even if its child owner itself is hidden.
	return selectionWindowPID(owner) == selectionWindowPID(target) && call(user, "IsWindowVisible", owner) == 0 && call(user, "IsWindowVisible", root) == 0
}

func selectionReadClipboard(target uintptr) (string, uint32, error) {
	if call(user, "GetForegroundWindow") != target {
		return "", 0, errSelectionFocus
	}
	if call(user, "OpenClipboard", 0) == 0 {
		// Keep polling bounded. If the target already owns the copied data,
		// retain its sequence so restoration is still attempted on timeout.
		owner := call(user, "GetClipboardOwner")
		if selectionClipboardOwnedByTarget(target, owner) && call(user, "GetForegroundWindow") == target {
			return "", selectionClipboardSequence(), errSelectionPending
		}
		return "", 0, errSelectionPending
	}
	defer call(user, "CloseClipboard")
	owner := call(user, "GetClipboardOwner")
	if !selectionClipboardOwnedByTarget(target, owner) || call(user, "GetForegroundWindow") != target {
		return "", 0, errors.New("剪贴板已被其他程序更新，请重新选择文字后重试")
	}
	seq := selectionClipboardSequence()
	handle := call(user, "GetClipboardData", 13)
	if handle == 0 {
		return "", seq, errSelectionPending
	}
	seq = selectionClipboardSequence()
	size := call(kernel, "GlobalSize", handle)
	if size < 2 || size%2 != 0 || size > (selectionMaxUTF16+1)*2 {
		return "", seq, errors.New("选中文字过长或格式无效，请缩小选择范围")
	}
	p := call(kernel, "GlobalLock", handle)
	if p == 0 {
		return "", seq, errors.New("无法读取已复制的选中文字")
	}
	defer call(kernel, "GlobalUnlock", handle)
	units := make([]uint16, int(size/2))
	call(ntdll, "RtlMoveMemory", uintptr(unsafe.Pointer(&units[0])), p, size)
	if call(user, "GetForegroundWindow") != target {
		return "", 0, errSelectionFocus
	}
	return selectionDecodeUTF16(units, seq)
}

func selectionDecodeUTF16(data []uint16, seq uint32) (string, uint32, error) {
	end := -1
	for i, c := range data {
		if c == 0 {
			end = i
			break
		}
	}
	if end < 0 || end > selectionMaxUTF16 {
		return "", seq, errors.New("选中文字格式无效或过长，请重新选择")
	}
	text, err := selectionText(string(utf16.Decode(data[:end])))
	return text, seq, err
}
