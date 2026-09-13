//go:build windows

package disk

import (
	"context"
	"fmt"

	"github.com/wslkit/wslkit/internal/winapi/virtdisk"
)

// WindowsDisks is the production Disks, backed by virtdisk.dll.
type WindowsDisks struct{}

// Facts reads the provider's view of a disk.
func (WindowsDisks) Facts(path string) (DiskFacts, error) {
	h, err := virtdisk.OpenForInfo(path)
	if err != nil {
		return DiskFacts{}, err
	}
	defer h.Close()

	size, err := h.Size()
	if err != nil {
		return DiskFacts{}, err
	}
	facts := DiskFacts{
		VirtualSize:  size.VirtualSize,
		PhysicalSize: size.PhysicalSize,
		BlockSize:    size.BlockSize,
		SectorSize:   size.SectorSize,
	}
	// A parent is unusual enough that failing to read it should not cost the
	// sizes, which are what the caller actually asked for.
	if parent, err := h.ParentLocation(); err == nil {
		facts.ParentPath = parent
	}
	return facts, nil
}

// Compact reclaims unused blocks.
//
// The context cancels the operation through the progress callback, which is the
// only cancellation path the API offers: there is no way to abandon a
// compaction from outside without leaving the kernel writing into a structure
// the caller has moved on from.
func (WindowsDisks) Compact(ctx context.Context, path string, progress func(current, total uint64) bool) error {
	h, err := virtdisk.OpenForCompact(path)
	if err != nil {
		return err
	}
	defer h.Close()

	if loaded, err := h.IsLoaded(); err == nil && loaded {
		return fmt.Errorf("disk: %s is attached, so it cannot be compacted: detach it first", path)
	}

	return h.Compact(virtdisk.CompactFlagNone, func(p virtdisk.Progress) bool {
		if ctx.Err() != nil {
			return false
		}
		if progress == nil {
			return true
		}
		return progress(p.Current, p.Total)
	})
}
