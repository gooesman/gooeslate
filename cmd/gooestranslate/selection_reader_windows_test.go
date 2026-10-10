package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"
)

type selectionFakeBackup struct {
	seq        uint32
	calls      *[]string
	restoreErr error
	checkSeq   uint32
}

func (b *selectionFakeBackup) Sequence() uint32 { return b.seq }
func (b *selectionFakeBackup) Restore(seq uint32) error {
	*b.calls = append(*b.calls, "restore")
	b.checkSeq = seq
	return b.restoreErr
}
func (b *selectionFakeBackup) Release() { *b.calls = append(*b.calls, "release") }

type selectionFakeReader struct {
	api                                   selectionReadAPI
	backup                                *selectionFakeBackup
	calls                                 []string
	fg                                    uintptr
	seq                                   uint32
	text                                  string
	uia                                   string
	uiaErr, snapshotErr, copyErr, readErr error
	modifiers                             bool
	sleeps                                int
}

func newSelectionFake() *selectionFakeReader {
	f := &selectionFakeReader{fg: 7, seq: 10, text: "selected", uiaErr: errSelectionUnavailable}
	f.backup = &selectionFakeBackup{seq: 10, calls: &f.calls}
	f.api = selectionReadAPI{
		foreground: func() uintptr { return f.fg }, ownWindow: func(uintptr) bool { return false },
		accessible:    func(uintptr) (string, error) { f.calls = append(f.calls, "uia"); return f.uia, f.uiaErr },
		modifiersDown: func() bool { return f.modifiers }, sequence: func() uint32 { return f.seq },
		snapshot: func() (selectionClipboardBackup, error) {
			f.calls = append(f.calls, "snapshot")
			return f.backup, f.snapshotErr
		},
		copy: func(uintptr) error { f.calls = append(f.calls, "copy"); f.seq = 11; return f.copyErr },
		readClipboard: func(uintptr) (string, uint32, error) {
			f.calls = append(f.calls, "read")
			return f.text, f.seq, f.readErr
		},
		sleep: func(time.Duration) { f.sleeps++ },
	}
	return f
}

func TestSelectionPassiveNeverReadsClipboardOrSendsKeys(t *testing.T) {
	for _, tc := range []struct {
		text string
		err  error
		want string
	}{{"", errSelectionUnavailable, ""}, {"", nil, ""}, {" chosen ", nil, "chosen"}} {
		f := newSelectionFake()
		f.uia, f.uiaErr = tc.text, tc.err
		got, err := readSelectedTextWithAPI(7, false, f.api)
		if err != nil || got != tc.want || !reflect.DeepEqual(f.calls, []string{"uia"}) {
			t.Fatalf("passive read changed clipboard/keyboard: text=%q err=%v calls=%v", got, err, f.calls)
		}
	}
}

func TestSelectionUIAFirstAndPasswordNeverFallback(t *testing.T) {
	f := newSelectionFake()
	f.uia = "selected via accessibility"
	f.uiaErr = nil
	got, err := readSelectedTextWithAPI(7, true, f.api)
	if err != nil || got != f.uia || !reflect.DeepEqual(f.calls, []string{"uia"}) {
		t.Fatal("accessible selection must bypass clipboard")
	}
	f = newSelectionFake()
	f.uiaErr = errSelectionPassword
	if _, err := readSelectedTextWithAPI(7, true, f.api); !errors.Is(err, errSelectionPassword) || !reflect.DeepEqual(f.calls, []string{"uia"}) {
		t.Fatal("password field must not trigger copy")
	}
	f = newSelectionFake()
	f.uiaErr = nil
	if _, err := readSelectedTextWithAPI(7, true, f.api); err == nil || !reflect.DeepEqual(f.calls, []string{"uia"}) {
		t.Fatal("known empty caret selection must not copy current line")
	}
}

func TestSelectionWindowGuards(t *testing.T) {
	for _, own := range []bool{false, true} {
		f := newSelectionFake()
		if !own {
			f.fg = 8
		}
		f.api.ownWindow = func(uintptr) bool { return own }
		if _, err := readSelectedTextWithAPI(7, true, f.api); err == nil || len(f.calls) != 0 {
			t.Fatal("wrong/own target must be rejected before reading")
		}
	}
	f := newSelectionFake()
	f.api.accessible = func(uintptr) (string, error) { f.fg = 8; return "old selection", nil }
	if _, err := readSelectedTextWithAPI(7, true, f.api); err == nil || len(f.calls) != 0 {
		t.Fatal("foreground changed during accessibility read")
	}
}

