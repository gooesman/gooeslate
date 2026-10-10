package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

const eudicAuthorizationURL = "https://my.eudic.net/OpenAPI/Authorization"

// Only this fixed official page can be opened by the vocabulary settings action.
func openEudicAuthorization(owner uintptr) error {
	result := call(syscall.NewLazyDLL("shell32.dll"), "ShellExecuteW", owner, uintptr(unsafe.Pointer(str("open"))), uintptr(unsafe.Pointer(str(eudicAuthorizationURL))), 0, 0, 1)
	if result <= 32 {
		return fmt.Errorf("无法打开默认浏览器，请访问欧路 OpenAPI 授权页面")
	}
	return nil
}
