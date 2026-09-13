// Package top implements `wslkit top`: what the WSL utility VM is using, and
// which distribution is responsible for it.
//
// How memory is attributed depends on the WSL version, and the difference is
// not cosmetic.
//
// From WSL 2.8 or so, mini_init creates /sys/fs/cgroup/wsl-user/distro-<pid>
// per distribution when wsl2.isolateDistroCgroup is on, which it is by default.
// That cgroup accounts for exactly one distribution, so reading memory.current
// from it is true attribution. There is no cgroup namespace, so one
// distribution can read every other one's node.
//
// Before that, every distribution's processes sit in the true root cgroup
// together. Reading cgroup accounting there returns VM-wide numbers that look
// per-distribution and are not: measured on 2.7.13, burning CPU in one
// distribution moves the counter every other distribution reads, and so does
// allocating memory. See docs/research/2026-09-cgroups.md.
//
// So where the per-distribution cgroup exists, it is used. Where it does not,
// the resident set of each distribution's own processes is summed instead,
// which is real attribution of process memory but undercounts: page cache and
// kernel memory belong to no distribution. The reports say which was used and
// what it leaves out, rather than presenting two different measurements as if
// they were the same number.
package top

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Method is how a distribution's memory was attributed.
type Method string

const (
	// MethodCgroup read the per-distribution cgroup, which accounts for that
	// distribution and nothing else.
	MethodCgroup Method = "cgroup"
	// MethodProcesses summed the resident set of the distribution's own
	// processes, because this WSL has no per-distribution cgroup.
	MethodProcesses Method = "processes"
)

// Sample is one measurement of one distribution.
type Sample struct {
	Distro string
	Method Method
	// MemoryBytes is what the distribution is using.
	MemoryBytes uint64
	// AnonBytes is the part of that which automatic reclaim can never return
	// to Windows, so it is what keeps vmmem large. Only the cgroup method
	// can report it.
	AnonBytes *uint64
	// Processes is how many processes the distribution can see.
	Processes int
	// CPUUsec is CPU time used so far, which a rate is computed from.
	CPUUsec uint64
	// Init is the name of pid 1, which distinguishes a systemd distribution.
	Init string
	// CgroupPath is the node the numbers came from, when there was one.
	CgroupPath string
	// Err explains a distribution that could not be measured, so one that is
	// shutting down does not hide the rest.
	Err error
}

// VM is what the utility VM as a whole is using. This is the number Task
// Manager shows against vmmem, and it belongs to no single distribution.
type VM struct {
	TotalBytes     uint64
	FreeBytes      uint64
	AvailableBytes uint64
	// CachedBytes is page cache: memory the VM holds and can give back.
	CachedBytes uint64
	// AnonBytes is anonymous memory across the whole VM, which automatic
	// reclaim cannot return.
	AnonBytes uint64
	CPUs      int
	UptimeSec uint64
}

// UsedBytes is what the VM has committed.
func (v VM) UsedBytes() uint64 {
	if v.TotalBytes < v.FreeBytes {
		return 0
	}
	return v.TotalBytes - v.FreeBytes
}

// Report is a whole measurement.
type Report struct {
	VM       VM
	Samples  []Sample
	Interval time.Duration
	Notes    []string
}

// Attributed is the memory the distributions account for.
func (r Report) Attributed() uint64 {
	var total uint64
	for _, s := range r.Samples {
		total += s.MemoryBytes
	}
	return total
}

// Method is how the report attributed memory, or empty when the distributions
// disagreed, which should not happen on one VM.
func (r Report) Method() Method {
	var seen Method
	for _, s := range r.Samples {
		if s.Err != nil || s.Method == "" {
			continue
		}
		if seen == "" {
			seen = s.Method
			continue
		}
		if seen != s.Method {
			return ""
		}
	}
	return seen
}

