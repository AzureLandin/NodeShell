//go:build windows

package fonts

import "syscall"

const createNoWindow = 0x08000000

// procAttr hides the console window of child tools (PowerShell would
// otherwise flash a window on the desktop).
func procAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