func TestSelectionClipboardOrderAndRestoration(t *testing.T) {
	f := newSelectionFake()
	got, err := readSelectedTextWithAPI(7, true, f.api)
	want := []string{"uia", "snapshot", "copy", "read", "restore", "release"}
	if err != nil || got != "selected" || !reflect.DeepEqual(f.calls, want) || f.backup.checkSeq != 11 {
		t.Fatalf("ordering/sequence guard: %q %v %v", got, err, f.calls)
	}
}

func TestSelectionNoCopyWhenBackupUnsupportedOrForegroundChanged(t *testing.T) {
	f := newSelectionFake()
	f.snapshotErr = errors.New("unsupported format")
	if _, err := readSelectedTextWithAPI(7, true, f.api); err == nil || !reflect.DeepEqual(f.calls, []string{"uia", "snapshot"}) {
		t.Fatal("unsafe clipboard must abort before copy")
	}
	for _, change := range []string{"foreground", "clipboard", "modifier"} {
		f = newSelectionFake()
		f.api.snapshot = func() (selectionClipboardBackup, error) {
			f.calls = append(f.calls, "snapshot")
			switch change {
			case "foreground":
				f.fg = 8
			case "clipboard":
				f.seq = 12
			case "modifier":
				f.modifiers = true
			}
			return f.backup, nil
		}
		if _, err := readSelectedTextWithAPI(7, true, f.api); err == nil || !reflect.DeepEqual(f.calls, []string{"uia", "snapshot", "release"}) {
			t.Fatalf("late %s change must abort before copy: %v", change, f.calls)
		}
	}
}

func TestSelectionUnchangedClipboardNeverReturnsStaleText(t *testing.T) {
	f := newSelectionFake()
	f.api.copy = func(uintptr) error { f.calls = append(f.calls, "copy"); return nil }
	got, err := readSelectedTextWithAPI(7, true, f.api)
	if got != "" || err == nil || !reflect.DeepEqual(f.calls, []string{"uia", "snapshot", "copy", "release"}) || f.sleeps != 60 {
		t.Fatalf("stale clipboard read: %q %v %v", got, err, f.calls)
	}
}

func TestSelectionFocusChangeDuringCopyWaitDoesNotReadOtherWindow(t *testing.T) {
	f := newSelectionFake()
	f.api.copy = func(uintptr) error { f.calls = append(f.calls, "copy"); f.seq = 11; f.fg = 8; return nil }
	if got, err := readSelectedTextWithAPI(7, true, f.api); got != "" || !errors.Is(err, errSelectionFocus) || !reflect.DeepEqual(f.calls, []string{"uia", "snapshot", "copy", "release"}) {
		t.Fatalf("different foreground must not be read/restored: %q %v %v", got, err, f.calls)
	}
}

func TestSelectionUIABusyDoesNotSpawnOrFallback(t *testing.T) {
	selectionUIAGate <- struct{}{}
	defer func() { <-selectionUIAGate }()
	if got, err := selectionUIAText(0); got != "" || err == nil || len(selectionUIAGate) != 1 {
		t.Fatal("stalled provider must retain one query gate and fail fast")
	}
}

func TestSelectionFallbackRestoresBeforeValidationErrors(t *testing.T) {
	for _, tc := range []struct {
		text    string
		readErr error
	}{{strings.Repeat("x", 60001), nil}, {"", nil}, {"", errors.New("invalid copy")}, {"", errSelectionPending}} {
		f := newSelectionFake()
		f.text, f.readErr = tc.text, tc.readErr
		if _, err := readSelectedTextWithAPI(7, true, f.api); err == nil {
			t.Fatal("invalid/empty/unfinished copy must fail")
		}
		if f.backup.checkSeq != 11 || f.calls[len(f.calls)-2] != "restore" || f.calls[len(f.calls)-1] != "release" {
			t.Fatalf("must restore even if selected text invalid: %v", f.calls)
		}
	}
}

func TestSelectionModifierWaitBoundedAndDoesNotReleaseUserKeys(t *testing.T) {
	f := newSelectionFake()
	f.modifiers = true
	if _, err := readSelectedTextWithAPI(7, true, f.api); err == nil || f.sleeps != 50 || !reflect.DeepEqual(f.calls, []string{"uia"}) {
		t.Fatal("held modifiers must time out without clipboard/key mutation")
	}
	f = newSelectionFake()
	f.modifiers = true
	f.api.sleep = func(time.Duration) { f.sleeps++; f.modifiers = false }
	if _, err := readSelectedTextWithAPI(7, true, f.api); err != nil || f.sleeps != 1 {
		t.Fatal("copy should work after physical modifiers release")
	}
}

func TestSelectionRestoreFailureDoesNotReturnTranslationText(t *testing.T) {
	f := newSelectionFake()
	f.backup.restoreErr = errors.New("restore failed")
	if got, err := readSelectedTextWithAPI(7, true, f.api); got != "" || err == nil || f.calls[len(f.calls)-1] != "release" {
		t.Fatal("clipboard failure must be visible")
	}
}

