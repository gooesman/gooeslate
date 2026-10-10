package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"reflect"
	"testing"
)

func TestImageClipboardDIBColorRowsAndSubimage(t *testing.T) {
	img := image.NewRGBA(image.Rect(-3, -4, 3, 3))
	crop := img.SubImage(image.Rect(-1, -2, 1, 0)).(*image.RGBA)
	crop.SetRGBA(-1, -2, color.RGBA{255, 0, 0, 255})
	crop.SetRGBA(0, -2, color.RGBA{0, 255, 0, 255})
	crop.SetRGBA(-1, -1, color.RGBA{0, 0, 255, 255})
	crop.SetRGBA(0, -1, color.RGBA{30, 40, 50, 255})
	data, err := imageClipboardDIB(crop)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 56 || binary.LittleEndian.Uint32(data[:4]) != 40 || binary.LittleEndian.Uint32(data[4:8]) != 2 || binary.LittleEndian.Uint32(data[8:12]) != 2 || binary.LittleEndian.Uint16(data[14:16]) != 32 {
		t.Fatalf("invalid bottom-up 32-bit DIB header: %v", data[:40])
	}
	want := []byte{255, 0, 0, 255, 50, 40, 30, 255, 0, 0, 255, 255, 0, 255, 0, 255}
	if !bytes.Equal(data[40:], want) {
		t.Fatalf("BGRA bottom row first: got %v want %v", data[40:], want)
	}
}

func TestImageClipboardDIBTransparentPixelsAndInvalidImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.SetRGBA(0, 0, color.RGBA{64, 32, 0, 128})
	data, err := imageClipboardDIB(img)
	if err != nil || !bytes.Equal(data[40:], []byte{127, 159, 191, 255}) {
		t.Fatalf("transparent premultiplied RGBA must flatten to white: %v %v", data, err)
	}
	for _, invalid := range []*image.RGBA{nil, image.NewRGBA(image.Rectangle{}), {Pix: []byte{0}, Stride: 8, Rect: image.Rect(0, 0, 2, 2)}} {
		if _, err := imageClipboardDIB(invalid); err == nil {
			t.Fatal("invalid capture must be rejected before touching clipboard")
		}
	}
}

type fakeImageClipboard struct {
	calls                  []string
	failAllocate           bool
	openOK, emptyOK, setOK bool
}

func (a *fakeImageClipboard) allocate(data []byte) (uintptr, error) {
	a.calls = append(a.calls, "allocate")
	if a.failAllocate {
		return 0, errors.New("allocation failed")
	}
	return 100, nil
}
func (a *fakeImageClipboard) free(mem uintptr) { a.calls = append(a.calls, "free") }
func (a *fakeImageClipboard) open(owner uintptr) bool {
	a.calls = append(a.calls, "open")
	return a.openOK
}
func (a *fakeImageClipboard) close() { a.calls = append(a.calls, "close") }
func (a *fakeImageClipboard) empty() bool {
	a.calls = append(a.calls, "empty")
	return a.emptyOK
}
func (a *fakeImageClipboard) setDIB(mem uintptr) bool {
	a.calls = append(a.calls, "set")
	return a.setOK
}

func TestClipboardImageFailureOrderAndMemoryOwnership(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for _, tc := range []struct {
		name string
		api  fakeImageClipboard
		want []string
		ok   bool
	}{
		{"allocation", fakeImageClipboard{failAllocate: true}, []string{"allocate"}, false},
		{"occupied", fakeImageClipboard{}, []string{"allocate", "open", "free"}, false},
		{"empty", fakeImageClipboard{openOK: true}, []string{"allocate", "open", "empty", "close", "free"}, false},
		{"write", fakeImageClipboard{openOK: true, emptyOK: true}, []string{"allocate", "open", "empty", "set", "close", "free"}, false},
		{"success", fakeImageClipboard{openOK: true, emptyOK: true, setOK: true}, []string{"allocate", "open", "empty", "set", "close"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := clipboardImageWithAPI(1, img, &tc.api)
			if (err == nil) != tc.ok || !reflect.DeepEqual(tc.api.calls, tc.want) {
				t.Fatalf("clipboard ownership/order: err=%v calls=%v want=%v", err, tc.api.calls, tc.want)
			}
		})
	}
	api := &fakeImageClipboard{}
	if clipboardImageWithAPI(1, nil, api) == nil || len(api.calls) != 0 {
		t.Fatal("missing crop must never open or clear clipboard")
	}
}
