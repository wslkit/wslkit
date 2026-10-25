package collect

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/wslkit/wslkit/internal/env"
)

// Mirrored networking reserves a contiguous run of ports for the VM through
// HcnReserveGuestNetworkServicePortRange: 4096 of them, and from WSL 3.0.4 it
// halves that down to 512 before giving up (microsoft/WSL#41880). How HNS
// picks the run is not documented. What the report that led to the fix traced
// is that WinNAT's runtime exclusions fragment the dynamic range until no run
// that long is left (microsoft/WSL#41812), so those two tables are read.
//
// netsh labels its output in the display language, so nothing here reads a
// label: the dynamic range is the first two numbers after a colon, and an
// exclusion is any line that is two numbers and nothing else but the asterisk
// that marks one an administrator added.

var (
	colonNumber   = regexp.MustCompile(`:\s*(\d+)\s*$`)
	exclusionLine = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s*\*?\s*$`)
)

// parseDynamicPorts reads `netsh int ipv4 show dynamicport <proto>`.
func parseDynamicPorts(out string) (env.PortSpan, error) {
	var nums []int
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if m := colonNumber.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			nums = append(nums, n)
		}
	}
	if len(nums) < 2 || nums[0] < 1 || nums[1] < 1 || nums[0]+nums[1]-1 > 65535 {
		return env.PortSpan{}, errors.New("netsh printed no dynamic port range that could be read")
	}
	return env.PortSpan{Start: nums[0], End: nums[0] + nums[1] - 1}, nil
}

// parseExcludedPorts reads `netsh int ipv4 show excludedportrange protocol=<proto>`.
func parseExcludedPorts(out string) []env.PortSpan {
	var spans []env.PortSpan
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		m := exclusionLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		if a >= 1 && a <= b && b <= 65535 {
			spans = append(spans, env.PortSpan{Start: a, End: b})
		}
	}
	return spans
}
