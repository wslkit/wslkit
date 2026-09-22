// Package top implements `wslkit top`: what the WSL utility VM is using, and
// which distribution is responsible for it.
//
// How memory is attributed depends on the WSL version, and the difference is
// not cosmetic.
//
// From WSL 2.9 (measured absent on 2.7.13 and present on 2.9.11; there is no
// 2.8), mini_init creates /sys/fs/cgroup/wsl-user/distro-<pid> per
// distribution when wsl2.isolateDistroCgroup is on, which it is by default.
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
//
// The cgroup also carries what the process method cannot see at all: disk
// I/O, peak and swapped memory, OOM kills and pressure. Those are reported
// where the cgroup exists and left out where it does not, rather than
// approximated.
//
// Where the cgroup exists, so do cgroups that belong to no distribution: WSL's
// own non-distro group, and anything created at the VM's root, which is where
// Docker Engine without systemd puts its containers. Those are reported as
// rows of their own, because otherwise that memory is in no row at all.
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

// Kind is what a row of the report is.
type Kind string

const (
	// KindDistro is a WSL distribution.
	KindDistro Kind = "distribution"
	// KindWSL is WSL's own processes, wsl-user/non-distro.
	KindWSL Kind = "wsl"
	// KindCgroup is a cgroup at the VM's root that belongs to no
	// distribution, such as /docker.
	KindCgroup Kind = "cgroup"
)

// Pressure is PSI "some" avg10, in percent: the share of the last ten seconds
// in which at least one task was stalled waiting for the resource. Usage says
// a resource is used; pressure says it is short.
type Pressure struct {
	CPU    float64
	Memory float64
	IO     float64
}

// Rates are what changed between two samples, per second of the guest's own
// clock. Each is nil when it could not be computed.
type Rates struct {
	// CPU is a share of one processor, so two busy cores read 200.
	CPU *float64
	// Read and Write are disk bytes per second.
	Read  *float64
	Write *float64
}

// Sample is one measurement of one row: a distribution, or a cgroup that
// belongs to none.
type Sample struct {
	// Distro is the row's name: the distribution, or for a group row the
	// cgroup's.
	Distro string
	Kind   Kind
	Method Method
	// MemoryBytes is what the row is using.
	MemoryBytes uint64
	// AnonBytes is the part of that which automatic reclaim can never return
	// to Windows, so it is what keeps vmmem large. Only the cgroup method
	// can report it.
	AnonBytes *uint64

	// These come only from a cgroup, and are nil without one.
	PeakBytes  *uint64
	SwapBytes  *uint64
	OOMKills   *uint64
	PIDs       *uint64
	ReadBytes  *uint64
	WriteBytes *uint64
	Pressure   *Pressure

	// Processes is how many processes the distribution can see.
	Processes int
	// CPUUsec is CPU time used so far, which a rate is computed from.
	CPUUsec uint64
	// AtCsec is the guest's uptime, in hundredths of a second, when the
	// counters were read. A rate divides by this rather than by the
	// requested interval, because the sweep itself takes time: measured at
	// about 190 ms per sweep, which made a two-second rate read 9% high.
	AtCsec uint64
	// Init is the name of pid 1, which distinguishes a systemd distribution.
	Init string
	// CgroupPath is the node the numbers came from, when there was one.
	CgroupPath string
	// Err explains a distribution that could not be measured, so one that is
	// shutting down does not hide the rest.
	Err error

	Rates Rates
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

	// CPUUsec is busy time across every processor.
	CPUUsec uint64
	// NetRxBytes and NetTxBytes are what the VM's eth interfaces moved.
	// Distributions share one network namespace, so this is per VM only.
	NetRxBytes uint64
	NetTxBytes uint64
	HasNet     bool
	Pressure   *Pressure
	AtCsec     uint64

	Rates VMRates
}

// VMRates are the VM's own rates, nil where they could not be computed.
type VMRates struct {
	CPU   *float64
	NetRx *float64
	NetTx *float64
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
	VM      VM
	Samples []Sample
	// Groups are the cgroups that belong to no distribution. They exist only
	// where the per-distribution cgroup does: without it, their processes are
	// already inside some distribution's resident set.
	Groups   []Sample
	Interval time.Duration
	Notes    []string
	// SampledAt is when the sweep finished, by the Windows clock. With the
	// guest's uptime it gives the VM's boot time, which is how its vmmem is
	// found.
	SampledAt time.Time
	// StartedAt is when the sweep began, by the Windows clock.
	StartedAt time.Time
	// Host is the Windows side, when it was read.
	Host *Host
	// Sessions are the wslc sessions, read only when asked for; nil when not
	// asked, which is different from an empty list.
	Sessions []Session
}

