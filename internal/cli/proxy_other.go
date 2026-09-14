//go:build !windows

package cli

import "fmt"

func (a *App) proxy(args []string) int {
	fmt.Fprintln(a.Stderr, "wslkit proxy runs on Windows")
	return ExitUsage
}
