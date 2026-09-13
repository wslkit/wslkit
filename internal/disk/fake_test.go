package disk

import (
	"context"
	"errors"
	"strings"
	"time"
)

// The fakes below stand in for Windows. They are deliberately dumb: a map of
// what each call should answer, so a test states the machine it is describing
// instead of arranging one.

type fakeFile struct {
	size      uint64
	onDisk    uint64
	allocated uint64
	sparse    bool
	locked    bool
	sizeErr   error
}

type fakeFS struct {
	files map[string]fakeFile
	// unlockAfter makes a file report itself locked for this many calls and
	// free afterwards, so the wait loop can be exercised without waiting.
	unlockAfter map[string]int
	lockCalls   map[string]int
	volumes     map[string]VolumeInfo
	dirs        map[string][]DirEntry
	env         map[string]string
}

func (f *fakeFS) Exists(path string) bool {
	_, ok := f.files[path]
	return ok
}

func (f *fakeFS) get(path string) (fakeFile, error) {
	v, ok := f.files[path]
	if !ok {
		return fakeFile{}, errors.New("no such file: " + path)
	}
	if v.sizeErr != nil {
		return fakeFile{}, v.sizeErr
	}
	return v, nil
}

func (f *fakeFS) FileSize(path string) (uint64, error) {
	v, err := f.get(path)
	return v.size, err
}

func (f *fakeFS) SizeOnDisk(path string) (uint64, error) {
	v, err := f.get(path)
	return v.onDisk, err
}

func (f *fakeFS) Sparse(path string) (bool, error) {
	v, err := f.get(path)
	return v.sparse, err
}

func (f *fakeFS) AllocatedBytes(path string) (uint64, error) {
	v, err := f.get(path)
	return v.allocated, err
}

func (f *fakeFS) Locked(path string) (bool, error) {
	v, err := f.get(path)
	if err != nil {
		return false, err
	}
	if n, ok := f.unlockAfter[path]; ok {
		if f.lockCalls == nil {
			f.lockCalls = map[string]int{}
		}
		f.lockCalls[path]++
		return f.lockCalls[path] <= n, nil
	}
	return v.locked, nil
}

func (f *fakeFS) Volume(path string) (VolumeInfo, error) {
	// Keyed on the drive letter, extracted without filepath so the fake
	// behaves the same on the Linux job as it does on Windows.
	drive := ""
	if len(path) >= 2 && path[1] == ':' {
		drive = strings.ToUpper(path[:2])
	}
	if v, ok := f.volumes[drive]; ok {
		return v, nil
	}
	return VolumeInfo{}, errors.New("no volume for " + path)
}

func (f *fakeFS) List(dir, pattern string) ([]DirEntry, error) {
	return f.dirs[dir], nil
}

func (f *fakeFS) ExpandEnv(s string) (string, error) {
	out := s
	for k, v := range f.env {
		out = strings.ReplaceAll(out, "%"+k+"%", v)
	}
	return out, nil
}

type fakeDisks struct {
	facts map[string]DiskFacts
	err   map[string]error
}

func (f *fakeDisks) Facts(path string) (DiskFacts, error) {
	if e, ok := f.err[path]; ok {
		return DiskFacts{}, e
	}
	v, ok := f.facts[path]
	if !ok {
		return DiskFacts{}, errors.New("no facts for " + path)
	}
	return v, nil
}

func (f *fakeDisks) Compact(ctx context.Context, path string, progress func(uint64, uint64) bool) error {
	return errors.New("not implemented in the fake")
}

type fakeHost struct {
	running    []string
	runningErr error
	results    map[string]CommandResult // distro+" "+argv[0] -> result
	runErr     error
	calls      []string
}

func (f *fakeHost) Running(ctx context.Context) ([]string, error) {
	return f.running, f.runningErr
}

func (f *fakeHost) Terminate(ctx context.Context, name string) error {
	f.calls = append(f.calls, "terminate "+name)
	return nil
}

func (f *fakeHost) Shutdown(ctx context.Context) error {
	f.calls = append(f.calls, "shutdown")
	return nil
}

func (f *fakeHost) RunAsRoot(ctx context.Context, distro string, argv []string, timeout time.Duration) (CommandResult, error) {
	f.calls = append(f.calls, "run "+distro+" "+strings.Join(argv, " "))
	if f.runErr != nil {
		return CommandResult{}, f.runErr
	}
	if r, ok := f.results[distro+" "+argv[0]]; ok {
		return r, nil
	}
	return CommandResult{}, nil
}

type fakeClock struct {
	now   time.Time
	slept []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(d time.Duration) {
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
}

// u64 is a helper for building the pointer-valued optional fields.
func u64(v uint64) *uint64 { return &v }
