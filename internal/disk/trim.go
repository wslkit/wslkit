package disk

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// fstrim is addressed by absolute path because wsl.exe does not search PATH for
// the program it is asked to exec. /sbin works on both merged-usr distributions
// and the busybox layout.
const fstrimPath = "/sbin/fstrim"

// trimMount is the only mountpoint trimmed. `fstrim -a` is not used: busybox
// rejects it, and the root filesystem is the one inside the disk being
// reclaimed.
const trimMount = "/"

// DefaultTrimTimeout bounds the trim. On a large disk with a lot to discard it
// genuinely takes minutes.
const DefaultTrimTimeout = 10 * time.Minute

// TrimmedBytesAreMisleading explains the number fstrim prints.
//
// It is the size of the free extent it offered to the device, not space that
// came back. On a 1 TiB virtual disk holding 12 GiB, fstrim reports about a
// tebibyte every time it runs. Printing that figure without this sentence next
// to it reads as a promise the tool cannot keep.
const TrimmedBytesAreMisleading = "that figure is the free extent of the disk, not space reclaimed: compaction is what shrinks the file"

// TrimResult is the outcome of a trim.
type TrimResult struct {
	Distro string
	// Bytes is what fstrim reported, when it said anything. See
	// TrimmedBytesAreMisleading for what it means.
	Bytes *uint64
	// UsedFallback records that the verbose flag was rejected and the plain
	// form was used instead, which is why no figure came back.
	UsedFallback bool
}

// PlanTrim describes what a trim will do.
func PlanTrim(r Registration) (Plan, error) {
	p := Plan{Subject: r.Name, SubjectKey: "distribution"}
	if r.Version != 2 {
		return p, fmt.Errorf("%w: %s stores its files directly on NTFS, so there is no filesystem inside a disk to trim; convert it with wsl --set-version %s 2", ErrNotWSL2, r.Name, r.Name)
	}
	p.Add("run %s %s in %s", fstrimPath, trimMount, r.Name)
	p.Warn("this starts the distribution if it is stopped, and leaves it running",
		"stop it afterwards with wsl --terminate "+r.Name)
	return p, nil
}

// Trim asks the guest filesystem to release the blocks it is no longer using.
//
// This is what makes a later compaction worth running: without it the VHDX
// still holds the stale data and compaction reclaims almost nothing.
func Trim(ctx context.Context, e Env, r Registration, timeout time.Duration) (TrimResult, error) {
	if timeout == 0 {
		timeout = DefaultTrimTimeout
	}
	res := TrimResult{Distro: r.Name}

	out, err := e.Host.RunAsRoot(ctx, r.Name, []string{fstrimPath, "-v", trimMount}, timeout)
	if err != nil {
		return res, fmt.Errorf("disk: running fstrim in %s: %w", r.Name, err)
	}
	if out.ExitCode != 0 {
		// busybox fstrim has no -v. Both spellings of its complaint have
		// been seen, so both are matched; any other failure is real and
		// is not retried.
		if rejectsVerbose(out.Stderr) {
			res.UsedFallback = true
			out, err = e.Host.RunAsRoot(ctx, r.Name, []string{fstrimPath, trimMount}, timeout)
			if err != nil {
				return res, fmt.Errorf("disk: running fstrim in %s: %w", r.Name, err)
			}
		}
		if out.ExitCode != 0 {
			return res, fmt.Errorf("disk: fstrim exited %d in %s: %s", out.ExitCode, r.Name, oneLine(out.Stderr))
		}
	}
	if n, ok := ParseTrimmedBytes(out.Stdout); ok {
		res.Bytes = &n
	}
	return res, nil
}

func rejectsVerbose(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "unrecognized option") || strings.Contains(s, "invalid option")
}

// oneLine flattens a guest message so it fits on one line of output.
func oneLine(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// ParseTrimmedBytes reads the byte count out of `fstrim -v` output.
//
// The line looks like: "/: 1004.8 GiB (1078939029504 bytes) trimmed". The
// figure is found by anchoring on the last "bytes" and reading the digits
// immediately before it, which survives the wording differing between
// util-linux versions.
func ParseTrimmedBytes(out string) (uint64, bool) {
	idx := strings.LastIndex(out, "bytes")
	if idx < 0 {
		return 0, false
	}
	end := idx
	for end > 0 && out[end-1] == ' ' {
		end--
	}
	start := end
	for start > 0 && out[start-1] >= '0' && out[start-1] <= '9' {
		start--
	}
	if start == end {
		return 0, false
	}
	n, err := strconv.ParseUint(out[start:end], 10, 64)
	if err != nil {
		// Longer than a uint64 can hold: no figure rather than a wrong one.
		return 0, false
	}
	return n, true
}

// RenderTrim writes the human-readable result.
func RenderTrim(w io.Writer, res TrimResult) {
	if res.Bytes != nil {
		fmt.Fprintf(w, "%s: trimmed. fstrim reported %s.\n", res.Distro, FormatSize(*res.Bytes))
		fmt.Fprintf(w, "%s\n", TrimmedBytesAreMisleading)
	} else {
		fmt.Fprintf(w, "%s: trimmed. fstrim did not say how much.\n", res.Distro)
	}
	fmt.Fprintf(w, "run `wslkit disk compact %s` to shrink the file itself\n", res.Distro)
}

// TrimJSON is the object printed for --json.
func TrimJSON(res TrimResult) map[string]any {
	o := map[string]any{
		"distribution": res.Distro,
		"trimmed":      true,
		"note":         TrimmedBytesAreMisleading,
	}
	if res.Bytes != nil {
		// Named for what it is. Calling it bytes_freed would be wrong.
		o["bytes_offered"] = *res.Bytes
	}
	return o
}
