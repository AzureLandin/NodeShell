//go:build windows

package main

import "syscall"

var (
	modkernel32     = syscall.NewLazyDLL("kernel32.dll")
	procFreeConsole = modkernel32.NewProc("FreeConsole")
)

// detachConsole hides the console window that -windowsconsole leaves attached
// to the process. MCP mode keeps the parent's redirected stdio; GUI mode
// drops the unused console so double-clicking the app does not flash a
// terminal.
func detachConsole() {
	_, _, _ = procFreeConsole.Call()
}
