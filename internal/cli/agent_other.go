//go:build !windows

package cli

import "fmt"

func (a *App) agent(args []string) int {
	fmt.Fprintln(a.Stderr, "wslkit agent commands run on Windows; the guest side is the wslkit-agent binary")
	return ExitUsage
}
