//go:build windows

// Spike: are installed hotfixes (Win32_QuickFixEngineering) readable unelevated,
// and how long does the query take? Needed for the KB-regression check in NET004.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/wslkit/wslkit/internal/winapi/wmi"
)

func main() {
	t := time.Now()
	rows, err := wmi.Query(context.Background(), `root\cimv2`, "SELECT HotFixID, InstalledOn FROM Win32_QuickFixEngineering", "HotFixID", "InstalledOn")
	fmt.Printf("all: took %v err=%v rows=%d\n", time.Since(t).Round(time.Millisecond), err, len(rows))
	for i, r := range rows {
		if i < 6 {
			fmt.Printf("  %v %v\n", r["HotFixID"], r["InstalledOn"])
		}
	}
	t = time.Now()
	rows, err = wmi.Query(context.Background(), `root\cimv2`, "SELECT HotFixID FROM Win32_QuickFixEngineering WHERE HotFixID='KB5068861'", "HotFixID")
	fmt.Printf("filtered: took %v err=%v rows=%d\n", time.Since(t).Round(time.Millisecond), err, len(rows))
	t = time.Now()
	rows, err = wmi.Query(context.Background(), `ROOT\standardcimv2`, "SELECT * FROM MSFT_NetFirewallHyperVProfile", "Name", "Enabled")
	fmt.Printf("HyperVProfile (firewall v2): took %v err=%v rows=%d\n", time.Since(t).Round(time.Millisecond), err, len(rows))
	t = time.Now()
	rows, err = wmi.Query(context.Background(), `ROOT\standardcimv2`, "SELECT * FROM MSFT_NetFirewallHyperVVMCreator", "VMCreatorId", "FriendlyName")
	fmt.Printf("HyperVVMCreator (firewall v1): took %v err=%v rows=%d\n", time.Since(t).Round(time.Millisecond), err, len(rows))
}
