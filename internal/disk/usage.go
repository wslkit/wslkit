package disk

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wslkit/wslkit/internal/data"
)

// Guest programs are addressed by absolute path: wsl.exe does not search PATH
// for the program it is asked to exec.
const (
	dfPath     = "/bin/df"
	duPath     = "/usr/bin/du"
	getentPath = "/usr/bin/getent"
)

// DefaultUsageTimeout bounds the whole walk. Measuring a large guest with du
// takes minutes.
const DefaultUsageTimeout = 5 * time.Minute

// DefaultDepth is how deep the directory breakdown goes by default.
const DefaultDepth = 2

// MaxDepth bounds the breakdown. Deeper than this and the table is longer than
// anyone reads, and du takes proportionally longer to produce it.
const MaxDepth = 8

// UsageEntry is one catalogued path and what it measured.
type UsageEntry struct {
	Path  string
	Label string
	Bytes uint64
	Safe  bool
	Note  string
	// ContainsOthers marks an entry that encloses other entries in the
	// table, so a reader can see why the numbers do not add up naively.
	ContainsOthers bool
}

// UsageDir is one row of the directory breakdown.
type UsageDir struct {
	Path  string
	Bytes uint64
	Depth int
	// Attributed is how much of this directory the catalogue already
	// explains, and AttributedTo names the largest such entry.
	Attributed   uint64
	AttributedTo string
}

// Usage is the result of looking inside a distribution.
type Usage struct {
	Distro    string
	GuestUsed uint64
	GuestFree uint64
	// Counted is the sum of the entries that are not inside another entry,
	// so nothing is counted twice.
	Counted     uint64
	Entries     []UsageEntry
	Directories []UsageDir
	Notes       []string
}

// UsageOptions controls the walk.
type UsageOptions struct {
	// Top limits the table to the largest N entries. Zero shows all.
	Top int
	// ByDirectory adds a breakdown of the whole guest by directory.
	ByDirectory bool
	// Depth is how deep that breakdown goes.
	Depth int
	// Timeout bounds each guest command.
	Timeout time.Duration
}

// PlanUsage answers --dry-run.
//
// Usage only reads, and a dry run must be free of side effects rather than
// merely free of mutations, so it does not measure either: starting a
// distribution to look inside it is a side effect.
func PlanUsage(r Registration) (Plan, error) {
	p := Plan{Subject: r.Name, SubjectKey: "distribution"}
	if r.Version != 2 {
		return p, fmt.Errorf("%w: %s stores its files directly on NTFS; look at them from Windows instead", ErrNotWSL2, r.Name)
	}
	p.Add("read the filesystem usage inside %s", r.Name)
	return p, nil
}

// MeasureUsage looks inside a distribution and reports where the space went.
func MeasureUsage(ctx context.Context, e Env, r Registration, o UsageOptions) (Usage, error) {
	u := Usage{Distro: r.Name}
	if r.Version != 2 {
		return u, fmt.Errorf("%w: %s", ErrNotWSL2, r.Name)
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = DefaultUsageTimeout
	}

	res, err := e.Host.RunAsRoot(ctx, r.Name, []string{dfPath, "-B1", trimMount}, timeout)
	if err != nil {
		return u, fmt.Errorf("disk: reading the filesystem usage of %s: %w", r.Name, err)
	}
	if res.ExitCode != 0 {
		return u, fmt.Errorf("disk: df exited %d in %s: %s", res.ExitCode, r.Name, oneLine(res.Stderr))
	}
	u.GuestUsed, u.GuestFree, err = ParseDF(res.Stdout)
	if err != nil {
		return u, fmt.Errorf("disk: %w", err)
	}

	homes := readHomes(ctx, e, r, timeout, &u)
	catalogue, err := data.Caches()
	if err != nil {
		return u, err
	}

	u.Entries = measureEntries(ctx, e, r, timeout, catalogue, homes)
	sort.SliceStable(u.Entries, func(i, j int) bool { return u.Entries[i].Bytes > u.Entries[j].Bytes })
	u.Counted = accountForNesting(u.Entries)

	if o.ByDirectory {
		u.Directories = measureDirectories(ctx, e, r, timeout, o.Depth, u.Entries, &u)
	}

	// Truncation happens after the arithmetic, so --top changes what is
	// shown and never what is counted.
	if o.Top > 0 && len(u.Entries) > o.Top {
		u.Entries = u.Entries[:o.Top]
	}
	if o.Top > 0 && len(u.Directories) > o.Top {
		u.Directories = u.Directories[:o.Top]
	}
	return u, nil
}

