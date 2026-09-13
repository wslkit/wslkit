//go:build !windows

package cli

import "fmt"

func (a *App) top(args []string) int {
	fmt.Fprintln(a.Stderr, "wslkit top runs on Windows")
	return ExitUsage
}
