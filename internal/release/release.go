// Package release reads the published WSL releases from GitHub, with a cache.
//
// The version a check compares against ships inside the binary, refreshed by a
// weekly pull request. That is right for a tool that must work on a machine
// with no network and must never phone home unasked, and wrong for a binary
// somebody downloaded eight months ago: it will happily report a runtime as
// current when four releases have come out since.
//
// So the network is available and off by default. Asking for it is --online,
// what comes back is cached for a day, and everything here fails quietly: a
// rate limit, a proxy that swallows the request or a machine with no route at
// all leaves the embedded answer in place rather than turning a diagnosis into
// an error about the diagnosis.
package release

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Info is what a lookup produced.
type Info struct {
	// Stable is the newest published release that is not a pre-release.
	Stable string `json:"stable"`
	// Prerelease is the newest pre-release, when it is newer than Stable.
	// A pre-release older than the current stable is not news.
	Prerelease string `json:"prerelease,omitempty"`
	// FetchedAt is when this was read from GitHub, which is what the cache
	// ages against and what the source line reports.
	FetchedAt time.Time `json:"fetched_at"`
}

// Source describes where the answer came from, for the line under a finding.
func (i Info) Source() string {
	return "github releases " + i.FetchedAt.UTC().Format("2006-01-02")
}

// Fresh reports whether a cached lookup is still worth using.
func (i Info) Fresh(now time.Time, ttl time.Duration) bool {
	if i.Stable == "" || i.FetchedAt.IsZero() {
		return false
	}
	age := now.Sub(i.FetchedAt)
	// A clock that has gone backwards (a resumed VM, a corrected clock)
	// leaves a cache stamped in the future. Treat it as stale rather than
	// as valid forever.
	return age >= 0 && age < ttl
}

// ghRelease is the part of the GitHub release object this uses.
type ghRelease struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// Parse picks the newest stable and pre-release out of a releases listing.
//
// The listing is ordered newest first by the API, but that ordering is by
// creation date and a patch to an older branch can be published after a newer
// release. So the versions are compared rather than trusted in order.
func Parse(body []byte) (Info, error) {
	var rels []ghRelease
	if err := json.Unmarshal(body, &rels); err != nil {
		return Info{}, fmt.Errorf("release: parsing the releases listing: %w", err)
	}
	if len(rels) == 0 {
		return Info{}, fmt.Errorf("release: the releases listing was empty")
	}
	var stable, pre []string
	for _, r := range rels {
		if r.Draft {
			continue
		}
		v := Version(r.TagName)
		if v == "" {
			v = Version(r.Name)
		}
		if v == "" {
			continue
		}
		if r.Prerelease {
			pre = append(pre, v)
		} else {
			stable = append(stable, v)
		}
	}
	if len(stable) == 0 {
		return Info{}, fmt.Errorf("release: no published stable release in the listing")
	}
	sort.Slice(stable, func(i, j int) bool { return Compare(stable[i], stable[j]) > 0 })
	info := Info{Stable: stable[0]}
	if len(pre) > 0 {
		sort.Slice(pre, func(i, j int) bool { return Compare(pre[i], pre[j]) > 0 })
		// A pre-release older than the current stable is not news, and
		// showing it would read as an available upgrade that is behind.
		if Compare(pre[0], info.Stable) > 0 {
			info.Prerelease = pre[0]
		}
	}
	return info, nil
}

// Version pulls a version out of a tag or a release name.
//
// The WSL repository tags releases as "2.7.14", and has also used a "v" prefix
// and names like "WSL 2.6.1". Anything else is left alone: a tag this does not
// recognise is better ignored than guessed at.
func Version(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	if i := strings.LastIndex(s, " "); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return ""
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return ""
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return ""
			}
		}
	}
	return s
}

// Compare orders two dotted numeric versions: 1 if a is newer, -1 if b is, 0 if
// they are the same. A missing component counts as zero, so 2.7 and 2.7.0 are
// the same version.
func Compare(a, b string) int {
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(ap) || i < len(bp); i++ {
		x, y := part(ap, i), part(bp, i)
		switch {
		case x > y:
			return 1
		case x < y:
			return -1
		}
	}
	return 0
}

func part(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n := 0
	for _, r := range parts[i] {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}
