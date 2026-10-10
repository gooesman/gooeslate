package main

import (
	"fmt"
	"image/png"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

type webViewRenderStats struct {
	Width, Height, Samples, NonWhitePixels, DistinctColors int
}

func inspectWebViewPNG(file string) (webViewRenderStats, error) {
	var stats webViewRenderStats
	f, err := os.Open(file)
	if err != nil {
		return stats, err
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return stats, fmt.Errorf("WebView PNG 无效：%w", err)
	}
	stats.Width, stats.Height = cfg.Width, cfg.Height
	if cfg.Width < 160 || cfg.Height < 160 || int64(cfg.Width)*int64(cfg.Height) > 40000000 {
		return stats, fmt.Errorf("WebView PNG 尺寸无效：%d × %d", cfg.Width, cfg.Height)
	}
	if _, err = f.Seek(0, 0); err != nil {
		return stats, err
	}
	img, err := png.Decode(f)
	if err != nil {
		return stats, err
	}
	colors := map[uint32]bool{}
	step := max(1, min(cfg.Width, cfg.Height)/120)
	for y := 0; y < cfg.Height; y += step {
		for x := 0; x < cfg.Width; x += step {
			r, g, b, a := img.At(x, y).RGBA()
			stats.Samples++
			if r < 0xf500 || g < 0xf500 || b < 0xf500 || a < 0xf500 {
				stats.NonWhitePixels++
			}
			colors[uint32(r>>11)<<10|uint32(g>>11)<<5|uint32(b>>11)] = true
		}
	}
	stats.DistinctColors = len(colors)
	if stats.NonWhitePixels*50 < stats.Samples || stats.DistinctColors < 6 {
		return stats, fmt.Errorf("WebView 渲染为空白或单色：非白像素 %d/%d，颜色 %d", stats.NonWhitePixels, stats.Samples, stats.DistinctColors)
	}
	return stats, nil
}

//go:uintptrescapes
func renderCOM(object unsafe.Pointer, slot int, args ...uintptr) uintptr {
	vt := *(**[31]uintptr)(object)
	returnValue, _, _ := syscall.SyscallN(vt[slot], append([]uintptr{uintptr(object)}, args...)...)
	return returnValue
}

func renderHRESULT(hr uintptr, operation string) error {
	if int32(hr) < 0 {
		return fmt.Errorf("%s 失败：HRESULT 0x%08X", operation, uint32(hr))
	}
	return nil
}

func controllerVisible(w *webWindow) (bool, error) {
	if w == nil || w.browser == nil || w.browser.GetController() == nil {
		return false, fmt.Errorf("WebView 控制器未就绪")
	}
	var visible int32
	hr := renderCOM(unsafe.Pointer(w.browser.GetController()), 3, uintptr(unsafe.Pointer(&visible)))
	return visible != 0, renderHRESULT(hr, "GetIsVisible")
}

func controllerBounds(w *webWindow) (rect, error) {
	var bounds rect
	if w == nil || w.browser == nil || w.browser.GetController() == nil {
		return bounds, fmt.Errorf("WebView 控制器未就绪")
	}
	hr := renderCOM(unsafe.Pointer(w.browser.GetController()), 5, uintptr(unsafe.Pointer(&bounds)))
	return bounds, renderHRESULT(hr, "GetBounds")
}

var renderDispatchMutex sync.Mutex
var renderDispatchJobs = map[uintptr]func(){}
var renderDispatchProc = syscall.NewCallback(func(hwnd uintptr, msg uint32, timer, tick uintptr) uintptr {
	call(user, "KillTimer", 0, timer)
	renderDispatchMutex.Lock()
	f := renderDispatchJobs[timer]
	delete(renderDispatchJobs, timer)
	renderDispatchMutex.Unlock()
	if f != nil {
		f()
	}
	return 0
})

// A thread timer delivers the result after the COM callback returns, keeping
// completion on the initiating UI thread without nesting a message loop.
func deferRenderResult(file string, err error, completed func(error)) {
	if completed == nil {
		return
	}
	f := func() {
		if err == nil {
			_, err = inspectWebViewPNG(file)
		}
		completed(err)
	}
	timer := call(user, "SetTimer", 0, 0, 10, renderDispatchProc)
	if timer == 0 {
		completed(fmt.Errorf("无法投递渲染完成回调"))
		return
	}
	renderDispatchMutex.Lock()
	renderDispatchJobs[timer] = f
	renderDispatchMutex.Unlock()
}

type webRenderCallback struct {
	VTable *[4]uintptr
	Refs   atomic.Int32
	Op     *webRenderOperation
}

type webRenderOperation struct {
	Callback  webRenderCallback
	Pin       runtime.Pinner
	Stream    unsafe.Pointer
	Core      unsafe.Pointer
	File      string
	Completed func(error)
	Finished  atomic.Bool
}

var webRenderMutex sync.Mutex
var webRenderOperations = map[*webRenderCallback]*webRenderOperation{}
var webRenderIUnknown = [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0xc0, 0, 0, 0, 0, 0, 0, 0x46}
var webRenderCaptureIID = [16]byte{0xe9, 0x05, 0x7e, 0x69, 0x8f, 0x3d, 0xfa, 0x45, 0x96, 0xf4, 0x8f, 0xfe, 0x1e, 0xde, 0xda, 0xf5}

func webRenderAddRef(this *webRenderCallback) uintptr {
	return uintptr(this.Refs.Add(1))
}

func webRenderRelease(this *webRenderCallback) uintptr {
	refs := this.Refs.Add(-1)
	if refs == 0 {
		webRenderMutex.Lock()
		op := webRenderOperations[this]
		delete(webRenderOperations, this)
		webRenderMutex.Unlock()
		if op != nil {
			op.Pin.Unpin()
		}
	}
	return uintptr(refs)
}

var webRenderVTable = [4]uintptr{
	syscall.NewCallback(func(this *webRenderCallback, iid *[16]byte, result **webRenderCallback) uintptr {
		if result == nil {
			return 0x80004003 // E_POINTER
		}
		*result = nil
		if iid == nil {
			return 0x80004002
		}
		id := *iid
		if id != webRenderIUnknown && id != webRenderCaptureIID {
			return 0x80004002 // E_NOINTERFACE
		}
		*result = this
		webRenderAddRef(this)
		return 0
	}),
	syscall.NewCallback(webRenderAddRef),
	syscall.NewCallback(webRenderRelease),
	syscall.NewCallback(func(this *webRenderCallback, hr uintptr) uintptr {
		this.Op.finish(renderHRESULT(hr, "CapturePreview"))
		return 0
	}),
}

func (op *webRenderOperation) finish(err error) {
	if !op.Finished.CompareAndSwap(false, true) {
		return
	}
	if op.Stream != nil {
		if err == nil {
			err = renderHRESULT(renderCOM(op.Stream, 8, 0), "IStream.Commit")
		}
		renderCOM(op.Stream, 2)
		op.Stream = nil
	}
	if op.Core != nil {
		renderCOM(op.Core, 2)
		op.Core = nil
	}
	deferRenderResult(op.File, err, op.Completed)
}

// captureWebView captures ONLY this application's supplied WebView into a PNG.
// Call on its UI thread after navigation completes and while the controller is
// visible. Completion is asynchronous on that thread and verifies rendered PNG
// pixels, rather than only checking that JavaScript/DOM initialization ran.
func captureWebView(w *webWindow, file string, completed func(error)) {
	if w == nil || w.browser == nil || w.browser.GetController() == nil {
		deferRenderResult(file, fmt.Errorf("WebView 控制器未就绪"), completed)
		return
	}
	op := &webRenderOperation{File: file, Completed: completed}
	controller := unsafe.Pointer(w.browser.GetController())
	if err := renderHRESULT(renderCOM(controller, 25, uintptr(unsafe.Pointer(&op.Core))), "GetCoreWebView2"); err != nil || op.Core == nil {
		if err == nil {
			err = fmt.Errorf("WebView 核心对象为空")
		}
		op.finish(err)
		return
	}
	shlwapi := syscall.NewLazyDLL("shlwapi.dll")
	hr := call(shlwapi, "SHCreateStreamOnFileEx", uintptr(unsafe.Pointer(str(file))), 0x1042, 0x80, 1, 0, uintptr(unsafe.Pointer(&op.Stream)))
	if err := renderHRESULT(hr, "SHCreateStreamOnFileEx"); err != nil {
		op.finish(err)
		return
	}
	op.Callback.VTable, op.Callback.Op = &webRenderVTable, op
	op.Callback.Refs.Store(1)
	op.Pin.Pin(&op.Callback)
	webRenderMutex.Lock()
	webRenderOperations[&op.Callback] = op
	webRenderMutex.Unlock()
	hr = renderCOM(op.Core, 30, 0, uintptr(op.Stream), uintptr(unsafe.Pointer(&op.Callback))) // PNG = 0
	if err := renderHRESULT(hr, "CapturePreview"); err != nil {
		op.finish(err)
	}
	webRenderRelease(&op.Callback)
	runtime.KeepAlive(op)
}
