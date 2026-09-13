package disk

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Info is everything wslkit knows about one distribution disk.
//
// Every measurement is optional and absent is not zero: a disk held open by a
// running distribution genuinely has no readable virtual size, and reporting
// that as 0 would be a lie rather than a gap. Optional fields are pointers so
// the JSON encoder can omit them.
type Info struct {
	// From the host filesystem.
	FileSize   *uint64
	SizeOnDisk *uint64
	Allocated  *uint64
	Sparse     *bool

	// From the virtual disk provider.
	VirtualSize *uint64
	BlockSize   *uint32
	SectorSize  *uint32
	ParentPath  string

	// From inside the guest.
	GuestUsed *uint64
	GuestFree *uint64

	// Notes explain, in the user's terms, why something is missing.
	Notes []string
}

// Reclaimable estimates how much compaction could give back: what the file
// costs on the volume, less what the guest says it is using.
//
// It needs both numbers, and it floors at zero, because on a compressed volume
// the guest's figure can legitimately exceed the file's cost on disk.
func (i Info) Reclaimable() *uint64 {
	if i.SizeOnDisk == nil || i.GuestUsed == nil {
		return nil
	}
	if *i.GuestUsed >= *i.SizeOnDisk {
		zero := uint64(0)
		return &zero
	}
	v := *i.SizeOnDisk - *i.GuestUsed
	return &v
}

// note appends an explanation, ignoring duplicates so five failed measurements
// of the same missing file do not produce five identical lines.
func (i *Info) note(s string) {
	for _, existing := range i.Notes {
		if existing == s {
			return
		}
	}
	i.Notes = append(i.Notes, s)
}

// MeasureOptions controls how much work Measure does.
type MeasureOptions struct {
	// Probe permits starting a stopped distribution to read guest usage.
	// Off by default: starting a distribution to measure it changes the
	// thing being measured, and booting every distribution because someone
	// ran a listing is not a reasonable thing for a tool to do.
	Probe bool
	// Running is the set of distributions already up, queried once for a
	// whole listing rather than per row.
	Running map[string]bool
	// Timeout bounds each guest command.
	Timeout time.Duration
}

// guestTimeout is the default bound on a df inside the guest.
const guestTimeout = 30 * time.Second

// Measure collects everything known about one distribution disk.
//
// It never returns an error: a measurement that cannot be taken is a missing
// field and a note, because a listing that fails wholesale because one disk is
// busy is less useful than one that reports what it could read.
func Measure(ctx context.Context, e Env, r Registration, opts MeasureOptions) Info {
	var info Info
	if r.Version != 2 {
		// A WSL 1 distribution keeps its files directly on NTFS. There
		// is no disk to measure and that is not a fault.
		return info
	}
	path := r.VhdPath()
	if path == "" {
		info.note("this distribution has no BasePath in the registry, so wslkit cannot tell where its disk is")
		return info
	}

	// Ask once whether the file is there. Five measurements reading the
	// same missing file used to fail five times and say so five times.
	if !e.FS.Exists(path) {
		info.note(fmt.Sprintf("the disk recorded in the registry is missing: %s. Run wslkit disk orphans to find disks nothing claims, or wslkit disk relink to repoint this one.", path))
	} else {
		measureFile(e, path, &info)
		measureProvider(e, path, &info)
	}

	measureGuest(ctx, e, r, &info, opts)
	return info
}

func measureFile(e Env, path string, info *Info) {
	if v, err := e.FS.FileSize(path); err == nil {
		info.FileSize = &v
	} else {
		info.note("the disk file's length could not be read: " + err.Error())
	}
	if v, err := e.FS.SizeOnDisk(path); err == nil {
		info.SizeOnDisk = &v
	} else {
		info.note("the space the disk file occupies could not be read: " + err.Error())
	}
	if v, err := e.FS.Sparse(path); err == nil {
		info.Sparse = &v
	}
	if v, err := e.FS.AllocatedBytes(path); err == nil {
		info.Allocated = &v
	}
}

func measureProvider(e Env, path string, info *Info) {
	facts, err := e.Disks.Facts(path)
	if err != nil {
		// A running distribution holds its disk open, so this is the
		// measurement most likely to be missing on a working machine.
		info.note("the virtual disk's own metadata could not be read, which is normal while the distribution is running: " + err.Error())
		return
	}
	info.VirtualSize = &facts.VirtualSize
	if facts.BlockSize != 0 {
		info.BlockSize = &facts.BlockSize
	}
	if facts.SectorSize != 0 {
		info.SectorSize = &facts.SectorSize
	}
	info.ParentPath = facts.ParentPath
}

func measureGuest(ctx context.Context, e Env, r Registration, info *Info, opts MeasureOptions) {
	if r.Version != 2 {
		return
	}
	if !opts.Running[r.Name] && !opts.Probe {
		return
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = guestTimeout
	}
	// An absolute path: wsl.exe does not search PATH for the executable it
	// is asked to exec.
	res, err := e.Host.RunAsRoot(ctx, r.Name, []string{"/bin/df", "-B1", "/"}, timeout)
	if err != nil {
		info.note("the guest filesystem usage could not be read: " + err.Error())
		return
	}
	if res.ExitCode != 0 {
		info.note("df exited " + strconv.Itoa(res.ExitCode) + " inside the distribution, so its usage is unknown")
		return
	}
	used, free, err := ParseDF(res.Stdout)
	if err != nil {
		info.note("the output of df could not be understood: " + err.Error())
		return
	}
	info.GuestUsed = &used
	info.GuestFree = &free
}

// ParseDF reads the used and available byte counts out of `df -B1 /`.
//
// The columns are counted from the right, never by header text, for two
// reasons: the headers are localised, and a long device name wraps onto its own
// line leaving the numbers on the next one. Taking the last non-empty line and
// indexing backwards survives both.
func ParseDF(out string) (used, avail uint64, err error) {
	line := lastNonEmptyLine(out)
	if line == "" {
		return 0, 0, fmt.Errorf("df printed nothing")
	}
	fields := strings.Fields(line)
	// Filesystem, 1B-blocks, Used, Available, Use%, Mounted on.
	const trailing = 5
	if len(fields) < trailing {
		return 0, 0, fmt.Errorf("df printed %d columns, want at least %d: %q", len(fields), trailing, line)
	}
	used, err = strconv.ParseUint(fields[len(fields)-4], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("the used column of df is not a number: %q", fields[len(fields)-4])
	}
	avail, err = strconv.ParseUint(fields[len(fields)-3], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("the available column of df is not a number: %q", fields[len(fields)-3])
	}
	return used, avail, nil
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// FlagBits are the documented bits of the Lxss Flags value.
const (
	FlagInterop      = 0x1
	FlagAppendNTPath = 0x2
	FlagDriveMount   = 0x4
)

// DecodeFlags renders the Flags value as names. Undocumented bits are reported
// rather than dropped, because every distribution observed in the wild has at
// least one set and hiding them would make the output look complete when it is
// not.
func DecodeFlags(flags int) string {
	if flags == 0 {
		return "none"
	}
	var parts []string
	rest := flags
	for _, known := range []struct {
		bit  int
		name string
	}{
		{FlagInterop, "interop"},
		{FlagAppendNTPath, "append-nt-path"},
		{FlagDriveMount, "drive-mounting"},
	} {
		if flags&known.bit != 0 {
			parts = append(parts, known.name)
			rest &^= known.bit
		}
	}
	if rest != 0 {
		parts = append(parts, fmt.Sprintf("undocumented(0x%x)", rest))
	}
	return strings.Join(parts, ", ")
}
