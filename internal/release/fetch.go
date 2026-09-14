package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// URL is the listing the lookup reads. Thirty is more than enough to find the
// newest stable and pre-release, and one page means one request.
const URL = "https://api.github.com/repos/microsoft/WSL/releases?per_page=30"

// TTL is how long a lookup is reused. WSL releases every few weeks, so a day
// is far finer than the thing being watched changes.
const TTL = 24 * time.Hour

// Timeout bounds the request. This runs inside a diagnosis someone is waiting
// on, and a slow answer about a version number is worth less than a fast
// answer about everything else.
const Timeout = 3 * time.Second

// maxBody caps what is read. Thirty releases is around 200 KB; anything far
// past that is not the listing.
const maxBody = 4 << 20

// Lookup returns the published releases, from the cache when it is fresh and
// from GitHub when it is not.
//
// A failed fetch with a stale cache returns the stale answer rather than
// nothing: a fortnight-old release list is still better than the one compiled
// into a binary a year ago. The error is returned alongside so the caller can
// say where the answer came from.
func Lookup(ctx context.Context, rt http.RoundTripper, cachePath string, now time.Time) (Info, error) {
	if rt == nil {
		// http.DefaultTransport already honours HTTPS_PROXY and the
		// system proxy settings, which is why nothing here reimplements
		// them.
		rt = http.DefaultTransport
	}
	cached, _ := readCache(cachePath)
	if cached.Fresh(now, TTL) {
		return cached, nil
	}
	info, err := Fetch(ctx, rt, now)
	if err != nil {
		if cached.Stable != "" {
			return cached, err
		}
		return Info{}, err
	}
	// A cache that cannot be written is not worth failing over; the answer
	// is already in hand.
	_ = writeCache(cachePath, info)
	return info, nil
}

// Fetch asks GitHub. The transport is a parameter so a test can answer without
// a network, and so a caller can supply one with its own proxy settings;
// http.DefaultTransport already honours HTTPS_PROXY.
func Fetch(ctx context.Context, rt http.RoundTripper, now time.Time) (Info, error) {
	cctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, URL, nil)
	if err != nil {
		return Info{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// An unauthenticated request is rate limited by address, and a shared
	// address can run out. Identifying the tool is what lets GitHub tell
	// this apart from a scraper, and is what their documentation asks for.
	req.Header.Set("User-Agent", "wslkit (+https://github.com/wslkit/wslkit)")

	resp, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("release: fetching the releases listing: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		// The usual reason is the unauthenticated hourly limit, which says
		// when it resets. Repeating that is more use than the status code.
		msg := "release: GitHub rate limited the request"
		// The reset header is a Unix timestamp, in seconds.
		if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
			if secs, perr := strconv.ParseInt(reset, 10, 64); perr == nil {
				msg += ", until " + time.Unix(secs, 0).UTC().Format("15:04 MST")
			}
		}
		return Info{}, fmt.Errorf("%s", msg)
	default:
		return Info{}, fmt.Errorf("release: GitHub answered %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Info{}, fmt.Errorf("release: reading the releases listing: %w", err)
	}
	info, err := Parse(body)
	if err != nil {
		return Info{}, err
	}
	info.FetchedAt = now
	return info, nil
}

// DefaultCachePath is %LOCALAPPDATA%\wslkit\cache\releases.json, beside the undo
// journal, falling back to the temporary directory where that is not set.
func DefaultCachePath() string {
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		return filepath.Join(la, "wslkit", "cache", "releases.json")
	}
	return filepath.Join(os.TempDir(), "wslkit", "cache", "releases.json")
}

func readCache(path string) (Info, error) {
	if path == "" {
		return Info{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Info{}, err
	}
	var info Info
	if err := json.Unmarshal(b, &info); err != nil {
		// A cache file that cannot be read is one to replace, not one to
		// complain about.
		return Info{}, err
	}
	return info, nil
}

func writeCache(path string, info Info) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
