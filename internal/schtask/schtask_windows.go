//go:build windows

// Package schtask registers and removes Windows scheduled tasks from XML, which
// is how wslkit runs something when nobody is watching: guard after a resume,
// disk automount at logon.
package schtask

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf16"
)

// Register writes the XML to a temporary file and hands it to schtasks,
// replacing any task of the same name.
//
// The XML form rather than the flag form, because the flag form cannot express
// several triggers, a trigger delay or a named logon user.
func Register(ctx context.Context, name, xmlText string) error {
	f, err := os.CreateTemp("", "wslkit-task-*.xml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	// UTF-16 with a byte-order mark: schtasks rejects anything else, with a
	// message that does not say so.
	if _, err := f.Write(UTF16BOM(xmlText)); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	out, err := Run(ctx, "/create", "/tn", name, "/xml", f.Name(), "/f")
	if err != nil {
		return fmt.Errorf("registering the task %q: %w: %s", name, err, strings.TrimSpace(out))
	}
	return nil
}

// Delete removes a task. A task that is not there is reported as not removed,
// not as an error: the point of the call is to end up with none.
func Delete(ctx context.Context, name string) (bool, error) {
	out, err := Run(ctx, "/delete", "/tn", name, "/f")
	switch {
	case err == nil:
		return true, nil
	case strings.Contains(out, "cannot find") || strings.Contains(out, "does not exist"):
		return false, nil
	default:
		return false, fmt.Errorf("removing the task %q: %w: %s", name, err, strings.TrimSpace(out))
	}
}

// Exists reports whether a task is registered.
func Exists(ctx context.Context, name string) bool {
	_, err := Run(ctx, "/query", "/tn", name)
	return err == nil
}

// Run runs schtasks.exe and returns what it printed.
func Run(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "schtasks.exe", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return decode(out.Bytes()), err
}

// decode reads schtasks output, which is the console code page normally and
// UTF-16 when redirected on some builds.
func decode(b []byte) string {
	if !bytes.ContainsRune(b, 0) {
		return string(b)
	}
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00")
}

// XMLEscape escapes text for an element body.
func XMLEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// UTF16BOM encodes the XML the way schtasks insists on reading it.
func UTF16BOM(s string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xff, 0xfe})
	for _, r := range s {
		if r > 0xffff {
			r = '?'
		}
		b.WriteByte(byte(r))
		b.WriteByte(byte(r >> 8))
	}
	return b.Bytes()
}