// Attributed is the memory the distributions account for.
func (r Report) Attributed() uint64 {
	var total uint64
	for _, s := range r.Samples {
		total += s.MemoryBytes
	}
	return total
}

// GroupBytes is the memory the groups that belong to no distribution account
// for.
func (r Report) GroupBytes() uint64 {
	var total uint64
	for _, g := range r.Groups {
		total += g.MemoryBytes
	}
	return total
}

// HasRates says whether the report was measured over an interval.
func (r Report) HasRates() bool {
	return r.Interval > 0
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

// elapsed is the time between two samples: the guest's own clock where both
// carry it, and the requested interval otherwise.
func elapsed(beforeCsec, afterCsec uint64, interval time.Duration) time.Duration {
	if beforeCsec > 0 && afterCsec > beforeCsec {
		return time.Duration(afterCsec-beforeCsec) * 10 * time.Millisecond
	}
	return interval
}

// perSecond is how fast a counter moved, or nil when it went backwards,
// which means the thing it counts restarted.
func perSecond(before, after uint64, over time.Duration) *float64 {
	if over <= 0 || after < before {
		return nil
	}
	v := float64(after-before) / over.Seconds()
	return &v
}

// optPerSecond is perSecond for counters that may not have been measured.
func optPerSecond(before, after *uint64, over time.Duration) *float64 {
	if before == nil || after == nil {
		return nil
	}
	return perSecond(*before, *after, over)
}

// CPUPercent is what share of one processor a distribution used between two
// samples. It needs both, so it is nil for a single measurement.
func CPUPercent(before, after Sample, interval time.Duration) *float64 {
	over := elapsed(before.AtCsec, after.AtCsec, interval)
	if over.Microseconds() == 0 {
		return nil
	}
	p := perSecond(before.CPUUsec, after.CPUUsec, over)
	if p == nil {
		return nil
	}
	pct := *p / 1e6 * 100
	return &pct
}

// sampleRates fills in what changed between two samples of one row.
func sampleRates(before, after Sample, interval time.Duration) Rates {
	over := elapsed(before.AtCsec, after.AtCsec, interval)
	return Rates{
		CPU:   CPUPercent(before, after, interval),
		Read:  optPerSecond(before.ReadBytes, after.ReadBytes, over),
		Write: optPerSecond(before.WriteBytes, after.WriteBytes, over),
	}
}

// WithRates is after, with every rate filled in from what changed since
// before. A row that is new in after, or failed in either, gets none.
func WithRates(before, after Report, interval time.Duration) Report {
	out := after
	out.Interval = interval
	out.Samples = ratesFor(before.Samples, after.Samples, interval)
	out.Groups = ratesFor(before.Groups, after.Groups, interval)
	out.VM.Rates = vmRates(before.VM, after.VM, interval)
	if after.Sessions != nil {
		out.Sessions = sessionRates(before.Sessions, after.Sessions, interval)
	}
	return out
}

// vmRates is what changed across a whole VM between two samples.
func vmRates(b, a VM, interval time.Duration) VMRates {
	var r VMRates
	if b.TotalBytes == 0 || a.TotalBytes == 0 {
		return r
	}
	over := elapsed(b.AtCsec, a.AtCsec, interval)
	if p := perSecond(b.CPUUsec, a.CPUUsec, over); p != nil {
		pct := *p / 1e6 * 100
		r.CPU = &pct
	}
	if b.HasNet && a.HasNet {
		r.NetRx = perSecond(b.NetRxBytes, a.NetRxBytes, over)
		r.NetTx = perSecond(b.NetTxBytes, a.NetTxBytes, over)
	}
	return r
}

func ratesFor(before, after []Sample, interval time.Duration) []Sample {
	type key struct {
		kind Kind
		name string
	}
	prev := map[key]Sample{}
	for _, s := range before {
		prev[key{s.Kind, s.Distro}] = s
	}
	out := make([]Sample, len(after))
	for i, s := range after {
		s.Rates = Rates{}
		if b, ok := prev[key{s.Kind, s.Distro}]; ok && b.Err == nil && s.Err == nil {
			s.Rates = sampleRates(b, s, interval)
		}
		out[i] = s
	}
	return out
}

// SampleScript is what runs inside a distribution to measure it.
//
// It prefers the per-distribution cgroup and falls back to /proc, deciding
// inside the guest so the whole measurement is one round trip rather than a
// probe followed by a measurement.
//
// It reads /proc directly rather than calling ps, which is not on every
// distribution and formats differently where it is. Output is key=value lines,
// so parsing never depends on column positions or on a locale. A counter whose
// file does not exist prints no line at all, so a missing number is never read
// as zero.
//
// Line endings matter: this is fed to a shell inside the guest, and a carriage
// return turns a line into a command that does not exist.
const SampleScript = scriptFuncs + distroScript + vmScript

// SessionScript is what runs inside a wslc session VM: the VM, and one group
// per container. wslc runs its containers as Docker Engine does, one cgroup
// each under /docker, named by the full container ID. Right after the VM
// boots, before any container has run, /docker does not exist yet.
const SessionScript = scriptFuncs + `hz=$(getconf CLK_TCK 2>/dev/null || echo 100)
echo "clock_hz=$hz"
for d in /sys/fs/cgroup/docker/*/; do
  [ -d "$d" ] || continue
  group "g.$(basename "$d")." "${d%/}"
done
` + vmScript

// scriptFuncs are the shell functions both scripts share.
const scriptFuncs = `# /proc/uptime in hundredths of a second: the guest's own clock, which is
# what a rate divides by. It always carries two decimals.
clock() {
  awk '{split($1, a, "."); printf "%.0f\n", a[1] * 100 + a[2]}' /proc/uptime
}
# One cgroup's counters, each line prefixed with $1. A file that is missing
# prints nothing.
group() {
  gp=$1
  gd=$2
  [ -r "$gd/memory.current" ] || return 0
  echo "${gp}at_csec=$(clock)"
  echo "${gp}memory_bytes=$(cat "$gd/memory.current")"
  awk -v p="$gp" '$1=="anon"{print p "anon_bytes=" $2; exit}' "$gd/memory.stat" 2>/dev/null
  awk -v p="$gp" '$1=="usage_usec"{print p "cpu_usec=" $2; exit}' "$gd/cpu.stat" 2>/dev/null
  [ -r "$gd/memory.peak" ] && echo "${gp}peak_bytes=$(cat "$gd/memory.peak")"
  [ -r "$gd/memory.swap.current" ] && echo "${gp}swap_bytes=$(cat "$gd/memory.swap.current")"
  [ -r "$gd/pids.current" ] && echo "${gp}pids=$(cat "$gd/pids.current")"
  awk -v p="$gp" '$1=="oom_kill"{print p "oom_kills=" $2; exit}' "$gd/memory.events" 2>/dev/null
  # io.stat is one line per device; an empty file means no I/O yet, which is
  # a real zero.
  if [ -r "$gd/io.stat" ]; then
    awk -v p="$gp" '{for (i = 2; i <= NF; i++) {split($i, a, "="); if (a[1] == "rbytes") r += a[2]; if (a[1] == "wbytes") w += a[2]}} END {printf "%sread_bytes=%.0f\n%swrite_bytes=%.0f\n", p, r, p, w}' "$gd/io.stat"
  fi
  for k in cpu memory io; do
    awk -v p="$gp" -v k="$k" '$1=="some"{split($2, a, "="); print p "psi_" k "=" a[2]; exit}' "$gd/$k.pressure" 2>/dev/null
  done
}
`

// distroScript measures the distribution it runs in.
const distroScript = `# This shell's own cgroup, not pid 1's. Measured on WSL 2.9.11: a systemd
# distribution puts pid 1 in /wsl-user/distro-N/systemd/init.scope, but a
# distribution without systemd reports 0::/ for pid 1 and the distro node only
# for its own processes. Reading pid 1 therefore finds nothing on exactly the
# distributions that are cheapest to measure, and falls back to summing /proc
# for no reason.
node=$(awk -F: '{print $3}' /proc/self/cgroup 2>/dev/null | head -1)
case "$node" in
  /wsl-user/distro-*) ;;
  *) node=$(awk -F: '{print $3}' /proc/1/cgroup 2>/dev/null | head -1) ;;
esac
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
hz=$(getconf CLK_TCK 2>/dev/null || echo 100)
echo "clock_hz=$hz"
if [ -n "$node" ] && [ -r "$dir/memory.current" ]; then
  echo "method=cgroup"
  echo "cgroup_path=$node"
  group "" "$dir"
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
  echo "at_csec=$(clock)"
  echo "memory_kb=$rss"
  echo "cpu_ticks=$ticks"
fi
echo "processes=$(ls -d /proc/[0-9]* 2>/dev/null | wc -l)"
echo "init=$(cat /proc/1/comm 2>/dev/null)"
# Cgroups that belong to no distribution. Only where wsl-user exists: without
# it every distribution shares the root, and whatever sits in these groups is
# already in some distribution's resident set.
if [ -n "$node" ]; then
  group "g.wsl." /sys/fs/cgroup/wsl-user/non-distro
  for d in /sys/fs/cgroup/*/; do
    n=$(basename "$d")
    [ "$n" = wsl-user ] && continue
    group "g.$n." "${d%/}"
  done
fi
`

// vmScript measures the VM as a whole.
const vmScript = `echo "vm_at_csec=$(clock)"
echo "mem_total_kb=$(awk '/^MemTotal:/{print $2}' /proc/meminfo)"
echo "mem_free_kb=$(awk '/^MemFree:/{print $2}' /proc/meminfo)"
echo "mem_available_kb=$(awk '/^MemAvailable:/{print $2}' /proc/meminfo)"
echo "mem_cached_kb=$(awk '/^Cached:/{print $2; exit}' /proc/meminfo)"
echo "mem_anon_kb=$(awk '/^AnonPages:/{print $2}' /proc/meminfo)"
echo "cpus=$(awk '/^processor/{n++} END{print n+0}' /proc/cpuinfo)"
echo "uptime_sec=$(awk '{print int($1)}' /proc/uptime)"
# user nice system idle iowait irq softirq steal: busy is all but idle and
# iowait.
awk '$1=="cpu"{printf "vm_cpu_ticks=%.0f\n", $2 + $3 + $4 + $7 + $8 + $9; exit}' /proc/stat
# Only eth interfaces: docker0, bridges and veths carry the same packets a
# second time on their way to eth0.
awk -F'[: ]+' '{sub(/^ +/, "")} $1 ~ /^eth[0-9]+$/ {rx += $2; tx += $10; n++} END {if (n) printf "net_rx_bytes=%.0f\nnet_tx_bytes=%.0f\n", rx, tx}' /proc/net/dev
for k in cpu memory io; do
  awk -v k="$k" '$1=="some"{split($2, a, "="); print "vm_psi_" k "=" a[2]; exit}' "/proc/pressure/$k" 2>/dev/null
done
`

// fields splits key=value output into the distribution's own keys and those
// of each group, which carry a "g.<name>." prefix.
func fields(out string) (map[string]string, map[string]map[string]string, []string) {
	own := map[string]string{}
	groups := map[string]map[string]string{}
	var order []string
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key, val := line[:eq], strings.TrimSpace(line[eq+1:])
		if rest, ok := strings.CutPrefix(key, "g."); ok {
			// Keys carry no dots; a cgroup name may, so split on the last.
			dot := strings.LastIndexByte(rest, '.')
			if dot <= 0 {
				continue
			}
			name, k := rest[:dot], rest[dot+1:]
			if groups[name] == nil {
				groups[name] = map[string]string{}
				order = append(order, name)
			}
			groups[name][k] = val
			continue
		}
		own[key] = val
	}
	return own, groups, order
}

