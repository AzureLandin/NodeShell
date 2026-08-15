//go:build windows

package permission

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformPrompt(req Request) Decision {
	title, err := windows.UTF16PtrFromString("NodeShell")
	if err != nil {
		return DecisionDeny
	}
	body, err := windows.UTF16PtrFromString(nativeBody(req))
	if err != nil {
		return DecisionDeny
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	const (
		mbYesNo         = 0x00000004
		mbIconWarning   = 0x00000030
		mbSetForeground = 0x00010000
		mbTopmost       = 0x00040000
		idYes           = 6
	)
	r, _, _ := messageBox.Call(
		0,
		uintptr(unsafe.Pointer(body)),
		uintptr(unsafe.Pointer(title)),
		uintptr(mbYesNo|mbIconWarning|mbSetForeground|mbTopmost),
	)
	if r == idYes {
		return DecisionAllowOnce
	}
	return DecisionDeny
}