// CPUPercent is what share of one processor a distribution used between two
// samples. It needs both, so it is nil for a single measurement.
func CPUPercent(before, after Sample, interval time.Duration) *float64 {
	if interval <= 0 || after.CPUUsec < before.CPUUsec {
		return nil
	}
	elapsed := float64(interval.Microseconds())
	if elapsed == 0 {
		return nil
	}
	pct := float64(after.CPUUsec-before.CPUUsec) / elapsed * 100
	return &pct
}

// SampleScript is what runs inside a distribution to measure it.
//
// It prefers the per-distribution cgroup and falls back to /proc, deciding
// inside the guest so the whole measurement is one round trip rather than a
// probe followed by a measurement.
//
// It reads /proc directly rather than calling ps, which is not on every
// distribution and formats differently where it is. Output is key=value lines,
// so parsing never depends on column positions or on a locale.
//
// Line endings matter: this is fed to a shell inside the guest, and a carriage
// return turns a line into a command that does not exist.
const SampleScript = `node=$(awk -F: '{print $3}' /proc/1/cgroup 2>/dev/null | head -1)
case "$node" in
  /wsl-user/distro-*) ;;
  *) node="" ;;
esac
# The distribution's own node is the one directly under wsl-user; systemd puts
# pid 1 in a child of it, so walk back up to the distro-N level.
if [ -n "$node" ]; then
  node=$(echo "$node" | sed -n 's,^\(/wsl-user/distro-[0-9]*\).*,\1,p')
fi
dir="/sys/fs/cgroup$node"
if [ -n "$node" ] && [ -r "$dir/memory.current" ]; then
  echo "method=cgroup"
  echo "cgroup_path=$node"
  echo "memory_bytes=$(cat "$dir/memory.current")"
  echo "anon_bytes=$(awk '$1=="anon"{print $2; exit}' "$dir/memory.stat" 2>/dev/null)"
  echo "cpu_usec=$(awk '$1=="usage_usec"{print $2; exit}' "$dir/cpu.stat" 2>/dev/null)"
else
  echo "method=processes"
  rss=0
  ticks=0
  for p in /proc/[0-9]*; do
    [ -r "$p/stat" ] || continue
    r=$(awk '/^VmRSS:/{print $2; exit}' "$p/status" 2>/dev/null)
    rss=$((rss + ${r:-0}))
    # Fields 14 and 15 are the process own user and system time; 16 and 17
    # are what it reaped from children that have since exited. Without the
    # latter a fork-heavy workload reads as almost idle, because the
    # processes doing the work are gone by the time the next sample runs.
    t=$(awk '{print $14 + $15 + $16 + $17}' "$p/stat" 2>/dev/null)
    ticks=$((ticks + ${t:-0}))
  done
  hz=$(getconf CLK_TCK 2>/dev/null || echo 100)
  echo "memory_kb=$rss"
  echo "cpu_ticks=$ticks"
  echo "clock_hz=$hz"
fi
echo "processes=$(ls -d /proc/[0-9]* 2>/dev/null | wc -l)"
echo "init=$(cat /proc/1/comm 2>/dev/null)"
echo "mem_total_kb=$(awk '/^MemTotal:/{print $2}' /proc/meminfo)"
echo "mem_free_kb=$(awk '/^MemFree:/{print $2}' /proc/meminfo)"
echo "mem_available_kb=$(awk '/^MemAvailable:/{print $2}' /proc/meminfo)"
echo "mem_cached_kb=$(awk '/^Cached:/{print $2; exit}' /proc/meminfo)"
echo "mem_anon_kb=$(awk '/^AnonPages:/{print $2}' /proc/meminfo)"
echo "cpus=$(awk '/^processor/{n++} END{print n+0}' /proc/cpuinfo)"
echo "uptime_sec=$(awk '{print int($1)}' /proc/uptime)"
`

