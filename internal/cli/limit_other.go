//go:build !windows

package cli

import "fmt"

func (a *App) limit(args []string) int {
	fmt.Fprintln(a.Stderr, "wslkit limit runs on Windows")
	return ExitUsage
}
