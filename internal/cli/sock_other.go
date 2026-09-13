//go:build !windows

package cli

import "fmt"

func (a *App) sock(args []string) int {
	fmt.Fprintln(a.Stderr, "wslkit sock runs on Windows")
	return ExitUsage
}
