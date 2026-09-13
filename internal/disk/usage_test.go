package disk

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/wslkit/wslkit/internal/data"
)

func TestParseDuLine(t *testing.T) {
	for _, c := range []struct {
		in    string
		bytes uint64
		path  string
		ok    bool
	}{
		{"1024\t/var/log", 1024, "/var/log", true},
		{"1024\t/var/log/", 1024, "/var/log", true},
		{"512\t/", 512, "/", true},
		{"not a number\t/x", 0, "", false},
		{"1024 /var/log", 0, "", false}, // a space is not a tab
		{"", 0, "", false},
	} {
		gotBytes, gotPath, ok := parseDuLine(c.in)
		if ok != c.ok || gotBytes != c.bytes || gotPath != c.path {
			t.Errorf("parseDuLine(%q) = (%d, %q, %v), want (%d, %q, %v)", c.in, gotBytes, gotPath, ok, c.bytes, c.path, c.ok)
		}
	}
}

// The separator after the prefix is what stops /var/log claiming /var/logbook.
func TestPathContainsRequiresASeparator(t *testing.T) {
	for _, c := range []struct {
		prefix, path string
		want         bool
	}{
		{"/var/log", "/var/log/journal", true},
		{"/var/log", "/var/logbook", false},
		{"/var/log", "/var/log", false},
		{"/", "/var", true},
		{"/", "/", false},
		{"/home/a/.cache", "/home/a/.cache/pip", true},
	} {
		if got := pathContains(c.prefix, c.path); got != c.want {
			t.Errorf("pathContains(%q, %q) = %v, want %v", c.prefix, c.path, got, c.want)
		}
	}
}

// A parent and its child must not both add their bytes to the total, or the
// report claims more space than the guest is using.
func TestAccountForNestingCountsOverlapOnce(t *testing.T) {
	entries := []UsageEntry{
		{Path: "/var/log", Bytes: 300},
		{Path: "/var/log/journal", Bytes: 200},
		{Path: "/tmp", Bytes: 100},
	}
	total := accountForNesting(entries)
	if total != 400 {
		t.Errorf("total = %d, want 400 (the journal is inside /var/log)", total)
	}
	if !entries[0].ContainsOthers {
		t.Error("/var/log should be marked as containing others")
	}
	if entries[1].ContainsOthers || entries[2].ContainsOthers {
		t.Error("only the enclosing entry should be marked")
	}
}

