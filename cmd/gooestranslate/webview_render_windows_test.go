package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestWebViewRenderRejectsWhiteAndSingleColor(t *testing.T) {
	for _, c := range []color.RGBA{{255, 255, 255, 255}, {17, 18, 20, 255}} {
		img := image.NewRGBA(image.Rect(0, 0, 480, 320))
		draw.Draw(img, img.Bounds(), &image.Uniform{c}, image.Point{}, draw.Src)
		file := writeRenderTestPNG(t, img)
		if _, err := inspectWebViewPNG(file); err == nil {
			t.Fatal("solid browser pane must not pass rendered UI regression check")
		}
	}
}

func TestWebViewRenderCallbackOwnsNativeLifetimeAndDefersCompletion(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	img := image.NewRGBA(image.Rect(0, 0, 480, 320))
	for y := 0; y < 320; y++ {
		for x := 0; x < 480; x++ {
			img.SetRGBA(x, y, color.RGBA{byte(x % 255), byte(y % 255), 120, 255})
		}
	}
	file := writeRenderTestPNG(t, img)
	thread := call(kernel, "GetCurrentThreadId")
	called := false
	var completionError error
	op := &webRenderOperation{File: file, Completed: func(err error) {
		called = true
		completionError = err
		if thread != call(kernel, "GetCurrentThreadId") {
			t.Error("completion must run on initiating UI thread")
		}
	}}
	op.Callback.VTable, op.Callback.Op = &webRenderVTable, op
	op.Callback.Refs.Store(1)
	op.Pin.Pin(&op.Callback)
	webRenderMutex.Lock()
	webRenderOperations[&op.Callback] = op
	webRenderMutex.Unlock()
	this := uintptr(unsafe.Pointer(&op.Callback))
	var queried uintptr
	hr, _, _ := syscall.SyscallN(webRenderVTable[0], this, uintptr(unsafe.Pointer(&webRenderCaptureIID)), uintptr(unsafe.Pointer(&queried)))
	if hr != 0 || queried != this || op.Callback.Refs.Load() != 2 {
		t.Fatal("CapturePreview QueryInterface must retain the callback")
	}
	// Drop the caller reference. The simulated native operation still owns one.
	webRenderRelease(&op.Callback)
	runtime.GC()
	webRenderMutex.Lock()
	retained := webRenderOperations[&op.Callback] == op
	webRenderMutex.Unlock()
	if !retained {
		t.Fatal("asynchronous native reference must root callback after caller returns")
	}
	syscall.SyscallN(webRenderVTable[3], this, 0)
	if called || !op.Finished.Load() {
		t.Fatal("completion must leave COM callback before reporting rendered pixels")
	}
	webRenderRelease(&op.Callback)
	webRenderMutex.Lock()
	retained = webRenderOperations[&op.Callback] != nil
	webRenderMutex.Unlock()
	if retained {
		t.Fatal("native Release must remove callback root and unpin its memory")
	}
	deadline := time.Now().Add(time.Second)
	for !called && time.Now().Before(deadline) {
		var msg message
		if call(user, "PeekMessageW", uintptr(unsafe.Pointer(&msg)), 0, 0, 0, 1) != 0 {
			call(user, "TranslateMessage", uintptr(unsafe.Pointer(&msg)))
			call(user, "DispatchMessageW", uintptr(unsafe.Pointer(&msg)))
		} else {
			time.Sleep(time.Millisecond)
		}
	}
	if !called || completionError != nil {
		t.Fatalf("deferred native callback did not validate PNG: called=%t err=%v", called, completionError)
	}
	runtime.KeepAlive(op)
}

func TestWebViewRenderAcceptsVariedPixelsAndRejectsBadDimensions(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 480, 320))
	for y := 0; y < 320; y++ {
		for x := 0; x < 480; x++ {
			img.SetRGBA(x, y, color.RGBA{byte(20 + x%200), byte(20 + y%200), 120, 255})
		}
	}
	stats, err := inspectWebViewPNG(writeRenderTestPNG(t, img))
	if err != nil || stats.Width != 480 || stats.Height != 320 || stats.DistinctColors < 6 {
		t.Fatalf("rendered color validation: %+v, %v", stats, err)
	}
	if _, err = inspectWebViewPNG(writeRenderTestPNG(t, image.NewRGBA(image.Rect(0, 0, 12, 12)))); err == nil {
		t.Fatal("tiny/zero bounds must not pass rendered UI check")
	}
}

func writeRenderTestPNG(t *testing.T, img image.Image) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "capture.png")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}
