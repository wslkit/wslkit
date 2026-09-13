package disk

import (
	"context"
	"errors"
	"fmt"
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
	removed     []string
	listErr     map[string]error
	renames     []string
	copies      []string
	mkdirs      []string
	renameErr   error
	copyErr     error
	volumeErr   error
	blobs       map[string][]byte
	removedDirs []string
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
	if err, ok := f.listErr[dir]; ok {
		return nil, err
	}
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
	// byArgs is keyed on the whole command line, for tests that need
	// different answers for the same program on different paths.
	byArgs        map[string]CommandResult
	runErr        error
	calls         []string
	unregisterErr error
	importErr     error
	// onImport stands in for what wsl.exe would do: create a new
	// registration, with a fresh GUID, for the imported disk.
	onImport func(name, vhdPath string)
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
	if r, ok := f.byArgs[strings.Join(argv, " ")]; ok {
		return r, nil
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

func (f *fakeFS) Remove(path string) error {
	if _, ok := f.files[path]; !ok {
		return errors.New("no such file: " + path)
	}
	delete(f.files, path)
	delete(f.blobs, path)
	f.dropFromDir(path)
	f.removed = append(f.removed, path)
	return nil
}

// dropFromDir stops a removed file showing up in its parent's listing.
func (f *fakeFS) dropFromDir(path string) {
	parent := DirOf(path)
	var kept []DirEntry
	for _, e := range f.dirs[parent] {
		if e.Path != path {
			kept = append(kept, e)
		}
	}
	if f.dirs != nil {
		f.dirs[parent] = kept
	}
}

// fakeRegistry stands in for the Lxss registration.
type fakeRegistry struct {
	list     []Registration
	warnings []string
	err      error
	values   map[string]string
	writes   []string
	writeErr error
	// failWriteOn makes the write of this value name fail, so the rollback
	// path can be exercised.
	failWriteOn string
	dwords      map[string]uint32
	defaultGUID string
}

func (f *fakeRegistry) key(guid, name string) string { return guid + "\x00" + name }

func (f *fakeRegistry) Distros() ([]Registration, []string, error) {
	return f.list, f.warnings, f.err
}

func (f *fakeRegistry) ReadString(guid, name string) (string, bool, error) {
	v, ok := f.values[f.key(guid, name)]
	return v, ok, nil
}

func (f *fakeRegistry) WriteString(guid, name, value string) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	if name == f.failWriteOn {
		return errors.New("write refused: " + name)
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[f.key(guid, name)] = value
	f.writes = append(f.writes, guid+"/"+name+"="+value)
	return nil
}

func (f *fakeFS) Rename(from, to string) error {
	v, ok := f.files[from]
	if !ok {
		return errors.New("no such file: " + from)
	}
	if f.renameErr != nil {
		return f.renameErr
	}
	delete(f.files, from)
	f.dropFromDir(from)
	f.files[to] = v
	f.addToDir(to)
	if b, ok := f.blobs[from]; ok {
		delete(f.blobs, from)
		if f.blobs == nil {
			f.blobs = map[string][]byte{}
		}
		f.blobs[to] = b
	}
	f.renames = append(f.renames, from+" -> "+to)
	return nil
}

func (f *fakeFS) SameVolume(a, b string) (bool, error) {
	if f.volumeErr != nil {
		return false, f.volumeErr
	}
	return driveOf(a) == driveOf(b), nil
}

func driveOf(p string) string {
	if len(p) >= 2 && p[1] == ':' {
		return strings.ToUpper(p[:2])
	}
	return ""
}

func (f *fakeFS) CopySparse(from, to string, progress func(done, total uint64) bool) error {
	v, ok := f.files[from]
	if !ok {
		return errors.New("no such file: " + from)
	}
	if f.copyErr != nil {
		return f.copyErr
	}
	if progress != nil && !progress(v.onDisk, v.onDisk) {
		return errors.New("cancelled")
	}
	f.files[to] = v
	f.addToDir(to)
	f.copies = append(f.copies, from+" -> "+to)
	return nil
}

func (f *fakeFS) MkdirAll(path string) error {
	f.mkdirs = append(f.mkdirs, path)
	if f.dirs == nil {
		f.dirs = map[string][]DirEntry{}
	}
	if _, ok := f.dirs[path]; !ok {
		f.dirs[path] = nil
	}
	parent := DirOf(path)
	for _, e := range f.dirs[parent] {
		if e.Path == path {
			return nil
		}
	}
	f.dirs[parent] = append(f.dirs[parent], DirEntry{Path: path, IsDir: true})
	return nil
}

func (f *fakeRegistry) ReadDWORD(guid, name string) (uint32, bool, error) {
	v, ok := f.dwords[f.key(guid, name)]
	return v, ok, nil
}

func (f *fakeRegistry) WriteDWORD(guid, name string, value uint32) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	if name == f.failWriteOn {
		return errors.New("write refused: " + name)
	}
	if f.dwords == nil {
		f.dwords = map[string]uint32{}
	}
	f.dwords[f.key(guid, name)] = value
	f.writes = append(f.writes, fmt.Sprintf("%s/%s=%d", guid, name, value))
	return nil
}

func (f *fakeRegistry) DefaultDistribution() (string, bool, error) {
	return f.defaultGUID, f.defaultGUID != "", nil
}

func (f *fakeRegistry) SetDefaultDistribution(guid string) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.defaultGUID = guid
	f.writes = append(f.writes, "DefaultDistribution="+guid)
	return nil
}

func (f *fakeFS) RemoveDir(path string) error {
	delete(f.dirs, path)
	f.removedDirs = append(f.removedDirs, path)
	// A directory is also an entry of its parent, so it has to stop being
	// listed there or the trash still appears to hold it.
	parent := DirOf(path)
	var kept []DirEntry
	for _, e := range f.dirs[parent] {
		if e.Path != path {
			kept = append(kept, e)
		}
	}
	f.dirs[parent] = kept
	return nil
}

func (f *fakeFS) ReadFile(path string) ([]byte, error) {
	b, ok := f.blobs[path]
	if !ok {
		return nil, errors.New("no such file: " + path)
	}
	return b, nil
}

func (f *fakeFS) WriteFile(path string, b []byte) error {
	if f.blobs == nil {
		f.blobs = map[string][]byte{}
	}
	f.blobs[path] = b
	if f.files == nil {
		f.files = map[string]fakeFile{}
	}
	f.files[path] = fakeFile{size: uint64(len(b)), onDisk: uint64(len(b))}
	f.addToDir(path)
	return nil
}

// addToDir makes a written file show up in its parent's listing, the way a real
// filesystem would.
func (f *fakeFS) addToDir(path string) {
	if f.dirs == nil {
		f.dirs = map[string][]DirEntry{}
	}
	parent := DirOf(path)
	for _, e := range f.dirs[parent] {
		if e.Path == path {
			return
		}
	}
	f.dirs[parent] = append(f.dirs[parent], DirEntry{Path: path})
}

func (f *fakeHost) Unregister(ctx context.Context, name string) error {
	f.calls = append(f.calls, "unregister "+name)
	if f.unregisterErr != nil {
		return f.unregisterErr
	}
	return nil
}

func (f *fakeHost) ImportInPlace(ctx context.Context, name, vhdPath string) error {
	f.calls = append(f.calls, "import-in-place "+name+" "+vhdPath)
	if f.importErr != nil {
		return f.importErr
	}
	if f.onImport != nil {
		f.onImport(name, vhdPath)
	}
	return nil
}