// ParseSample reads what SampleScript printed.
func ParseSample(distro, out string) (Sample, VM, error) {
	s := Sample{Distro: distro}
	var vm VM
	fields := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		fields[line[:eq]] = strings.TrimSpace(line[eq+1:])
	}
	if len(fields) == 0 {
		return s, vm, fmt.Errorf("top: %s printed nothing that could be read", distro)
	}

	num := func(key string) uint64 {
		v, err := strconv.ParseUint(fields[key], 10, 64)
		if err != nil {
			return 0
		}
		return v
	}
	has := func(key string) bool {
		_, ok := fields[key]
		return ok
	}

	s.Processes = int(num("processes"))
	s.Init = fields["init"]

	switch Method(fields["method"]) {
	case MethodCgroup:
		s.Method = MethodCgroup
		s.CgroupPath = fields["cgroup_path"]
		s.MemoryBytes = num("memory_bytes")
		if has("anon_bytes") && fields["anon_bytes"] != "" {
			v := num("anon_bytes")
			s.AnonBytes = &v
		}
		s.CPUUsec = num("cpu_usec")
	case MethodProcesses:
		s.Method = MethodProcesses
		s.MemoryBytes = num("memory_kb") * 1024
		// Process CPU comes out of /proc in clock ticks, whose rate is not
		// guaranteed to be 100, so the distribution reports its own.
		hz := num("clock_hz")
		if hz == 0 {
			hz = 100
		}
		s.CPUUsec = num("cpu_ticks") * 1_000_000 / hz
	default:
		return s, vm, fmt.Errorf("top: %s did not say how it measured itself", distro)
	}

	vm.TotalBytes = num("mem_total_kb") * 1024
	vm.FreeBytes = num("mem_free_kb") * 1024
	vm.AvailableBytes = num("mem_available_kb") * 1024
	vm.CachedBytes = num("mem_cached_kb") * 1024
	vm.AnonBytes = num("mem_anon_kb") * 1024
	vm.CPUs = int(num("cpus"))
	vm.UptimeSec = num("uptime_sec")
	return s, vm, nil
}

// Sorted orders the samples the way the table prints them: biggest first, with
// the ones that could not be measured last.
func Sorted(samples []Sample) []Sample {
	out := append([]Sample(nil), samples...)
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Err != nil) != (out[j].Err != nil) {
			return out[j].Err != nil
		}
		return out[i].MemoryBytes > out[j].MemoryBytes
	})
	return out
}

// FormatSize renders a byte count in binary units, truncating rather than
// rounding so a number never reads as larger than the thing it describes.
func FormatSize(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	value := b
	var idx int
	for idx = 0; idx < len(units)-1 && value >= unit*unit; idx++ {
		value /= unit
	}
	return fmt.Sprintf("%d.%d %s", value/unit, (value%unit)*10/unit, units[idx])
}

// Explanations of what the two methods do and do not cover, printed with the
// report so nobody has to guess why the columns do not add up.
const (
	cgroupNote    = "per-distribution figures come from each distribution's own cgroup, which accounts for it alone. They still do not sum to the VM total: page cache and kernel memory belong to the VM."
	processesNote = "this WSL has no per-distribution cgroup, so per-distribution memory is the resident set of the processes each distribution can see. Shared pages are counted once per process that maps them, and page cache and kernel memory are not counted at all."
)

// MethodNote explains how the numbers were arrived at.
func MethodNote(m Method) string {
	switch m {
	case MethodCgroup:
		return cgroupNote
	case MethodProcesses:
		return processesNote
	default:
		return ""
	}
}