func TestSelectionUTF16AndSizeLimits(t *testing.T) {
	input := append(utf16.Encode([]rune("中文 😀")), 0)
	if got, seq, err := selectionDecodeUTF16(input, 17); err != nil || got != "中文 😀" || seq != 17 {
		t.Fatalf("UTF16 decode: %q %d %v", got, seq, err)
	}
	for _, input := range [][]uint16{{'x'}, append(make([]uint16, selectionMaxUTF16+1), 0)} {
		if input[0] == 0 {
			for i := 0; i < len(input)-1; i++ {
				input[i] = 'x'
			}
		}
		if _, _, err := selectionDecodeUTF16(input, 17); err == nil {
			t.Fatal("unterminated/oversized UTF16 must be rejected")
		}
	}
	if _, err := selectionText(strings.Repeat("中", 20000)); err != nil {
		t.Fatal("60000-byte boundary must work")
	}
	if _, err := selectionText(strings.Repeat("中", 20001)); err == nil {
		t.Fatal("byte limit must apply to multibyte characters")
	}
	if _, err := selectionText("a\x00b"); err == nil {
		t.Fatal("embedded null must not be sent to provider")
	}
	if unsafe.Sizeof(selectionKeyboardInput{}) != 40 {
		t.Fatal("Windows x64 INPUT layout changed")
	}
}

// Native COM ABI is exercised using a fixture owned by this test. It does not
// inspect the desktop or clipboard and never sends input.
func TestSelectionUIATextPatternCOMFixture(t *testing.T) {
	var rangeVT, arrayVT, patternVT [85]uintptr
	textRange := &selectionCOM{VTable: &rangeVT}
	array := &selectionCOM{VTable: &arrayVT}
	pattern := &selectionCOM{VTable: &patternVT}
	releaseCount := 0
	release := syscall.NewCallback(func(uintptr) uintptr { releaseCount++; return 0 })
	rangeVT[2], arrayVT[2] = release, release
	rangeVT[12] = syscall.NewCallback(func(this, max uintptr, p *uintptr) uintptr {
		if max != selectionMaxUTF16+1 {
			t.Error("GetText must be bounded")
		}
		fixture := utf16.Encode([]rune("Selected 中文 😀"))
		*p = call(selectionOLEAut, "SysAllocStringLen", uintptr(unsafe.Pointer(&fixture[0])), uintptr(len(fixture)))
		return 0
	})
	arrayVT[3] = syscall.NewCallback(func(this uintptr, p *int32) uintptr { *p = 1; return 0 })
	arrayVT[4] = syscall.NewCallback(func(this, index uintptr, p **selectionCOM) uintptr {
		*p = textRange
		return 0
	})
	patternVT[5] = syscall.NewCallback(func(this uintptr, p **selectionCOM) uintptr {
		*p = array
		return 0
	})
	got, err := selectionUIAPattern(pattern)
	if err != nil || got != "Selected 中文 😀" || releaseCount != 2 {
		t.Fatalf("COM fixture: %q %v release=%d", got, err, releaseCount)
	}
}

func TestSelectionUIARequestsTextInterfaceInsteadOfIUnknown(t *testing.T) {
	var elementVT, patternVT [85]uintptr
	element, pattern := &selectionCOM{VTable: &elementVT}, &selectionCOM{VTable: &patternVT}
	requested := false
	elementVT[14] = syscall.NewCallback(func(this, id uintptr, iid *syscall.GUID, result **selectionCOM) uintptr {
		requested = id == 10014 && iid.Data1 == 0x32eba289 && iid.Data4 == [8]byte{0x9c, 0x59, 0x3b, 0x6d, 0x9a, 0x1e, 0x9b, 0x6a}
		*result = pattern
		return 0
	})
	if got := selectionUIATextPattern(element); got != pattern || !requested {
		t.Fatal("must request exact IUIAutomationTextPattern IID, not cast IUnknown")
	}
}

