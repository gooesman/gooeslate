package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"unsafe"
)

// Export the same native mark used by the title bar and tray as a shell icon.
func writeApplicationIcon(file string) error {
	icon := createAppIcon()
	if icon == 0 {
		return fmt.Errorf("无法创建应用图标")
	}
	defer call(user, "DestroyIcon", icon)
	var iconInfo appIconInfo
	if call(user, "GetIconInfo", icon, uintptr(unsafe.Pointer(&iconInfo))) == 0 {
		return fmt.Errorf("无法读取应用图标")
	}
	defer call(gdi, "DeleteObject", iconInfo.Color)
	defer call(gdi, "DeleteObject", iconInfo.Mask)
	const size = 64
	dc := call(gdi, "CreateCompatibleDC", 0)
	if dc == 0 {
		return fmt.Errorf("无法创建图标绘图环境")
	}
	defer call(gdi, "DeleteDC", dc)
	pixels := make([]byte, size*size*4)
	info := bitmapinfo{Header: bitmapheader{Size: 40, Width: size, Height: -size, Planes: 1, BitCount: 32}}
	if call(gdi, "GetDIBits", dc, iconInfo.Color, 0, size, uintptr(unsafe.Pointer(&pixels[0])), uintptr(unsafe.Pointer(&info)), 0) != size {
		return fmt.Errorf("无法读取应用图标像素")
	}
	const maskStride = ((size + 31) / 32) * 4
	const payloadSize = 40 + size*size*4 + size*maskStride
	data := make([]byte, 22+payloadSize)
	binary.LittleEndian.PutUint16(data[2:4], 1)
	binary.LittleEndian.PutUint16(data[4:6], 1)
	data[6], data[7] = size, size
	binary.LittleEndian.PutUint16(data[10:12], 1)
	binary.LittleEndian.PutUint16(data[12:14], 32)
	binary.LittleEndian.PutUint32(data[14:18], payloadSize)
	binary.LittleEndian.PutUint32(data[18:22], 22)
	bitmap := data[22:]
	binary.LittleEndian.PutUint32(bitmap[0:4], 40)
	binary.LittleEndian.PutUint32(bitmap[4:8], size)
	binary.LittleEndian.PutUint32(bitmap[8:12], size*2)
	binary.LittleEndian.PutUint16(bitmap[12:14], 1)
	binary.LittleEndian.PutUint16(bitmap[14:16], 32)
	binary.LittleEndian.PutUint32(bitmap[20:24], size*size*4)
	mask := bitmap[40+size*size*4:]
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			input := pixels[(y*size+x)*4:][:4]
			output := bitmap[40+((size-1-y)*size+x)*4:][:4]
			alpha := uint32(input[3])
			output[3] = byte(alpha)
			if alpha == 0 {
				mask[(size-1-y)*maskStride+x/8] |= 0x80 >> (x % 8)
				continue
			}
			for c := 0; c < 3; c++ {
				output[c] = byte(min(uint32(255), (uint32(input[c])*255+alpha/2)/alpha))
			}
		}
	}
	return os.WriteFile(file, data, 0644)
}
