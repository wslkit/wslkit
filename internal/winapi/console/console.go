//go:build windows

// Package console answers whether output goes to a console, and switches that
// console to understand the escape sequences a refreshing display is drawn
// with.
package console

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// OwnConsole gives cmd a console of its own, never shown, instead of wslkit's.
//
// Every wsl.exe and wslc.exe wslkit starts gets this. While it runs, wsl.exe
// holds the console it shares in Linux terminal semantics: measured on WSL
// 2.9.12, output mode 0x3 becomes 0xf, so a line feed stops returning to
// column 0, and input mode 0x1f7 becomes 0x3d8, so Ctrl+C is read as a
// character instead of stopping wslkit. It restores the modes when it exits,
// but two that overlap restore each other's: the second saves the first's
// changed modes and puts those back, and the console is left broken.
// wslkit reads their output through pipes either way, so a console of their
// own costs nothing. See #101.
//
// A child that needs the person at the keyboard, such as an editor, must
// not get this.
func OwnConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

// IsConsole reports whether f is a console rather than a pipe or a file.
func IsConsole(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

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

// Size is how many columns and rows of f's console window are visible, or
// zeros when f is not a console.
func Size(f *os.File) (cols, rows int) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(f.Fd()), &info); err != nil {
		return 0, 0
	}
	return int(info.Window.Right-info.Window.Left) + 1, int(info.Window.Bottom-info.Window.Top) + 1
}
