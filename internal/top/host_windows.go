//go:build windows

package top

import (
	"context"
	"fmt"
	"strconv"

	"github.com/wslkit/wslkit/internal/winapi/wmi"
)

// HostVMs lists the vmmem processes. WMI rather than OpenProcess, because
// vmmem belongs to the VM's virtual account and a normal user cannot open it,
// but WMI still reports its creation time and working set.
//
// Older builds named the utility VM's process vmmemWSL, so both are asked for.
func (r WSLRunner) HostVMs(ctx context.Context) ([]HostVM, error) {
	rows, err := wmi.Query(ctx, `root\cimv2`,
		"SELECT ProcessId, CreationDate, WorkingSetSize FROM Win32_Process WHERE Name='vmmem' OR Name='vmmemWSL'",
		"ProcessId", "CreationDate", "WorkingSetSize")
	if err != nil {
		return nil, fmt.Errorf("top: listing vmmem: %w", err)
	}
	var vms []HostVM
	for _, row := range rows {
		created, err := ParseCIMDateTime(fmt.Sprint(row["CreationDate"]))
		if err != nil {
			continue
		}
		// WorkingSetSize is a uint64, which WMI hands over as a string.
		ws, err := strconv.ParseUint(fmt.Sprint(row["WorkingSetSize"]), 10, 64)
		if err != nil {
			continue
		}
		pid, err := strconv.ParseUint(fmt.Sprint(row["ProcessId"]), 10, 32)
		if err != nil {
			continue
		}
		vms = append(vms, HostVM{PID: uint32(pid), Created: created, WorkingSetBytes: ws})
	}
	return vms, nil
}
