package disk

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatSizeTruncatesRatherThanRounds(t *testing.T) {
	for _, c := range []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		// One byte short of a gibibyte must never read as a gibibyte.
		{1<<30 - 1, "1023.9 MiB"},
		{1 << 30, "1.0 GiB"},
		{15 << 30, "15.0 GiB"},
		{1 << 40, "1.0 TiB"},
		{1 << 50, "1.0 PiB"},
		{2 << 20, "2.0 MiB"},
	} {
		if got := FormatSize(c.in); got != c.want {
			t.Errorf("FormatSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTableAlignsColumnsAndLeavesNoTrailingSpace(t *testing.T) {
	out := Table{
		Headers: []string{"NAME", "SIZE"},
		Rows:    [][]string{{"a", "1 B"}, {"muchlonger", "2 B"}},
	}.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d: %q", len(lines), out)
	}
	for _, l := range lines {
		if strings.TrimRight(l, " ") != l {
			t.Errorf("line has trailing whitespace: %q", l)
		}
	}
	if !strings.HasPrefix(lines[2], "muchlonger  2 B") {
		t.Errorf("columns are not aligned: %q", lines[2])
	}
	// The header must be padded to the widest cell below it.
	if !strings.HasPrefix(lines[0], "NAME        SIZE") {
		t.Errorf("header not padded to the widest row: %q", lines[0])
	}
}

// Widths are counted in code points. Measuring in bytes would push the columns
// out for any distribution with a non-ASCII name.
func TestTableMeasuresWidthInCodePointsNotBytes(t *testing.T) {
	out := Table{Headers: []string{"NAME", "X"}, Rows: [][]string{{"Ubuntu", "1"}, {"Debián", "2"}}}.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// The last column must begin at the same rune offset on every row, which
	// holds only if the widths were measured in runes. "Debián" is six runes
	// but seven bytes, so counting bytes would shift this row left by one.
	offset := func(s string) int { return len([]rune(s)) - 1 }
	if a, b := offset(lines[1]), offset(lines[2]); a != b {
		t.Errorf("columns misaligned across a non-ASCII name (%d vs %d):\n%s", a, b, out)
	}
}

func row() Row {
	running := true
	return Row{
		Reg: reg("Ubuntu", func(r *Registration) {
			r.IsDefault = true
			r.Flavor = "ubuntu"
			r.OsVersion = "24.04"
			r.Flags = 15
			r.DefaultUID = 1000
			r.Modern = 1
		}),
		Info: Info{
			FileSize:    u64(15 << 30),
			SizeOnDisk:  u64(9 << 30),
			Allocated:   u64(9 << 30),
			Sparse:      new(bool),
			VirtualSize: u64(1 << 40),
			GuestUsed:   u64(8 << 30),
			GuestFree:   u64(100 << 30),
		},
		Running: &running,
	}
}

func TestRenderListMarksTheDefaultAndDashesWhatIsUnknown(t *testing.T) {
	var b bytes.Buffer
	r := row()
	r.Info.GuestUsed = nil // unmeasured
	RenderList(&b, []Row{r})
	out := b.String()
	if !strings.Contains(out, "Ubuntu *") {
		t.Errorf("the default distribution should be marked:\n%s", out)
	}
	if !strings.Contains(out, "running") {
		t.Errorf("state missing:\n%s", out)
	}
	// Both guest used and reclaimable become unknowable.
	if strings.Count(out, "-") < 2 {
		t.Errorf("unmeasured columns should be dashes:\n%s", out)
	}
}

func TestRenderInfoExplainsAnAbsentVhdFileName(t *testing.T) {
	var b bytes.Buffer
	RenderInfo(&b, row(), `Software\...\Lxss\{Ubuntu}`)
	out := b.String()
	if !strings.Contains(out, "vhd file name: - (absent; defaults to ext4.vhdx)") {
		t.Errorf("an absent name should be explained, not blank:\n%s", out)
	}
	if !strings.Contains(out, "flags:         15 (interop, append-nt-path, drive-mounting, undocumented(0x8))") {
		t.Errorf("flags should be decoded:\n%s", out)
	}
}

func TestRenderInfoPrintsNotes(t *testing.T) {
	var b bytes.Buffer
	r := row()
	r.Info.Notes = []string{"something was unreadable"}
	RenderInfo(&b, r, "key")
	if !strings.Contains(b.String(), "note: something was unreadable") {
		t.Errorf("notes missing:\n%s", b.String())
	}
}

// Absent is not zero. A consumer must be able to tell "could not be measured"
// from "measured as nothing", so unmeasured fields are omitted entirely.
func TestListJSONOmitsWhatWasNotMeasured(t *testing.T) {
	o := ListJSON(Row{Reg: reg("Ubuntu")})
	for _, key := range []string{"file_size", "size_on_disk", "guest_used", "reclaimable", "sparse", "virtual_size", "notes"} {
		if _, ok := o[key]; ok {
			t.Errorf("%q should be absent when nothing was measured", key)
		}
	}
	for _, key := range []string{"name", "guid", "version", "default", "vhdx_path"} {
		if _, ok := o[key]; !ok {
			t.Errorf("%q must always be present", key)
		}
	}
}

func TestListJSONReportsSizesAsIntegerBytes(t *testing.T) {
	b, err := json.Marshal(ListJSON(row()))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"size_on_disk":9663676416`) {
		t.Errorf("sizes must be integer bytes, not strings:\n%s", s)
	}
	if !strings.Contains(s, `"reclaimable":1073741824`) {
		t.Errorf("reclaimable should be derived:\n%s", s)
	}
}

// Keys come out sorted, which keeps the output stable regardless of the order
// fields were added in.
func TestJSONKeysAreSorted(t *testing.T) {
	b, _ := json.Marshal(ListJSON(row()))
	s := string(b)
	last := ""
	for _, part := range strings.Split(s[1:len(s)-1], `,"`) {
		key := part[:strings.Index(part, `"`)+1]
		key = strings.Trim(key, `"`)
		if last != "" && key < last {
			t.Fatalf("keys are not sorted: %q came after %q\n%s", key, last, s)
		}
		last = key
	}
}

func TestInfoJSONAddsTheRegistrationDetail(t *testing.T) {
	o := InfoJSON(row(), `Software\...\Lxss\{Ubuntu}`)
	for _, key := range []string{"registry_key", "base_path", "modern", "default_uid", "flags", "flags_decoded", "running"} {
		if _, ok := o[key]; !ok {
			t.Errorf("%q missing from info output", key)
		}
	}
	if o["flags_decoded"] != "interop, append-nt-path, drive-mounting, undocumented(0x8)" {
		t.Errorf("flags_decoded = %v", o["flags_decoded"])
	}
	// base_path is reported exactly as stored, prefix and all.
	if o["base_path"] != `C:\wsl\Ubuntu` {
		t.Errorf("base_path = %v", o["base_path"])
	}
}

func TestWriteJSONLineEndsWithANewline(t *testing.T) {
	var b bytes.Buffer
	if err := WriteJSONLine(&b, map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "{\"a\":1}\n" {
		t.Errorf("got %q", got)
	}
}