// Render writes the human-readable report.
func Render(w io.Writer, r Report, rates map[string]*float64) {
	vm := r.VM
	fmt.Fprintf(w, "utility VM: %s of %s in use, %s free, %d processor(s)\n",
		FormatSize(vm.UsedBytes()), FormatSize(vm.TotalBytes), FormatSize(vm.FreeBytes), vm.CPUs)
	if vm.CachedBytes > 0 || vm.AnonBytes > 0 {
		fmt.Fprintf(w, "            %s page cache, which Windows can reclaim; %s anonymous, which it cannot\n",
			FormatSize(vm.CachedBytes), FormatSize(vm.AnonBytes))
	}
	fmt.Fprintln(w)

	samples := Sorted(r.Samples)
	if len(samples) == 0 {
		fmt.Fprintln(w, "no distributions are running")
		return
	}

	withCPU := len(rates) > 0
	headers := []string{"DISTRIBUTION", "MEMORY", "PROCESSES", "INIT"}
	if withCPU {
		headers = []string{"DISTRIBUTION", "MEMORY", "CPU", "PROCESSES", "INIT"}
	}
	t := table{headers: headers}
	for _, s := range samples {
		row := []string{s.Distro, FormatSize(s.MemoryBytes), strconv.Itoa(s.Processes), s.Init}
		if s.Err != nil {
			row = []string{s.Distro, "-", "-", "could not be measured"}
		}
		if withCPU {
			cpu := "-"
			if p := rates[s.Distro]; p != nil {
				cpu = fmt.Sprintf("%.1f%%", *p)
			}
			if s.Err != nil {
				row = []string{s.Distro, "-", "-", "-", "could not be measured"}
			} else {
				row = []string{s.Distro, FormatSize(s.MemoryBytes), cpu, strconv.Itoa(s.Processes), s.Init}
			}
		}
		t.rows = append(t.rows, row)
	}
	fmt.Fprint(w, t.String())

	line := fmt.Sprintf("%s attributed to distributions.", FormatSize(r.Attributed()))
	if note := MethodNote(r.Method()); note != "" {
		line += " " + note
	}
	fmt.Fprintf(w, "\n%s\n", line)

	for _, s := range samples {
		if s.Err != nil {
			fmt.Fprintf(w, "note: %s: %v\n", s.Distro, s.Err)
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
}

// JSON is the object printed for --json.
func JSON(r Report, rates map[string]*float64) map[string]any {
	distros := make([]map[string]any, 0, len(r.Samples))
	for _, s := range Sorted(r.Samples) {
		o := map[string]any{"distribution": s.Distro}
		if s.Err != nil {
			o["error"] = s.Err.Error()
			distros = append(distros, o)
			continue
		}
		o["memory_bytes"] = s.MemoryBytes
		o["processes"] = s.Processes
		o["init"] = s.Init
		o["method"] = string(s.Method)
		if s.AnonBytes != nil {
			o["anon_bytes"] = *s.AnonBytes
		}
		if s.CgroupPath != "" {
			o["cgroup_path"] = s.CgroupPath
		}
		if p := rates[s.Distro]; p != nil {
			o["cpu_percent"] = *p
		}
		distros = append(distros, o)
	}
	o := map[string]any{
		"vm": map[string]any{
			"total_bytes":     r.VM.TotalBytes,
			"free_bytes":      r.VM.FreeBytes,
			"available_bytes": r.VM.AvailableBytes,
			"used_bytes":      r.VM.UsedBytes(),
			"cached_bytes":    r.VM.CachedBytes,
			"anon_bytes":      r.VM.AnonBytes,
			"cpus":            r.VM.CPUs,
			"uptime_seconds":  r.VM.UptimeSec,
		},
		"distributions":    distros,
		"attributed_bytes": r.Attributed(),
	}
	if m := r.Method(); m != "" {
		o["method"] = string(m)
		o["note"] = MethodNote(m)
	}
	return o
}

// table is a small fixed-width renderer: left-aligned, two spaces between, and
// no padding after the last column so lines carry no trailing whitespace.
type table struct {
	headers []string
	rows    [][]string
}

func (t table) String() string {
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = len([]rune(h))
	}
	for _, row := range t.rows {
		for i, cell := range row {
			if i < len(widths) {
				if n := len([]rune(cell)); n > widths[i] {
					widths[i] = n
				}
			}
		}
	}
	var b strings.Builder
	write := func(cells []string) {
		last := len(cells) - 1
		for i, cell := range cells {
			b.WriteString(cell)
			if i != last {
				b.WriteString(strings.Repeat(" ", widths[i]-len([]rune(cell))+2))
			}
		}
		b.WriteByte('\n')
	}
	write(t.headers)
	for _, row := range t.rows {
		write(row)
	}
	return b.String()
}
