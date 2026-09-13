package disk

import (
	"context"
	"time"
)

// The ports below are the seams between the disk commands and Windows. Command
// logic depends only on these interfaces, so every branch is reachable from a
// test with a fake: a locked file, a full volume, a distribution that will not
// boot. The Windows implementations live in the _windows files of this package.

// Registry reads and writes the Lxss registration for WSL distributions.
//
// An absent value is not an error. VhdFileName is genuinely missing on the
// legacy packaged layout, so the read methods distinguish "not there" from
// "could not be read".
type Registry interface {
	// Distros enumerates every registration. Warnings describe keys that
	// were skipped because they were unusable; one bad key must not stop
	// the command reporting the distributions that are fine.
	Distros() (list []Registration, warnings []string, err error)
	// ReadString reads one string value from a distribution key.
	ReadString(guid, name string) (value string, present bool, err error)
	// WriteString writes one string value to a distribution key.
	WriteString(guid, name, value string) error
	// ReadDWORD reads one numeric value from a distribution key.
	ReadDWORD(guid, name string) (value uint32, present bool, err error)
	// WriteDWORD writes one numeric value to a distribution key.
	WriteDWORD(guid, name string, value uint32) error
	// DefaultDistribution is the GUID of the default distribution, which
	// lives on the Lxss key itself rather than on any distribution.
	DefaultDistribution() (guid string, present bool, err error)
	// SetDefaultDistribution makes one distribution the default.
	SetDefaultDistribution(guid string) error
}

// VolumeInfo describes the volume a path sits on.
type VolumeInfo struct {
	Root       string
	FileSystem string
	TotalBytes uint64
	FreeBytes  uint64
}

// SupportsVHDX reports whether a filesystem can hold a WSL disk. FAT and exFAT
// cannot: they cap a file at 4 GiB and have no sparse support.
func (v VolumeInfo) SupportsVHDX() bool {
	switch v.FileSystem {
	case "NTFS", "ReFS":
		return true
	default:
		return false
	}
}

// DirEntry is one item from a directory listing.
type DirEntry struct {
	Path  string
	IsDir bool
}

// FileSystem is the host filesystem, as much of it as the disk commands need.
type FileSystem interface {
	// Exists reports whether a path is present. A path that cannot be
	// interrogated counts as present, so the caller fails later with the
	// real error rather than reporting a file as missing because a
	// directory above it denied traversal.
	Exists(path string) bool
	// FileSize is the logical length.
	FileSize(path string) (uint64, error)
	// SizeOnDisk is what the volume actually spends on the file, which is
	// the number users notice. It differs from the logical length whenever
	// the file is sparse or compressed.
	SizeOnDisk(path string) (uint64, error)
	// Sparse reports the sparse attribute. It says nothing about how much
	// of the file is real, which is what AllocatedBytes answers.
	Sparse(path string) (bool, error)
	// AllocatedBytes sums the allocated ranges of a sparse file.
	AllocatedBytes(path string) (uint64, error)
	// Locked reports whether the file can be opened exclusively. An error
	// means the question could not be answered, which is not the same as a
	// "no".
	Locked(path string) (bool, error)
	// Volume describes the volume a path sits on.
	Volume(path string) (VolumeInfo, error)
	// List returns the entries of a directory matching a pattern.
	List(dir, pattern string) ([]DirEntry, error)
	// ExpandEnv expands %NAME% references.
	ExpandEnv(s string) (string, error)
	// Remove deletes a file.
	Remove(path string) error
	// Rename moves a file within a volume. It deliberately does not fall
	// back to a cross-volume copy, which would fill in the holes of a
	// sparse file.
	Rename(from, to string) error
	// SameVolume reports whether two paths live on the same volume. It
	// resolves both rather than comparing drive letters, so a mounted
	// folder answers correctly.
	SameVolume(a, b string) (bool, error)
	// CopySparse copies a file, preserving its holes. Progress is reported
	// against the real bytes, not the logical length; returning false
	// cancels.
	CopySparse(from, to string, progress func(done, total uint64) bool) error
	// MkdirAll creates a directory and its parents.
	MkdirAll(path string) error
	// RemoveDir deletes an empty directory.
	RemoveDir(path string) error
	// ReadFile reads a small file whole.
	ReadFile(path string) ([]byte, error)
	// WriteFile writes a small file, replacing what was there.
	WriteFile(path string, b []byte) error
}

// DiskFacts is what the virtual disk provider knows about a .vhdx, as opposed
// to what the host filesystem knows about the file holding it.
type DiskFacts struct {
	VirtualSize  uint64
	PhysicalSize uint64
	BlockSize    uint32
	SectorSize   uint32
	// ParentPath is set only for a differencing disk. WSL never creates
	// one, so a non-empty value here explains a disk that is not
	// self-contained.
	ParentPath string
}

// Disks opens virtual disks.
type Disks interface {
	// Facts reads the provider's view of a disk. A running distribution
	// holds its disk open, so this is the measurement most likely to be
	// unavailable on a machine in normal use; callers treat a failure as a
	// missing measurement rather than as a failed command.
	Facts(path string) (DiskFacts, error)
	// Compact reclaims unused blocks, reporting progress. The disk must not
	// be held by anything else.
	Compact(ctx context.Context, path string, progress func(current, total uint64) bool) error
}

// CommandResult is the outcome of running something inside a distribution.
type CommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Host runs wsl.exe. It is a process wrapper and nothing else: anything the
// registry can answer goes through Registry instead, and nothing here parses
// prose, so a localised Windows does not change what wslkit understands.
type Host interface {
	// Running lists the distributions that are up. An empty list is a
	// normal answer, not an error.
	Running(ctx context.Context) ([]string, error)
	// Terminate stops one distribution.
	Terminate(ctx context.Context, name string) error
	// Shutdown stops the utility VM and every distribution in it.
	Shutdown(ctx context.Context) error
	// Unregister removes a distribution from WSL. It deletes the disk along
	// with the registration, which is why wslkit moves the disk out of the
	// way before calling it.
	Unregister(ctx context.Context, name string) error
	// ImportInPlace registers an existing disk as a distribution, without
	// copying it.
	ImportInPlace(ctx context.Context, name, vhdPath string) error
	// RunAsRoot executes a command inside a distribution as root. argv[0]
	// must be an absolute path: wsl.exe does not search PATH for it.
	RunAsRoot(ctx context.Context, distro string, argv []string, timeout time.Duration) (CommandResult, error)
}

// Clock is injected so the wait for a disk to be released is instant in tests.
// Waiting for the utility VM is seconds of real time and would otherwise be the
// slowest thing in the suite.
type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

// RealClock is the production Clock.
type RealClock struct{}

func (RealClock) Now() time.Time        { return time.Now() }
func (RealClock) Sleep(d time.Duration) { time.Sleep(d) }

// Env bundles the ports so a command takes one argument instead of six.
type Env struct {
	Registry Registry
	FS       FileSystem
	Disks    Disks
	Host     Host
	Clock    Clock
}