func TestSelectionNativeEditOwnedFixture(t *testing.T) {
	if os.Getenv("GOOESTRANSLATE_SELECTION_FIXTURE") == "1" {
		selectionRunOwnedEditFixture()
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestSelectionNativeEditOwnedFixture$")
	cmd.Env = append(os.Environ(), "GOOESTRANSLATE_SELECTION_FIXTURE=1")
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		input.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	}()
	lines := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		if scanner.Scan() {
			lines <- scanner.Text()
		} else {
			lines <- ""
		}
	}()
	var line string
	select {
	case line = <-lines:
	case <-time.After(5 * time.Second):
		t.Fatal("owned EDIT fixture did not start")
	}
	var controls struct{ Text, Password uintptr }
	if err = json.Unmarshal([]byte(line), &controls); err != nil || controls.Text == 0 || controls.Password == 0 {
		t.Fatalf("fixture handles invalid: %v", err)
	}
	type result struct {
		text     string
		password bool
		err      error
	}
	results := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if selectionHRFailed(call(selectionOLE, "CoInitializeEx", 0, 0)) {
			results <- result{err: errors.New("COM initialization failed")}
			return
		}
		defer call(selectionOLE, "CoUninitialize")
		var r result
		class := syscall.GUID{Data1: 0xe22ad333, Data2: 0xb25f, Data3: 0x460c, Data4: [8]byte{0x83, 0xd0, 0x05, 0x81, 0x10, 0x73, 0x95, 0xc9}}
		iid := syscall.GUID{Data1: 0x34723aff, Data2: 0x0c9d, Data3: 0x49d0, Data4: [8]byte{0x98, 0x96, 0x7a, 0xb5, 0x2d, 0xf8, 0xcd, 0x8a}}
		var automation *selectionCOM
		if selectionHRFailed(call(selectionOLE, "CoCreateInstance", uintptr(unsafe.Pointer(&class)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&automation)))) || automation == nil {
			results <- result{err: errors.New("UIA initialization failed")}
			return
		}
		defer selectionCOMRelease(automation)
		selectionCOMCall(automation, 61, 500)
		selectionCOMCall(automation, 63, 500)
		for idx, hwnd := range []uintptr{controls.Text, controls.Password} {
			var element *selectionCOM
			if selectionHRFailed(selectionCOMCall(automation, 6, hwnd, uintptr(unsafe.Pointer(&element)))) || element == nil {
				r.err = errors.New("ElementFromHandle failed")
				break
			}
			var password int32
			if selectionHRFailed(selectionCOMCall(element, 35, uintptr(unsafe.Pointer(&password)))) {
				selectionCOMRelease(element)
				r.err = errors.New("IsPassword failed")
				break
			}
			if idx == 1 {
				r.password = password != 0
				selectionCOMRelease(element)
				continue
			}
			pattern := selectionUIATextPattern(element)
			if pattern == nil {
				selectionCOMRelease(element)
				r.err = errors.New("owned EDIT TextPattern unavailable")
				break
			}
			r.text, r.err = selectionUIAPattern(pattern)
			selectionCOMRelease(pattern)
			selectionCOMRelease(element)
			if r.err != nil {
				break
			}
		}
		results <- r
	}()
	select {
	case r := <-results:
		if r.err != nil || r.text != "中文 😀\r\nSecond line" || !r.password {
			t.Fatalf("native EDIT selection/password: text=%q password=%v err=%v", r.text, r.password, r.err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("native owned EDIT UIA timed out")
	}
}

func selectionRunOwnedEditFixture() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	proc := syscall.NewCallback(func(hwnd uintptr, msg uint32, w, l uintptr) uintptr {
		if msg == 0x10 {
			call(user, "DestroyWindow", hwnd)
			call(user, "PostQuitMessage", 0)
			return 0
		}
		return call(user, "DefWindowProcW", hwnd, uintptr(msg), w, l)
	})
	class := fmt.Sprintf("GooesSelectionFixture%d", os.Getpid())
	if register(class, proc, 32512) != nil {
		return
	}
	parent := create(class, "Owned selection test fixture", 0, 0, 0, 0, 500, 240, 0, 0)
	if parent == 0 {
		return
	}
	defer call(user, "DestroyWindow", parent)
	edit := create("EDIT", "prefix 中文 😀\r\nSecond line suffix", 0x40000000|0x4|0x1000, 0, 0, 0, 400, 160, parent, 1)
	password := create("EDIT", "private fixture only", 0x40000000|0x20, 0, 0, 170, 400, 40, parent, 2)
	start := len(utf16.Encode([]rune("prefix ")))
	end := start + len(utf16.Encode([]rune("中文 😀\r\nSecond line")))
	call(user, "SendMessageW", edit, 0xb1, uintptr(start), uintptr(end)) // EM_SETSEL, only this generated control.
	json.NewEncoder(os.Stdout).Encode(struct{ Text, Password uintptr }{edit, password})
	go func() { bufio.NewReader(os.Stdin).ReadByte(); call(user, "PostMessageW", parent, 0x10, 0, 0) }()
	var msg message
	for int32(call(user, "GetMessageW", uintptr(unsafe.Pointer(&msg)), 0, 0, 0)) > 0 {
		call(user, "TranslateMessage", uintptr(unsafe.Pointer(&msg)))
		call(user, "DispatchMessageW", uintptr(unsafe.Pointer(&msg)))
	}
}