// readHomes lists the real home directories.
//
// Only /home/* and /root count. Every Debian-derived system has a dozen service
// accounts whose home is / or /var/something, and expanding ~ against those
// would run du over the whole filesystem once per account.
func readHomes(ctx context.Context, e Env, r Registration, timeout time.Duration, u *Usage) []string {
	res, err := e.Host.RunAsRoot(ctx, r.Name, []string{getentPath, "passwd"}, timeout)
	if err != nil || res.ExitCode != 0 {
		u.Notes = append(u.Notes, "the list of users could not be read, so per-user caches were only looked for under /root")
		return []string{"/root"}
	}
	var homes []string
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(res.Stdout, "\r\n", "\n"), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 6 {
			continue
		}
		home := strings.TrimRight(fields[5], "/")
		if home == "" || seen[home] {
			continue
		}
		if home != "/root" && !strings.HasPrefix(home, "/home/") {
			continue
		}
		seen[home] = true
		homes = append(homes, home)
	}
	if len(homes) == 0 {
		return []string{"/root"}
	}
	sort.Strings(homes)
	return homes
}

// measureEntries runs du over every catalogued path.
func measureEntries(ctx context.Context, e Env, r Registration, timeout time.Duration, catalogue []data.Cache, homes []string) []UsageEntry {
	var out []UsageEntry
	for _, c := range catalogue {
		for _, path := range expandHome(c.Path, homes) {
			n, ok := du(ctx, e, r, timeout, path)
			// A path that is not there, or that measures nothing, is
			// the normal case for most of the catalogue on most
			// machines. Listing it would bury the rows that matter.
			if !ok || n == 0 {
				continue
			}
			label := c.Label
			if len(homes) > 1 && strings.HasPrefix(c.Path, "~/") {
				label = fmt.Sprintf("%s (%s)", label, path)
			}
			out = append(out, UsageEntry{Path: path, Label: label, Bytes: n, Safe: c.Safe, Note: c.Note})
		}
	}
	return out
}

// expandHome turns a ~/ path into one path per home directory.
func expandHome(path string, homes []string) []string {
	if !strings.HasPrefix(path, "~/") {
		return []string{path}
	}
	rest := strings.TrimPrefix(path, "~")
	out := make([]string, 0, len(homes))
	for _, h := range homes {
		out = append(out, h+rest)
	}
	return out
}

// du measures one path. -s for a total, -b for bytes, -x so bind mounts and the
// Windows drives under /mnt are not counted as part of the disk.
func du(ctx context.Context, e Env, r Registration, timeout time.Duration, path string) (uint64, bool) {
	res, err := e.Host.RunAsRoot(ctx, r.Name, []string{duPath, "-sbx", path}, timeout)
	if err != nil || res.ExitCode != 0 {
		return 0, false
	}
	n, _, ok := parseDuLine(lastNonEmptyLine(res.Stdout))
	return n, ok
}

// parseDuLine reads one "<bytes>\t<path>" line.
func parseDuLine(line string) (uint64, string, bool) {
	tab := strings.IndexByte(line, '\t')
	if tab <= 0 {
		return 0, "", false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(line[:tab]), 10, 64)
	if err != nil {
		return 0, "", false
	}
	path := strings.TrimRight(strings.TrimSpace(line[tab+1:]), "/")
	if path == "" {
		path = "/"
	}
	return n, path, true
}

// pathContains reports whether prefix encloses path.
//
// The separator after the prefix is required, or /var/log would claim
// /var/logbook.
func pathContains(prefix, path string) bool {
	if prefix == path {
		return false
	}
	if prefix == "/" {
		return path != "/"
	}
	return strings.HasPrefix(path, prefix+"/")
}

// accountForNesting marks the entries that enclose others and returns the total
// of those that are not themselves inside another entry, so /var/log and
// /var/log/journal do not both count the same bytes.
func accountForNesting(entries []UsageEntry) uint64 {
	var total uint64
	for i := range entries {
		inside := false
		for j := range entries {
			if i == j {
				continue
			}
			if pathContains(entries[j].Path, entries[i].Path) {
				inside = true
			}
			if pathContains(entries[i].Path, entries[j].Path) {
				entries[i].ContainsOthers = true
			}
		}
		if !inside {
			total += entries[i].Bytes
		}
	}
	return total
}

