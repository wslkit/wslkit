package release

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const listing = `[
  {"tag_name": "2.7.15", "name": "WSL 2.7.15", "draft": false, "prerelease": true},
  {"tag_name": "2.7.14", "name": "WSL 2.7.14", "draft": false, "prerelease": false},
  {"tag_name": "2.8.0",  "name": "WSL 2.8.0",  "draft": true,  "prerelease": false},
  {"tag_name": "2.7.13", "name": "WSL 2.7.13", "draft": false, "prerelease": false}
]`

func TestParse(t *testing.T) {
	info, err := Parse([]byte(listing))
	if err != nil {
		t.Fatal(err)
	}
	if info.Stable != "2.7.14" {
		t.Errorf("stable = %q, want 2.7.14", info.Stable)
	}
	if info.Prerelease != "2.7.15" {
		t.Errorf("prerelease = %q, want 2.7.15", info.Prerelease)
	}
}

// A draft is not published. Reporting one as the latest release would send
// everyone looking for a download that is not there.
func TestParseIgnoresDrafts(t *testing.T) {
	info, _ := Parse([]byte(`[{"tag_name":"9.9.9","draft":true},{"tag_name":"2.7.14"}]`))
	if info.Stable != "2.7.14" {
		t.Errorf("stable = %q", info.Stable)
	}
}

// The API orders by creation date, so a patch to an older branch published
// after a newer release comes first. The version decides, not the order.
func TestParsePicksTheNewestNotTheFirst(t *testing.T) {
	info, _ := Parse([]byte(`[{"tag_name":"2.6.9"},{"tag_name":"2.7.14"}]`))
	if info.Stable != "2.7.14" {
		t.Errorf("stable = %q, want the newer version", info.Stable)
	}
}

// A pre-release behind the stable release is not news, and showing it would
// read as an upgrade that is backwards.
func TestParseHidesAnOldPrerelease(t *testing.T) {
	info, _ := Parse([]byte(`[{"tag_name":"2.7.14"},{"tag_name":"2.6.0","prerelease":true}]`))
	if info.Prerelease != "" {
		t.Errorf("prerelease = %q, want none", info.Prerelease)
	}
}

func TestParseRejectsRubbish(t *testing.T) {
	for _, body := range []string{``, `{}`, `[]`, `[{"tag_name":"not-a-version"}]`, `[{"tag_name":"2.7.15","prerelease":true}]`} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("Parse(%q) should have failed", body)
		}
	}
}

func TestVersion(t *testing.T) {
	for in, want := range map[string]string{
		"2.7.14":     "2.7.14",
		"v2.7.14":    "2.7.14",
		"WSL 2.7.14": "2.7.14",
		"2.7":        "2.7",
		"":           "",
		"latest":     "",
		"2.7.x":      "",
		"2..7":       "",
	} {
		if got := Version(in); got != want {
			t.Errorf("Version(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompare(t *testing.T) {
	if Compare("2.7.14", "2.7.9") <= 0 {
		t.Error("2.7.14 is newer than 2.7.9; this is not a string comparison")
	}
	if Compare("2.7", "2.7.0") != 0 {
		t.Error("a missing component counts as zero")
	}
	if Compare("2.7.0", "2.8.0") >= 0 {
		t.Error("2.8.0 is newer")
	}
}

// roundTrip answers a request without a network.
type roundTrip func(*http.Request) *http.Response

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r), nil }

func reply(status int, body string, header http.Header) roundTrip {
	return func(*http.Request) *http.Response {
		if header == nil {
			header = http.Header{}
		}
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     header,
		}
	}
}

func TestFetch(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	info, err := Fetch(context.Background(), reply(200, listing, nil), now)
	if err != nil {
		t.Fatal(err)
	}
	if info.Stable != "2.7.14" || !info.FetchedAt.Equal(now) {
		t.Fatalf("info = %+v", info)
	}
	if info.Source() != "github releases 2026-09-13" {
		t.Errorf("source = %q", info.Source())
	}
}

// The rate limit is the failure people will actually hit, from an office
// sharing one address. It has to say so, and say when it clears.
func TestFetchRateLimited(t *testing.T) {
	h := http.Header{}
	h.Set("X-RateLimit-Reset", "1789000000")
	_, err := Fetch(context.Background(), reply(403, "", h), time.Now())
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "until") {
		t.Errorf("the error should say when it clears: %v", err)
	}
}

func TestFresh(t *testing.T) {
	now := time.Now()
	if (Info{}).Fresh(now, TTL) {
		t.Error("an empty lookup is never fresh")
	}
	if !(Info{Stable: "2.7.14", FetchedAt: now.Add(-time.Hour)}).Fresh(now, TTL) {
		t.Error("an hour old is fresh")
	}
	if (Info{Stable: "2.7.14", FetchedAt: now.Add(-48 * time.Hour)}).Fresh(now, TTL) {
		t.Error("two days old is not")
	}
	// A resumed VM or a corrected clock leaves a cache stamped in the
	// future, which must not read as valid forever.
	if (Info{Stable: "2.7.14", FetchedAt: now.Add(72 * time.Hour)}).Fresh(now, TTL) {
		t.Error("a cache from the future is stale, not eternal")
	}
}

// The cache is the difference between one request a day and one per run.
func TestLookupUsesAndRefreshesTheCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "releases.json")
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

	if err := writeCache(path, Info{Stable: "2.7.14", FetchedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, err := Lookup(context.Background(), failing{}, path, now)
	if err != nil || got.Stable != "2.7.14" {
		t.Fatalf("a fresh cache should be used without a request: %+v %v", got, err)
	}

	// Stale, and with nothing answering: the stale answer is still better
	// than nothing, and the error comes back with it so the caller can say
	// where the answer came from.
	if err := writeCache(path, Info{Stable: "2.6.0", FetchedAt: now.Add(-72 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, err = Lookup(context.Background(), failing{}, path, now)
	if got.Stable != "2.6.0" {
		t.Fatalf("a stale cache should still be returned: %+v", got)
	}
	if err == nil {
		t.Error("the failure to refresh should be reported alongside it")
	}
}

func TestDefaultCachePath(t *testing.T) {
	t.Setenv("LOCALAPPDATA", filepath.Join("X:", "AppData"))
	if got := DefaultCachePath(); !strings.Contains(got, "wslkit") || !strings.HasSuffix(got, "releases.json") {
		t.Errorf("cache path = %q", got)
	}
	t.Setenv("LOCALAPPDATA", "")
	if got := DefaultCachePath(); !strings.HasPrefix(got, os.TempDir()) {
		t.Errorf("fallback cache path = %q", got)
	}
}

// failing stands in for a machine with no route to GitHub.
type failing struct{}

func (failing) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errNoRoute
}

var errNoRoute = errStr("no route to host")

type errStr string

func (e errStr) Error() string { return string(e) }
