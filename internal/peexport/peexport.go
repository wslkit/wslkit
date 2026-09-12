// Package peexport reads the export name table of a PE file without loading
// it, so wsldoctor can tell whether a plugin DLL exports the WSL entry point.
// Pure Go; works on any OS against a file on disk.
package peexport

import (
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// Names returns the exported function names of the PE at path.
func Names(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return NamesFrom(f)
}

// NamesFrom reads exports from an open PE image.
func NamesFrom(r io.ReaderAt) ([]string, error) {
	pf, err := pe.NewFile(r)
	if err != nil {
		return nil, fmt.Errorf("not a PE file: %w", err)
	}
	defer func() { _ = pf.Close() }()
	var dir pe.DataDirectory
	switch oh := pf.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		if len(oh.DataDirectory) == 0 {
			return nil, errors.New("no data directories")
		}
		dir = oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_EXPORT]
	case *pe.OptionalHeader32:
		if len(oh.DataDirectory) == 0 {
			return nil, errors.New("no data directories")
		}
		dir = oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_EXPORT]
	default:
		return nil, errors.New("unknown optional header")
	}
	if dir.VirtualAddress == 0 || dir.Size == 0 {
		return []string{}, nil // no export table at all
	}
	rva2off := func(rva uint32) (int64, error) {
		for _, s := range pf.Sections {
			if rva >= s.VirtualAddress && rva < s.VirtualAddress+s.Size {
				return int64(rva-s.VirtualAddress) + int64(s.Offset), nil
			}
		}
		return 0, fmt.Errorf("rva 0x%x outside all sections", rva)
	}
	off, err := rva2off(dir.VirtualAddress)
	if err != nil {
		return nil, err
	}
	// IMAGE_EXPORT_DIRECTORY is 40 bytes.
	var ed [40]byte
	if _, err := r.ReadAt(ed[:], off); err != nil {
		return nil, fmt.Errorf("export directory: %w", err)
	}
	numNames := binary.LittleEndian.Uint32(ed[24:28])
	namesRVA := binary.LittleEndian.Uint32(ed[32:36])
	if numNames > 65536 {
		return nil, fmt.Errorf("implausible export count %d", numNames)
	}
	namesOff, err := rva2off(namesRVA)
	if err != nil {
		return nil, err
	}
	ptrs := make([]byte, 4*numNames)
	if _, err := r.ReadAt(ptrs, namesOff); err != nil {
		return nil, fmt.Errorf("export name pointers: %w", err)
	}
	out := make([]string, 0, numNames)
	for i := uint32(0); i < numNames; i++ {
		nameRVA := binary.LittleEndian.Uint32(ptrs[i*4 : i*4+4])
		noff, err := rva2off(nameRVA)
		if err != nil {
			continue
		}
		name, err := readCString(r, noff, 512)
		if err != nil {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// Has reports whether the PE at path exports name.
func Has(path, name string) (bool, error) {
	names, err := Names(path)
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if n == name {
			return true, nil
		}
	}
	return false, nil
}

func readCString(r io.ReaderAt, off int64, max int) (string, error) {
	buf := make([]byte, 0, 64)
	var b [64]byte
	for len(buf) < max {
		n, err := r.ReadAt(b[:], off+int64(len(buf)))
		if n == 0 && err != nil {
			return "", err
		}
		for i := 0; i < n; i++ {
			if b[i] == 0 {
				return string(buf), nil
			}
			buf = append(buf, b[i])
		}
		if err != nil {
			break
		}
	}
	return string(buf), nil
}
