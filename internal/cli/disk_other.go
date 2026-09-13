//go:build !windows

package cli

import "fmt"

func (a *App) disk(args []string) int {
	fmt.Fprintln(a.Stderr, "wslkit disk runs on Windows")
	return ExitUsage
}