// reader turns one set of fields into numbers.
type reader map[string]string

func (f reader) num(key string) uint64 {
	v, err := strconv.ParseUint(f[key], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// opt is a number that may not have been measured: nil when the line is
// missing or unreadable, never a zero standing in for "unknown".
func (f reader) opt(key string) *uint64 {
	s, ok := f[key]
	if !ok || s == "" {
		return nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

func (f reader) pressure(prefix string) *Pressure {
	var p Pressure
	var seen bool
	for k, dst := range map[string]*float64{"cpu": &p.CPU, "memory": &p.Memory, "io": &p.IO} {
		if v, err := strconv.ParseFloat(f[prefix+k], 64); err == nil {
			*dst = v
			seen = true
		}
	}
	if !seen {
		return nil
	}
	return &p
}

// cgroupCounters reads what group() in the script printed.
func (f reader) cgroupCounters(s *Sample) {
	s.MemoryBytes = f.num("memory_bytes")
	s.AnonBytes = f.opt("anon_bytes")
	s.CPUUsec = f.num("cpu_usec")
	s.AtCsec = f.num("at_csec")
	s.PeakBytes = f.opt("peak_bytes")
	s.SwapBytes = f.opt("swap_bytes")
	s.OOMKills = f.opt("oom_kills")
	s.PIDs = f.opt("pids")
	s.ReadBytes = f.opt("read_bytes")
	s.WriteBytes = f.opt("write_bytes")
	s.Pressure = f.pressure("psi_")
}

// ParseSample reads what SampleScript printed.
func ParseSample(distro, out string) (Sample, VM, error) {
	s := Sample{Distro: distro, Kind: KindDistro}
	var vm VM
	own, _, _ := fields(out)
	if len(own) == 0 {
		return s, vm, fmt.Errorf("top: %s printed nothing that could be read", distro)
	}
	f := reader(own)

	s.Processes = int(f.num("processes"))
	s.Init = f["init"]
	// Process CPU comes out of /proc in clock ticks, whose rate is not
	// guaranteed to be 100, so the distribution reports its own.
	hz := f.num("clock_hz")
	if hz == 0 {
		hz = 100
	}

	switch Method(f["method"]) {
	case MethodCgroup:
		s.Method = MethodCgroup
		s.CgroupPath = f["cgroup_path"]
		f.cgroupCounters(&s)
	case MethodProcesses:
		s.Method = MethodProcesses
		s.MemoryBytes = f.num("memory_kb") * 1024
		s.CPUUsec = f.num("cpu_ticks") * 1_000_000 / hz
		s.AtCsec = f.num("at_csec")
	default:
		return s, vm, fmt.Errorf("top: %s did not say how it measured itself", distro)
	}
	return s, f.vm(hz), nil
}

// vm reads what vmScript printed.
func (f reader) vm(hz uint64) VM {
	var vm VM
	vm.TotalBytes = f.num("mem_total_kb") * 1024
	vm.FreeBytes = f.num("mem_free_kb") * 1024
	vm.AvailableBytes = f.num("mem_available_kb") * 1024
	vm.CachedBytes = f.num("mem_cached_kb") * 1024
	vm.AnonBytes = f.num("mem_anon_kb") * 1024
	vm.CPUs = int(f.num("cpus"))
	vm.UptimeSec = f.num("uptime_sec")
	vm.CPUUsec = f.num("vm_cpu_ticks") * 1_000_000 / hz
	vm.AtCsec = f.num("vm_at_csec")
	if rx, tx := f.opt("net_rx_bytes"), f.opt("net_tx_bytes"); rx != nil && tx != nil {
		vm.NetRxBytes, vm.NetTxBytes, vm.HasNet = *rx, *tx, true
	}
	vm.Pressure = f.pressure("vm_psi_")
	return vm
}

// ParseGroups reads the cgroups that belong to no distribution out of what
// SampleScript printed. Every distribution reports the same ones, since they
// share one kernel.
func ParseGroups(out string) []Sample {
	_, groups, order := fields(out)
	var list []Sample
	for _, name := range order {
		f := reader(groups[name])
		if _, ok := f["memory_bytes"]; !ok {
			continue
		}
		g := Sample{Distro: name, Kind: KindCgroup, Method: MethodCgroup, CgroupPath: "/" + name}
		if name == "wsl" {
			g.Kind, g.CgroupPath = KindWSL, "/wsl-user/non-distro"
		}
		f.cgroupCounters(&g)
		list = append(list, g)
	}
	return list
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

// formatRate renders bytes per second, or a dash when it was not measured.
func formatRate(p *float64) string {
	if p == nil {
		return "-"
	}
	return FormatSize(uint64(*p)) + "/s"
}

func formatPercent(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", *p)
}

// Explanations of what the two methods do and do not cover, printed with the
// report so nobody has to guess why the columns do not add up.
const (
	cgroupNote    = "per-distribution figures come from each distribution's own cgroup, which accounts for it alone. They still do not sum to the VM total: page cache and kernel memory belong to the VM."
	processesNote = "this WSL has no per-distribution cgroup, so per-distribution memory is the resident set of the processes each distribution can see. Shared pages are counted once per process that maps them, and page cache and kernel memory are not counted at all."
	// groupsNote explains the rows that are not distributions.
	groupsNote = "wsl is WSL's own processes. A cgroup row sits at the VM's root and belongs to no distribution: /docker is where Docker Engine without systemd puts its containers."
	// cgroupOnlyNote says why the I/O and pressure columns are missing.
	cgroupOnlyNote = "disk I/O, PIDs and pressure per distribution need the per-distribution cgroup, which arrived in WSL 2.9, so they are not shown."
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
//
// Each VM gets a section of its own under a rule naming it, holding only its
// totals and its table. What explains the numbers comes once, at the end:
// with two VMs on screen, explanations between them made one section run into
// the next.
func Render(w io.Writer, r Report) {
	fmt.Fprintln(w, rule("utility VM"))
	if len(r.Samples) == 0 {
		fmt.Fprintln(w, "no distributions are running, so it is not up")
		renderOthers(w, r.Host)
	} else {
		renderVM(w, r.VM, r.Host)
		fmt.Fprintln(w)
		rows := append(Sorted(r.Samples), Sorted(r.Groups)...)
		fmt.Fprint(w, rowsTable(rows, r.Method() == MethodCgroup, r.HasRates()).String())
		line := fmt.Sprintf("%s attributed to distributions", FormatSize(r.Attributed()))
		if len(r.Groups) > 0 {
			line += fmt.Sprintf(", %s to groups that belong to none", FormatSize(r.GroupBytes()))
		}
		fmt.Fprintf(w, "\n%s.\n", line)
	}
	renderSessions(w, r)
	renderNotes(w, r)
}

// renderNotes writes what explains the numbers, and then everything that
// went wrong or needs saying about one row.
func renderNotes(w io.Writer, r Report) {
	var explain, notes []string
	if len(r.Samples) > 0 {
		if n := MethodNote(r.Method()); n != "" {
			explain = append(explain, upperFirst(n))
		}
	}
	if len(r.Groups) > 0 {
		explain = append(explain, groupsNote)
	}
	if r.Method() == MethodProcesses {
		explain = append(explain, upperFirst(cgroupOnlyNote))
	}
	if len(r.Sessions) > 0 {
		explain = append(explain, sessionsNote)
	}

	for _, s := range append(Sorted(r.Samples), Sorted(r.Groups)...) {
		notes = append(notes, rowNotes(s)...)
	}
	for _, s := range r.Sessions {
		if s.Err != nil {
			notes = append(notes, fmt.Sprintf("wslc session %s could not be measured: %v", s.Name, s.Err))
			continue
		}
		if s.Started {
			notes = append(notes, fmt.Sprintf("wslc session %s: its VM was not running, and asking wslc started it. It stops again once idle.", s.Name))
		}
		for _, c := range s.Containers {
			notes = append(notes, rowNotes(c)...)
		}
	}
	if r.Host != nil && r.Host.Err != nil {
		notes = append(notes, fmt.Sprintf("what Windows charges each VM could not be read: %v", r.Host.Err))
	}
	notes = append(notes, r.Notes...)

	if len(explain) == 0 && len(notes) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", rule("notes"))
	for _, e := range explain {
		fmt.Fprintln(w, e)
	}
	for _, n := range notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
}

// rowNotes is what needs saying about one row.
func rowNotes(s Sample) []string {
	var out []string
	if s.Err != nil {
		out = append(out, fmt.Sprintf("%s: %v", s.Distro, s.Err))
	}
	if s.OOMKills != nil && *s.OOMKills > 0 {
		out = append(out, fmt.Sprintf("%s: the kernel has killed %d process(es) for running out of memory", s.Distro, *s.OOMKills))
	}
	return out
}

// ruleWidth is how wide a section rule is drawn: narrow enough for an
// 80-column window.
const ruleWidth = 78

// rule is a section's heading: its name on a horizontal line.
func rule(label string) string {
	head := "── " + label + " "
	fill := ruleWidth - len([]rune(head))
	if fill < 3 {
		fill = 3
	}
	return head + strings.Repeat("─", fill)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// renderVM writes a VM's totals: what its kernel says, and what Windows
// charges for it. The section's rule has already named it.
func renderVM(w io.Writer, vm VM, host *Host) {
	head := fmt.Sprintf("%s of %s in use, %s free, %d CPUs",
		FormatSize(vm.UsedBytes()), FormatSize(vm.TotalBytes), FormatSize(vm.FreeBytes), vm.CPUs)
	if vm.Rates.CPU != nil {
		head += ", CPU " + formatPercent(vm.Rates.CPU)
	}
	fmt.Fprintln(w, head)
	if vm.CachedBytes > 0 || vm.AnonBytes > 0 {
		fmt.Fprintf(w, "%s page cache, which Windows can reclaim; %s anonymous, which it cannot\n",
			FormatSize(vm.CachedBytes), FormatSize(vm.AnonBytes))
	}
	if p := vm.Pressure; p != nil {
		fmt.Fprintf(w, "stalled over the last 10 s: memory %.1f%%, I/O %.1f%%, CPU %.1f%%\n", p.Memory, p.IO, p.CPU)
	}
	if vm.Rates.NetRx != nil && vm.Rates.NetTx != nil {
		fmt.Fprintf(w, "network: %s in, %s out\n", formatRate(vm.Rates.NetRx), formatRate(vm.Rates.NetTx))
	}
	if host != nil && host.Utility != nil {
		fmt.Fprintf(w, "Windows charges it %s (vmmem pid %d)\n", FormatSize(host.Utility.WorkingSetBytes), host.Utility.PID)
	}
	renderOthers(w, host)
}

// rowsTable lays out rows with the columns their measurement supports.
func rowsTable(rows []Sample, withCgroup, withRates bool) table {
	var withSwap, withInit bool
	for _, s := range rows {
		if s.SwapBytes != nil && *s.SwapBytes > 0 {
			withSwap = true
		}
		if s.Kind == KindDistro && s.Init != "" {
			withInit = true
		}
	}

	headers := []string{"NAME", "KIND", "MEMORY"}
	if withCgroup {
		headers = append(headers, "ANON")
	}
	if withSwap {
		headers = append(headers, "SWAP")
	}
	if withRates {
		headers = append(headers, "CPU")
		if withCgroup {
			headers = append(headers, "READ", "WRITE")
		}
	}
	if withCgroup {
		headers = append(headers, "PIDS", "STALL MEM/IO")
	} else {
		headers = append(headers, "PROCESSES")
	}
	if withInit {
		headers = append(headers, "INIT")
	}

	t := table{headers: headers}
	for _, s := range rows {
		kind := "distro"
		if s.Kind != KindDistro {
			kind = string(s.Kind)
		}
		if s.Err != nil {
			row := []string{s.Distro, kind}
			for range headers[3:] {
				row = append(row, "-")
			}
			t.rows = append(t.rows, append(row, "could not be measured"))
			continue
		}
		row := []string{s.Distro, kind, FormatSize(s.MemoryBytes)}
		if withCgroup {
			row = append(row, optSize(s.AnonBytes))
		}
		if withSwap {
			row = append(row, optSize(s.SwapBytes))
		}
		if withRates {
			row = append(row, formatPercent(s.Rates.CPU))
			if withCgroup {
				row = append(row, formatRate(s.Rates.Read), formatRate(s.Rates.Write))
			}
		}
		if withCgroup {
			pids, stall := "-", "-"
			if s.PIDs != nil {
				pids = strconv.FormatUint(*s.PIDs, 10)
			}
			if p := s.Pressure; p != nil {
				stall = fmt.Sprintf("%.1f%% / %.1f%%", p.Memory, p.IO)
			}
			row = append(row, pids, stall)
		} else {
			row = append(row, strconv.Itoa(s.Processes))
		}
		if withInit {
			init := ""
			if s.Kind == KindDistro {
				init = s.Init
			}
			row = append(row, init)
		}
		t.rows = append(t.rows, row)
	}
	return t
}

// renderOthers names the VMs that are not the utility VM. Which VM each one is
// cannot be told without elevation, so it says what they could be.
func renderOthers(w io.Writer, h *Host) {
	if h == nil || len(h.Others) == 0 {
		return
	}
	var total uint64
	for _, v := range h.Others {
		total += v.WorkingSetBytes
	}
	fmt.Fprintf(w, "%d other VM(s) hold %s more: a wslc session, or any other Hyper-V VM\n", len(h.Others), FormatSize(total))
}

func optSize(p *uint64) string {
	if p == nil {
		return "-"
	}
	return FormatSize(*p)
}

// sampleJSON is one row of the --json output.
func sampleJSON(s Sample, nameKey string) map[string]any {
	o := map[string]any{nameKey: s.Distro}
	if s.Err != nil {
		o["error"] = s.Err.Error()
		return o
	}
	o["memory_bytes"] = s.MemoryBytes
	o["method"] = string(s.Method)
	if s.AnonBytes != nil {
		o["anon_bytes"] = *s.AnonBytes
	}
	if s.CgroupPath != "" {
		o["cgroup_path"] = s.CgroupPath
	}
	for k, v := range map[string]*uint64{
		"peak_bytes":  s.PeakBytes,
		"swap_bytes":  s.SwapBytes,
		"oom_kills":   s.OOMKills,
		"pids":        s.PIDs,
		"read_bytes":  s.ReadBytes,
		"write_bytes": s.WriteBytes,
	} {
		if v != nil {
			o[k] = *v
		}
	}
	if p := s.Pressure; p != nil {
		o["pressure"] = pressureJSON(*p)
	}
	if p := s.Rates.CPU; p != nil {
		o["cpu_percent"] = *p
	}
	if p := s.Rates.Read; p != nil {
		o["read_bytes_per_second"] = *p
	}
	if p := s.Rates.Write; p != nil {
		o["write_bytes_per_second"] = *p
	}
	return o
}

// HostJSON is the Windows side of --json, or nil when it was not read.
func HostJSON(h *Host) map[string]any {
	if h == nil {
		return nil
	}
	if h.Err != nil {
		return map[string]any{"error": h.Err.Error()}
	}
	others := make([]map[string]any, 0, len(h.Others))
	for _, v := range h.Others {
		others = append(others, hostVMJSON(v))
	}
	o := map[string]any{"other_vms": others}
	if h.Utility != nil {
		o["utility_vm"] = hostVMJSON(*h.Utility)
	}
	return o
}

func hostVMJSON(v HostVM) map[string]any {
	return map[string]any{"pid": v.PID, "working_set_bytes": v.WorkingSetBytes, "created": v.Created.UTC().Format(time.RFC3339)}
}

func pressureJSON(p Pressure) map[string]any {
	return map[string]any{"cpu": p.CPU, "memory": p.Memory, "io": p.IO}
}

// vmJSON is a VM's block of --json.
func vmJSON(v VM) map[string]any {
	vm := map[string]any{
		"total_bytes":     v.TotalBytes,
		"free_bytes":      v.FreeBytes,
		"available_bytes": v.AvailableBytes,
		"used_bytes":      v.UsedBytes(),
		"cached_bytes":    v.CachedBytes,
		"anon_bytes":      v.AnonBytes,
		"cpus":            v.CPUs,
		"uptime_seconds":  v.UptimeSec,
	}
	if v.HasNet {
		vm["net_rx_bytes"] = v.NetRxBytes
		vm["net_tx_bytes"] = v.NetTxBytes
	}
	if p := v.Pressure; p != nil {
		vm["pressure"] = pressureJSON(*p)
	}
	if p := v.Rates.CPU; p != nil {
		vm["cpu_percent"] = *p
	}
	if p := v.Rates.NetRx; p != nil {
		vm["net_rx_bytes_per_second"] = *p
	}
	if p := v.Rates.NetTx; p != nil {
		vm["net_tx_bytes_per_second"] = *p
	}
	return vm
}

// JSON is the object printed for --json.
func JSON(r Report) map[string]any {
	if len(r.Samples) == 0 {
		o := map[string]any{"distributions": []any{}, "note": "no distributions are running"}
		// Another VM, a wslc session say, can be holding memory with no
		// distribution up at all.
		if h := HostJSON(r.Host); h != nil {
			o["host"] = h
		}
		if r.Sessions != nil {
			o["wslc_sessions"] = sessionsJSON(r.Sessions)
		}
		if len(r.Notes) > 0 {
			o["notes"] = r.Notes
		}
		return o
	}
	distros := make([]map[string]any, 0, len(r.Samples))
	for _, s := range Sorted(r.Samples) {
		o := sampleJSON(s, "distribution")
		if s.Err == nil {
			o["processes"] = s.Processes
			o["init"] = s.Init
		}
		distros = append(distros, o)
	}
	groups := make([]map[string]any, 0, len(r.Groups))
	for _, g := range Sorted(r.Groups) {
		o := sampleJSON(g, "name")
		o["kind"] = string(g.Kind)
		groups = append(groups, o)
	}
	o := map[string]any{
		"vm":               vmJSON(r.VM),
		"distributions":    distros,
		"groups":           groups,
		"attributed_bytes": r.Attributed(),
		"group_bytes":      r.GroupBytes(),
	}
	if r.Interval > 0 {
		o["interval_seconds"] = r.Interval.Seconds()
	}
	if h := HostJSON(r.Host); h != nil {
		o["host"] = h
	}
	// Present only when --wslc asked for it, so its absence says "not
	// asked" and an empty list says "asked, and there are none".
	if r.Sessions != nil {
		o["wslc_sessions"] = sessionsJSON(r.Sessions)
	}
	if len(r.Notes) > 0 {
		o["notes"] = r.Notes
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
		// A trailing empty cell would leave the padding before it behind.
		for len(cells) > 0 && cells[len(cells)-1] == "" {
			cells = cells[:len(cells)-1]
		}
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
