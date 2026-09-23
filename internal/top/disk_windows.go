//go:build windows

package top

import (
	"context"
	"os"
	"strings"

	"github.com/wslkit/wslkit/internal/disk"
)

// DiskSizes is the size on Windows of each distribution's .vhdx file, by
// distribution name, from the same registry records wslkit disk reads. A
// distribution whose file cannot be read is left out, not given a zero.
func (r WSLRunner) DiskSizes(ctx context.Context) (map[string]uint64, error) {
	regs, _, err := disk.NewRegistry().Distros()
	if err != nil {
		return nil, err
	}
	out := map[string]uint64{}
	for _, reg := range regs {
		p := reg.VhdPath()
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil {
			out[strings.ToLower(reg.Name)] = uint64(st.Size())
		}
	}
	return out, nil
}
