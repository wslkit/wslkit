//go:build !windows

package cli

import "fmt"

func (a *App) guard(args []string) int {
	fmt.Fprintln(a.Stderr, "wslkit guard runs on Windows")
	return ExitUsage
}
