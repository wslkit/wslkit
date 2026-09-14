package preflight

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/env"
	"github.com/wslkit/wslkit/internal/probe"
)

// entry is one member of a test archive.
type entry struct {
	name     string
	body     string
	typeflag byte
	link     string
	xattrs   map[string]string
}

// build writes a tar, gzipped when asked, the way a .wsl file is built.
func build(t *testing.T, gz bool, entries ...entry) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body))}
		h.Typeflag = e.typeflag
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		h.Linkname = e.link
		if len(e.xattrs) > 0 {
			h.Format = tar.FormatPAX
			h.PAXRecords = map[string]string{}
			for k, v := range e.xattrs {
				h.PAXRecords["SCHILY.xattr."+k] = v
			}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if !gz {
		return raw.Bytes()
	}
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func inspect(t *testing.T, body []byte) Archive {
	t.Helper()
	a, err := Inspect(context.Background(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

const passwd = "root:x:0:0:root:/root:/bin/bash\nana:x:1000:1000::/home/ana:/bin/bash\n"

func TestInspectReadsTheSmallFiles(t *testing.T) {
	for _, gz := range []bool{false, true} {
		a := inspect(t, build(t, gz,
			entry{name: "./etc/passwd", body: passwd},
			entry{name: "./etc/wsl.conf", body: "[boot]\nsystemd=true\n"},
			entry{name: "./etc/wsl-distribution.conf", body: "[oobe]\ndefaultUid=1000\n"},
			entry{name: "./usr/bin/bash", body: "ELF..."},
		))
		if got, ok := a.Get("etc/passwd"); !ok || !strings.Contains(got, "ana") {
			t.Fatalf("gz=%v: passwd = %q", gz, got)
		}
		if a.Entries != 4 {
			t.Errorf("gz=%v: entries = %d", gz, a.Entries)
		}
		want := "none"
		if gz {
			want = "gzip"
		}
		if a.Compression != want {
			t.Errorf("compression = %q, want %q", a.Compression, want)
		}
	}
}

// xz and zstd are named rather than failing with a confusing tar error, because
// "this is not a tar header" is not what the user needs to hear.
func TestInspectNamesUnreadableCompression(t *testing.T) {
	if _, err := Inspect(context.Background(), bytes.NewReader([]byte{0xfd, '7', 'z', 'X', 'Z', 0x00, 0, 0})); !errors.Is(err, ErrXZ) {
		t.Errorf("xz: err = %v", err)
	}
	if _, err := Inspect(context.Background(), bytes.NewReader([]byte{0x28, 0xb5, 0x2f, 0xfd, 0, 0})); !errors.Is(err, ErrZstd) {
		t.Errorf("zstd: err = %v", err)
	}
}

func TestInspectFindsEnabledUnitsAndXattrs(t *testing.T) {
	a := inspect(t, build(t, true,
		entry{name: "usr/lib/systemd/system/systemd-resolved.service", body: "[Unit]"},
		entry{name: "etc/systemd/system/multi-user.target.wants/systemd-resolved.service",
			typeflag: tar.TypeSymlink, link: "/usr/lib/systemd/system/systemd-resolved.service"},
		entry{name: "usr/lib/systemd/libsystemd-shared-258.so", body: "ELF"},
		entry{name: "etc/shadow", body: "root:!:1::::::", xattrs: map[string]string{"security.selinux": "system_u:object_r:shadow_t:s0"}},
	))
	if !a.HasSystemd {
		t.Error("the archive ships systemd")
	}
	if a.SystemdVersion != "258" {
		t.Errorf("systemd version = %q", a.SystemdVersion)
	}
	if !a.SystemdUnits["systemd-resolved.service"] {
		t.Errorf("units = %v", a.SystemdUnits)
	}
	if !a.Xattrs["security.selinux"] {
		t.Errorf("xattrs = %v", a.Xattrs)
	}
}

// /etc/shadow is read by nothing here, and an archive's password hashes have no
// business in a report.
func TestInspectDoesNotKeepShadow(t *testing.T) {
	a := inspect(t, build(t, true, entry{name: "etc/shadow", body: "root:$6$verysecret"}))
	if _, ok := a.Get("etc/shadow"); ok {
		t.Error("the shadow file must not be read into the report")
	}
}

// ---- checks ----

func machine(version string) *env.Env {
	e := env.New("t")
	if version != "" {
		e.Runtime.Version = env.Ok(version, "t")
	}
	return e
}

func find(results []probe.Result, id string) probe.Result {
	for _, r := range results {
		if r.ID == id {
			return r
		}
	}
	return probe.Result{}
}

func TestCheckCleanArchive(t *testing.T) {
	a := inspect(t, build(t, true,
		entry{name: "etc/passwd", body: passwd},
		entry{name: "etc/wsl.conf", body: "[boot]\nsystemd=true\n"},
		entry{name: "etc/wsl-distribution.conf", body: "[oobe]\ndefaultUid=1000\ndefaultName=Ubuntu\n[shortcut]\nenabled=true\nicon=/usr/share/icons/ubuntu.ico\n"},
	))
	for _, r := range Check(a, machine("2.7.13.0")) {
		if r.Status != probe.OK {
			t.Errorf("%s: %s %q", r.ID, r.Status, r.Summary)
		}
	}
}

// The expensive mistake: the first shell opens as a uid that does not exist and
// nothing in the archive creates it.
func TestCheckDefaultUidThatDoesNotExist(t *testing.T) {
	a := inspect(t, build(t, true,
		entry{name: "etc/passwd", body: passwd},
		entry{name: "etc/wsl-distribution.conf", body: "[oobe]\ndefaultUid=1001\n"},
	))
	r := find(Check(a, machine("2.7.13.0")), "PRE002")
	if r.Status != probe.Fail {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
}

// With a first-run command, the user does not exist yet on purpose.
func TestCheckDefaultUidCreatedByOobe(t *testing.T) {
	a := inspect(t, build(t, true,
		entry{name: "etc/passwd", body: passwd},
		entry{name: "etc/wsl-distribution.conf", body: "[oobe]\ncommand=/usr/lib/wsl/oobe.sh\ndefaultUid=1001\n"},
	))
	if r := find(Check(a, machine("2.7.13.0")), "PRE002"); r.Status != probe.OK {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
}

func TestCheckUidZeroMustBeRoot(t *testing.T) {
	a := inspect(t, build(t, true, entry{name: "etc/passwd", body: "admin:x:0:0::/root:/bin/sh\n"}))
	if r := find(Check(a, machine("2.7.13.0")), "PRE002"); r.Status != probe.Fail {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
}

func TestCheckUnknownDistConfKey(t *testing.T) {
	a := inspect(t, build(t, true,
		entry{name: "etc/passwd", body: passwd},
		entry{name: "etc/wsl-distribution.conf", body: "[oobe]\ndefaultuser=ana\n"},
	))
	r := find(Check(a, machine("2.7.13.0")), "PRE001")
	if r.Status != probe.Warn || !strings.Contains(r.Summary, "oobe.defaultuser") {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
}

func TestCheckDiscouragedUnit(t *testing.T) {
	a := inspect(t, build(t, true,
		entry{name: "usr/lib/systemd/system/systemd-resolved.service", body: "[Unit]"},
		entry{name: "etc/systemd/system/multi-user.target.wants/systemd-resolved.service",
			typeflag: tar.TypeSymlink, link: "../systemd-resolved.service"},
	))
	r := find(Check(a, machine("2.7.13.0")), "PRE004")
	if r.Status != probe.Warn || !strings.Contains(r.Summary, "systemd-resolved") {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
}

// The check no distribution validator can make: this machine's runtime is too
// old to start what is in the file.
func TestCheckCgroupV2NeedsANewerRuntime(t *testing.T) {
	a := inspect(t, build(t, true,
		entry{name: "etc/passwd", body: passwd},
		entry{name: "usr/lib/systemd/libsystemd-shared-258.so", body: "ELF"},
	))
	r := find(Check(a, machine("2.4.13.0")), "PRE006")
	if r.Status != probe.Fail {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
	if !strings.Contains(r.Detail, "never boots") && !strings.Contains(r.Detail, "fail to start") {
		t.Errorf("the detail should say what happens: %q", r.Detail)
	}
	// And on a runtime that has the fix, the same archive is fine.
	if r := find(Check(a, machine("2.7.13.0")), "PRE006"); r.Status != probe.OK {
		t.Errorf("2.7.13 should be able to run systemd 258: %s %q", r.Status, r.Summary)
	}
}

func TestCheckRuntimeTooOldForTheFormat(t *testing.T) {
	a := inspect(t, build(t, true, entry{name: "etc/passwd", body: passwd}))
	if r := find(Check(a, machine("2.0.0.0")), "PRE006"); r.Status != probe.Fail {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
}

// A machine that could not be read must not turn into a guess.
func TestCheckRuntimeUnknown(t *testing.T) {
	a := inspect(t, build(t, true, entry{name: "etc/passwd", body: passwd}))
	if r := find(Check(a, machine("")), "PRE006"); r.Status != probe.Skipped {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
	if r := find(Check(a, nil), "PRE006"); r.Status != probe.Skipped {
		t.Fatalf("nil env: status %s", r.Status)
	}
}

func TestCheckWslConfLint(t *testing.T) {
	a := inspect(t, build(t, true,
		entry{name: "etc/passwd", body: passwd},
		entry{name: "etc/wsl.conf", body: "[boot]\nsystmd=true\n"},
	))
	r := find(Check(a, machine("2.7.13.0")), "PRE003")
	if r.Status != probe.Warn || !strings.Contains(r.Summary, "boot.systmd") {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
}

func TestCheckXattrs(t *testing.T) {
	a := inspect(t, build(t, true,
		entry{name: "etc/passwd", body: passwd, xattrs: map[string]string{"security.selinux": "x"}},
	))
	if r := find(Check(a, machine("2.7.13.0")), "PRE005"); r.Status != probe.Warn {
		t.Fatalf("status %s: %q", r.Status, r.Summary)
	}
}