// measureDirectories walks the whole guest to a fixed depth.
func measureDirectories(ctx context.Context, e Env, r Registration, timeout time.Duration, depth int, entries []UsageEntry, u *Usage) []UsageDir {
	if depth <= 0 {
		depth = DefaultDepth
	}
	if depth > MaxDepth {
		depth = MaxDepth
	}
	// -d rather than --max-depth: busybox du, which Alpine ships, does not
	// accept the long form.
	res, err := e.Host.RunAsRoot(ctx, r.Name, []string{duPath, "-bx", "-d", strconv.Itoa(depth), "/"}, timeout)
	if err != nil {
		u.Notes = append(u.Notes, "the directory breakdown could not be produced: "+err.Error())
		return nil
	}
	if res.ExitCode != 0 {
		// du exits non-zero for anything it could not read, and on a
		// running guest /proc entries come and go constantly. The rows it
		// did produce are still worth having.
		u.Notes = append(u.Notes, "du reported some directories it could not read, so the breakdown may be incomplete")
	}

	var out []UsageDir
	for _, line := range strings.Split(strings.ReplaceAll(res.Stdout, "\r\n", "\n"), "\n") {
		n, path, ok := parseDuLine(line)
		if !ok || path == "/" {
			// The root row is the whole filesystem, which df already
			// reported.
			continue
		}
		d := UsageDir{Path: path, Bytes: n, Depth: strings.Count(path, "/")}
		d.Attributed, d.AttributedTo = attribute(path, entries)
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

// attribute reports how much of a directory the catalogue already explains.
func attribute(dir string, entries []UsageEntry) (uint64, string) {
	var total uint64
	var largest string
	var largestBytes uint64
	for _, en := range entries {
		if en.Path != dir && !pathContains(dir, en.Path) {
			continue
		}
		// Only entries not inside another counted entry, or the same
		// bytes are added twice.
		nested := false
		for _, other := range entries {
			if other.Path != en.Path && pathContains(other.Path, en.Path) && (other.Path == dir || pathContains(dir, other.Path)) {
				nested = true
				break
			}
		}
		if nested {
			continue
		}
		total += en.Bytes
		if en.Bytes > largestBytes {
			largestBytes, largest = en.Bytes, en.Label
		}
	}
	return total, largest
}

// RenderUsage writes the human-readable report.
func RenderUsage(w io.Writer, u Usage) {
	if len(u.Entries) == 0 {
		fmt.Fprintf(w, "%s: nothing in the cache catalogue is using space\n", u.Distro)
		return
	}
	t := Table{Headers: []string{"SIZE", "WHAT", "CLEARABLE", "PATH"}}
	for _, en := range u.Entries {
		t.Rows = append(t.Rows, []string{FormatSize(en.Bytes), en.Label, yesNo(en.Safe), en.Path})
	}
	fmt.Fprint(w, t.String())
	fmt.Fprintf(w, "\n%s found, of %s the guest reports in use\n", FormatSize(u.Counted), FormatSize(u.GuestUsed))

	if anyUnsafe(u.Entries) {
		fmt.Fprintln(w, "\nrows marked no are not caches: wslkit cannot tell whether what they hold still matters")
	}
	for _, en := range u.Entries {
		if en.ContainsOthers {
			fmt.Fprintf(w, "%s contains other rows above; the total counts those bytes once\n", en.Path)
		}
	}

	if len(u.Directories) > 0 {
		fmt.Fprintln(w)
		dt := Table{Headers: []string{"SIZE", "DIRECTORY", "OF WHICH KNOWN", "LARGEST KNOWN"}}
		for _, d := range u.Directories {
			known := "-"
			if d.Attributed > 0 {
				known = FormatSize(d.Attributed)
			}
			name := d.AttributedTo
			if name == "" {
				name = "-"
			}
			dt.Rows = append(dt.Rows, []string{FormatSize(d.Bytes), d.Path, known, name})
		}
		fmt.Fprint(w, dt.String())
		fmt.Fprintln(w, "\ndirectories overlap: a parent includes everything below it")
	}

	for _, n := range u.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
}

func anyUnsafe(entries []UsageEntry) bool {
	for _, e := range entries {
		if !e.Safe {
			return true
		}
	}
	return false
}

// UsageJSON is the object printed for --json.
func UsageJSON(u Usage) map[string]any {
	entries := make([]map[string]any, 0, len(u.Entries))
	for _, en := range u.Entries {
		o := map[string]any{
			"path":            en.Path,
			"label":           en.Label,
			"bytes":           en.Bytes,
			"safe":            en.Safe,
			"contains_others": en.ContainsOthers,
		}
		if en.Note != "" {
			o["note"] = en.Note
		}
		entries = append(entries, o)
	}
	out := map[string]any{
		"distribution": u.Distro,
		"guest_used":   u.GuestUsed,
		"guest_free":   u.GuestFree,
		"counted":      u.Counted,
		"entries":      entries,
	}
	if len(u.Directories) > 0 {
		dirs := make([]map[string]any, 0, len(u.Directories))
		for _, d := range u.Directories {
			o := map[string]any{
				"path":             d.Path,
				"bytes":            d.Bytes,
				"depth":            d.Depth,
				"attributed_bytes": d.Attributed,
			}
			if d.AttributedTo != "" {
				o["attributed_to"] = d.AttributedTo
			}
			dirs = append(dirs, o)
		}
		out["directories"] = dirs
	}
	if len(u.Notes) > 0 {
		out["notes"] = u.Notes
	}
	return out
}
