// Package preflight reads a .wsl distribution file and reports what installing
// it would run into, without installing it.
//
// A .wsl file is a tar archive, usually gzip-compressed, holding a whole root
// filesystem plus a few small configuration files that decide how WSL sets it
// up. `wsl --install --from-file` reads those files, acts on them, and reports
// a mistake in one of them as a failed install with an HRESULT.
//
// Everything here works from the tar headers and a handful of small files. No
// archive is extracted, nothing is written, and nothing is installed.
package preflight

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// ErrXZ is returned for an xz-compressed file, which Go's standard library
// cannot read and which this deliberately does not add a dependency for. Saying
// so plainly beats a parse error about a tar header that is not a tar header.
var ErrXZ = errors.New("preflight: this .wsl file is xz-compressed, which this tool cannot read yet")

// ErrZstd is the same for zstd, which newer distributions have started using.
var ErrZstd = errors.New("preflight: this .wsl file is zstd-compressed, which this tool cannot read yet")

// maxFile caps how much of any one interesting file is kept. These are
// configuration files of a few hundred bytes; anything far larger is not the
// file this is looking for, and reading it into memory is how a malformed
// archive turns a check into an out-of-memory crash.
const maxFile = 1 << 20

// maxEntries caps how many tar headers are read, so a crafted archive cannot
// spin this forever.
const maxEntries = 2_000_000

// wanted are the files read in full. Everything else is header only.
var wanted = map[string]bool{
	"etc/wsl-distribution.conf": true,
	"etc/wsl.conf":              true,
	"etc/os-release":            true,
	"usr/lib/os-release":        true,
	"etc/passwd":                true,
	"etc/shadow":                false, // named so nobody adds it: it is not needed and it is secret
}

// Archive is what one .wsl file turned out to contain.
type Archive struct {
	// Files are the small configuration files, by their path without the
	// leading "./" or "/".
	Files map[string]string
	// Entries is how many members the archive has.
	Entries int
	// Bytes is the total uncompressed size of its regular files.
	Bytes int64
	// Compression is "gzip" or "none".
	Compression string
	// SystemdUnits are the enabled units found under the systemd
	// directories, by unit name. Enabled means a symlink under a .wants
	// directory, which is how systemd records it.
	SystemdUnits map[string]bool
	// Xattrs are the extended attributes carried by the archive, by name.
	// They survive an install on WSL 2 and are dropped on WSL 1.
	Xattrs map[string]bool
	// HasSystemd records whether the archive ships systemd at all.
	HasSystemd bool
	// SystemdVersion is read from the systemd library's own file name where
	// one is present, which is where the version is recoverable without
	// running anything.
	SystemdVersion string
}

// Get returns one file's contents.
func (a Archive) Get(name string) (string, bool) {
	s, ok := a.Files[name]
	return s, ok
}

// Inspect reads an archive. The reader is consumed once, in order: a .wsl file
// is a stream, and seeking around one that is compressed means decompressing it
// twice.
func Inspect(ctx context.Context, r io.Reader) (Archive, error) {
	a := Archive{
		Files:        map[string]string{},
		SystemdUnits: map[string]bool{},
		Xattrs:       map[string]bool{},
		Compression:  "none",
	}

	br, magic, err := peek(r, 6)
	if err != nil {
		return a, err
	}
	switch {
	case bytes.HasPrefix(magic, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}):
		return a, ErrXZ
	case bytes.HasPrefix(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return a, ErrZstd
	}

	var tr *tar.Reader
	if bytes.HasPrefix(magic, []byte{0x1f, 0x8b}) {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return a, fmt.Errorf("preflight: reading the gzip stream: %w", err)
		}
		defer func() { _ = zr.Close() }()
		a.Compression = "gzip"
		tr = tar.NewReader(zr)
	} else {
		tr = tar.NewReader(br)
	}

	for {
		if err := ctx.Err(); err != nil {
			return a, err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A truncated or corrupt archive is a finding in itself, and
			// what was read before it is still worth reporting.
			return a, fmt.Errorf("preflight: reading the archive: %w", err)
		}
		a.Entries++
		if a.Entries > maxEntries {
			return a, fmt.Errorf("preflight: the archive has more than %d entries", maxEntries)
		}
		name := strings.TrimPrefix(strings.TrimPrefix(h.Name, "./"), "/")

		for k := range h.PAXRecords {
			// Extended attributes are carried in PAX records named
			// SCHILY.xattr.<name>. WSL 2 keeps them; WSL 1 does not, and
			// a distribution whose security model depends on them will
			// behave differently there.
			if attr, ok := strings.CutPrefix(k, "SCHILY.xattr."); ok {
				a.Xattrs[attr] = true
			}
		}

		switch h.Typeflag {
		case tar.TypeReg:
			a.Bytes += h.Size
			if want, listed := wanted[name]; listed && want && h.Size <= maxFile {
				b, err := io.ReadAll(io.LimitReader(tr, maxFile))
				if err != nil {
					return a, fmt.Errorf("preflight: reading %s: %w", name, err)
				}
				a.Files[name] = string(b)
			}
			noteSystemd(&a, name)
		case tar.TypeSymlink, tar.TypeLink:
			noteSystemd(&a, name)
			// An enabled unit is a symlink from a .wants directory to the
			// unit file. That is how systemd records "enabled", and it is
			// visible from the header alone.
			if dir, unit := path.Split(name); strings.Contains(dir, ".wants/") && strings.HasSuffix(unit, ".service") {
				a.SystemdUnits[unit] = true
			}
		case tar.TypeDir:
			noteSystemd(&a, name)
		}
	}
	return a, nil
}

// noteSystemd records whether the archive ships systemd, and its version where
// the file name gives it away.
func noteSystemd(a *Archive, name string) {
	if !a.HasSystemd && (strings.HasPrefix(name, "usr/lib/systemd/") || strings.HasPrefix(name, "lib/systemd/")) {
		a.HasSystemd = true
	}
	if a.SystemdVersion != "" {
		return
	}
	// The shared library carries the version in its file name, which is the
	// only place it can be read without running anything.
	base := path.Base(name)
	if v, ok := strings.CutPrefix(base, "libsystemd-shared-"); ok {
		a.SystemdVersion = strings.TrimSuffix(v, ".so")
	}
}

// peek returns a reader that still yields the bytes it looked at.
func peek(r io.Reader, n int) (io.Reader, []byte, error) {
	buf := make([]byte, n)
	read, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, nil, fmt.Errorf("preflight: reading the file: %w", err)
	}
	buf = buf[:read]
	return io.MultiReader(bytes.NewReader(buf), r), buf, nil
}