func TestExpandHome(t *testing.T) {
	got := expandHome("~/.cache/pip", []string{"/root", "/home/zoe"})
	want := []string{"/root/.cache/pip", "/home/zoe/.cache/pip"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := expandHome("/var/log", []string{"/root"}); len(got) != 1 || got[0] != "/var/log" {
		t.Errorf("an absolute path should pass through: %v", got)
	}
}

// Every Debian-derived system has a dozen service accounts whose home is / or
// /var/something. Expanding ~ against those would run du over the whole
// filesystem once per account.
func TestReadHomesKeepsOnlyRealHomes(t *testing.T) {
	passwd := strings.Join([]string{
		"root:x:0:0:root:/root:/bin/bash",
		"daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin",
		"bin:x:2:2:bin:/bin:/usr/sbin/nologin",
		"sys:x:3:3:sys:/dev:/usr/sbin/nologin",
		"nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin",
		"zoe:x:1000:1000:Zoe:/home/zoe:/bin/bash",
		"sam:x:1001:1001::/home/sam/:/bin/bash",
	}, "\n")
	host := &fakeHost{byArgs: map[string]CommandResult{
		getentPath + " passwd": {Stdout: passwd},
	}}
	var u Usage
	got := readHomes(context.Background(), env(nil, nil, host), reg("Ubuntu"), 0, &u)
	want := []string{"/home/sam", "/home/zoe", "/root"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
	if len(u.Notes) != 0 {
		t.Errorf("no note was warranted: %v", u.Notes)
	}
}

// If the user list cannot be read, fall back to /root and say so, rather than
// silently reporting nothing for every per-user cache.
func TestReadHomesFallsBackWithANote(t *testing.T) {
	host := &fakeHost{byArgs: map[string]CommandResult{
		getentPath + " passwd": {ExitCode: 127},
	}}
	var u Usage
	got := readHomes(context.Background(), env(nil, nil, host), reg("Ubuntu"), 0, &u)
	if len(got) != 1 || got[0] != "/root" {
		t.Errorf("got %v", got)
	}
	if len(u.Notes) != 1 {
		t.Fatalf("the fallback should be explained: %v", u.Notes)
	}
}

func usageHost(passwd string, sizes map[string]string) *fakeHost {
	byArgs := map[string]CommandResult{
		dfPath + " -B1 /":      {Stdout: "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sdc 1000 400 600 40% /\n"},
		getentPath + " passwd": {Stdout: passwd},
	}
	for path, out := range sizes {
		byArgs[duPath+" -sbx "+path] = CommandResult{Stdout: out}
	}
	return &fakeHost{byArgs: byArgs}
}

func TestMeasureUsageReportsWhatItFound(t *testing.T) {
	host := usageHost("root:x:0:0:root:/root:/bin/bash", map[string]string{
		"/var/cache/apt/archives": "150\t/var/cache/apt/archives",
		"/var/log":                "300\t/var/log",
		"/var/log/journal":        "200\t/var/log/journal",
	})
	u, err := MeasureUsage(context.Background(), env(nil, nil, host), reg("Ubuntu"), UsageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if u.GuestUsed != 400 || u.GuestFree != 600 {
		t.Errorf("df: used=%d free=%d", u.GuestUsed, u.GuestFree)
	}
	if len(u.Entries) != 3 {
		t.Fatalf("expected the three paths that measured non-zero, got %d: %+v", len(u.Entries), u.Entries)
	}
	// Biggest first.
	if u.Entries[0].Bytes != 300 {
		t.Errorf("entries are not sorted: %+v", u.Entries)
	}
	// The journal is inside /var/log, so its bytes are counted once.
	if u.Counted != 450 {
		t.Errorf("counted = %d, want 450", u.Counted)
	}
}

// Most of the catalogue is absent on any given machine. Listing those rows as
// zero would bury the ones that matter.
func TestMeasureUsageSkipsPathsThatAreAbsentOrEmpty(t *testing.T) {
	host := usageHost("root:x:0:0:root:/root:/bin/bash", map[string]string{
		"/tmp": "0\t/tmp",
	})
	u, err := MeasureUsage(context.Background(), env(nil, nil, host), reg("Ubuntu"), UsageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Entries) != 0 {
		t.Errorf("nothing measured, so nothing should be listed: %+v", u.Entries)
	}
}

// --top changes what is shown, never what is counted.
func TestTopTruncatesAfterTheArithmetic(t *testing.T) {
	host := usageHost("root:x:0:0:root:/root:/bin/bash", map[string]string{
		"/var/cache/apt/archives": "150\t/var/cache/apt/archives",
		"/tmp":                    "90\t/tmp",
		"/var/tmp":                "80\t/var/tmp",
	})
	full, err := MeasureUsage(context.Background(), env(nil, nil, host), reg("Ubuntu"), UsageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	top, err := MeasureUsage(context.Background(), env(nil, nil, host), reg("Ubuntu"), UsageOptions{Top: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(top.Entries) != 1 || top.Entries[0].Bytes != 150 {
		t.Fatalf("expected only the largest, got %+v", top.Entries)
	}
	if top.Counted != full.Counted {
		t.Errorf("counted changed with --top: %d vs %d", top.Counted, full.Counted)
	}
}

func TestMeasureUsageLabelsPerUserCachesWhenThereIsMoreThanOneHome(t *testing.T) {
	passwd := "root:x:0:0:root:/root:/bin/bash\nzoe:x:1000:1000::/home/zoe:/bin/bash"
	host := usageHost(passwd, map[string]string{
		"/root/.npm":     "10\t/root/.npm",
		"/home/zoe/.npm": "20\t/home/zoe/.npm",
	})
	u, err := MeasureUsage(context.Background(), env(nil, nil, host), reg("Ubuntu"), UsageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Entries) != 2 {
		t.Fatalf("expected one row per home, got %+v", u.Entries)
	}
	for _, e := range u.Entries {
		if !strings.Contains(e.Label, e.Path) {
			t.Errorf("with several homes the label must disambiguate: %q", e.Label)
		}
	}
}

func TestMeasureUsageRefusesWSL1(t *testing.T) {
	_, err := MeasureUsage(context.Background(), env(nil, nil, nil), reg("Legacy", func(r *Registration) { r.Version = 1 }), UsageOptions{})
	if err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestByDirectoryUsesTheShortDepthFlag(t *testing.T) {
	host := usageHost("root:x:0:0:root:/root:/bin/bash", map[string]string{"/var/log": "300\t/var/log"})
	host.byArgs[duPath+" -bx -d 2 /"] = CommandResult{Stdout: "300\t/var/log\n1000\t/\n50\t/etc\n"}
	u, err := MeasureUsage(context.Background(), env(nil, nil, host), reg("Ubuntu"), UsageOptions{ByDirectory: true, Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	// The long form --max-depth is rejected by busybox du, which Alpine
	// ships, so the short flag is the only portable spelling.
	found := false
	for _, c := range host.calls {
		if strings.Contains(c, "-d 2") {
			found = true
		}
		if strings.Contains(c, "--max-depth") {
			t.Errorf("busybox rejects the long form: %q", c)
		}
	}
	if !found {
		t.Fatalf("the depth flag was not passed: %v", host.calls)
	}
	// The root row is the whole filesystem, which df already reported.
	for _, d := range u.Directories {
		if d.Path == "/" {
			t.Error("the root row should be dropped")
		}
	}
	if len(u.Directories) != 2 {
		t.Fatalf("expected two directory rows, got %+v", u.Directories)
	}
	if u.Directories[0].Path != "/var/log" || u.Directories[0].Attributed != 300 {
		t.Errorf("the catalogue should explain /var/log: %+v", u.Directories[0])
	}
}

func TestDepthIsClampedToSomethingReadable(t *testing.T) {
	host := usageHost("root:x:0:0:root:/root:/bin/bash", nil)
	host.byArgs[duPath+" -bx -d "+strconv.Itoa(MaxDepth)+" /"] = CommandResult{Stdout: ""}
	if _, err := MeasureUsage(context.Background(), env(nil, nil, host), reg("Ubuntu"), UsageOptions{ByDirectory: true, Depth: 99}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(host.calls, "; ")
	if !strings.Contains(joined, "-d "+strconv.Itoa(MaxDepth)+" /") {
		t.Errorf("depth was not clamped: %v", host.calls)
	}
}

func TestRenderUsageExplainsOverlapAndUnsafeRows(t *testing.T) {
	u := Usage{
		Distro:    "Ubuntu",
		GuestUsed: 1000,
		Counted:   450,
		Entries: []UsageEntry{
			{Path: "/var/log", Label: "logs", Bytes: 300, Safe: false, ContainsOthers: true},
			{Path: "/var/log/journal", Label: "systemd journal", Bytes: 200, Safe: true},
			{Path: "/tmp", Label: "temporary files", Bytes: 150, Safe: true},
		},
	}
	var b bytes.Buffer
	RenderUsage(&b, u)
	out := b.String()
	if !strings.Contains(out, "rows marked no are not caches") {
		t.Errorf("an unsafe row should be explained:\n%s", out)
	}
	if !strings.Contains(out, "/var/log contains other rows above") {
		t.Errorf("overlap should be explained:\n%s", out)
	}
	if !strings.Contains(out, "of 1000 B the guest reports in use") {
		t.Errorf("the total should be put in context:\n%s", out)
	}
}

func TestRenderUsageWithNothingFound(t *testing.T) {
	var b bytes.Buffer
	RenderUsage(&b, Usage{Distro: "Ubuntu"})
	if !strings.Contains(b.String(), "nothing in the cache catalogue is using space") {
		t.Errorf("got %q", b.String())
	}
}

func TestUsageJSONAlwaysHasEntriesEvenWhenEmpty(t *testing.T) {
	o := UsageJSON(Usage{Distro: "Ubuntu"})
	entries, ok := o["entries"].([]map[string]any)
	if !ok {
		t.Fatalf("entries should always be a list, got %T", o["entries"])
	}
	if len(entries) != 0 {
		t.Errorf("expected an empty list, got %v", entries)
	}
	// Directories only appear when asked for.
	if _, ok := o["directories"]; ok {
		t.Error("directories should be absent unless --by-directory was given")
	}
}

// The catalogue is data, and the shape the report depends on is enforced when
// it loads rather than discovered halfway through a walk.
func TestTheCatalogueIsValid(t *testing.T) {
	list, err := data.Caches()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 20 {
		t.Errorf("the catalogue looks truncated: %d entries", len(list))
	}
	for _, c := range list {
		if strings.HasSuffix(c.Path, "/") {
			t.Errorf("%q ends in a slash, which breaks the containment check", c.Path)
		}
		if !strings.HasPrefix(c.Path, "/") && !strings.HasPrefix(c.Path, "~/") {
			t.Errorf("%q is neither absolute nor per-user", c.Path)
		}
	}
	// The deliberate nesting is what the overlap accounting exists for, so
	// assert it is still there.
	paths := map[string]bool{}
	for _, c := range list {
		paths[c.Path] = true
	}
	for _, pair := range [][2]string{{"/var/log", "/var/log/journal"}, {"~/.cache", "~/.cache/pip"}} {
		if !paths[pair[0]] || !paths[pair[1]] {
			t.Errorf("expected both %q and %q in the catalogue", pair[0], pair[1])
		}
	}
}
