//go:build !windows

package fonts

import "syscall"

// procAttr is nil on non-Windows platforms (no console window concept).
func procAttr() *syscall.SysProcAttr { return nil }
