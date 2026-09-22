//go:build windows

// Package console answers whether output goes to a console, and switches that
// console to understand the escape sequences a refreshing display is drawn
// with.
package console

import (
	"os"

	"golang.org/x/sys/windows"
)

// EnableVT turns on virtual-terminal processing for f and reports whether f is
// a console that now has it. A pipe or a file is not, and gets plain output.
func EnableVT(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
