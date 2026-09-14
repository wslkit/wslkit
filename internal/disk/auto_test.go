package disk

import (
	"strings"
	"testing"
)

// The case --auto exists for: a scheduled run must not stop somebody's
// distribution to reclaim a few megabytes.
func TestDecideAutoSkipsWhatIsNotWorthAStop(t *testing.T) {
	info := Info{SizeOnDisk: u64(20 << 30), GuestUsed: u64(19<<30 + 800<<20)}
	d := DecideAuto(info, true, AutoOptions{})
	if d.Compact {
		t.Fatalf("200 MiB is not worth stopping a distribution for: %q", d.Reason)
	}
	if !strings.Contains(d.Reason, "below") {
		t.Errorf("reason = %q", d.Reason)
	}
}

func TestDecideAutoCompactsWhenThereIsEnough(t *testing.T) {
	info := Info{SizeOnDisk: u64(20 << 30), GuestUsed: u64(12 << 30)}
	d := DecideAuto(info, true, AutoOptions{})
	if !d.Compact {
		t.Fatalf("8 GiB is worth it: %q", d.Reason)
	}
	if !strings.Contains(d.Reason, "reclaimable") {
		t.Errorf("reason = %q", d.Reason)
	}
}

// Nothing to stop means nothing to weigh it against. This is also the case
// where the estimate cannot be read, so a rule that demanded one would never
// compact a stopped distribution at all — which is the cheapest kind to
// compact.
func TestDecideAutoAlwaysCompactsWhenNothingIsDisrupted(t *testing.T) {
	d := DecideAuto(Info{}, false, AutoOptions{})
	if !d.Compact {
		t.Fatalf("reason = %q", d.Reason)
	}
	if !strings.Contains(d.Reason, "nothing has to be stopped") {
		t.Errorf("reason = %q", d.Reason)
	}
}

// An unreadable estimate on a distribution that would have to be stopped is a
// skip, not a guess: starting it to find out is the disruption being avoided.
func TestDecideAutoSkipsWhenItCannotTell(t *testing.T) {
	d := DecideAuto(Info{SizeOnDisk: u64(4 << 30)}, true, AutoOptions{})
	if d.Compact {
		t.Fatal("it cannot know, so it must not act")
	}
	if !strings.Contains(d.Reason, "without starting it") {
		t.Errorf("reason = %q", d.Reason)
	}
}

func TestDecideAutoHonoursTheThreshold(t *testing.T) {
	info := Info{SizeOnDisk: u64(4 << 30), GuestUsed: u64(3<<30 + 512<<20)} // 512 MiB
	if DecideAuto(info, true, AutoOptions{}).Compact {
		t.Error("512 MiB is below the default")
	}
	if !DecideAuto(info, true, AutoOptions{MinReclaim: 256 << 20}).Compact {
		t.Error("512 MiB is above a 256 MiB threshold")
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]uint64{
		"1024":    1024,
		"1KB":     1 << 10,
		"1 MiB":   1 << 20,
		"2gb":     2 << 30,
		"2GiB":    2 << 30,
		"1.5 GiB": 1<<30 + 512<<20,
		"0":       0,
	}
	for in, want := range cases {
		got, ok := ParseSize(in)
		if !ok || got != want {
			t.Errorf("ParseSize(%q) = %d, %v, want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "  ", "big", "GB", "1.2.3GB", "-1"} {
		if _, ok := ParseSize(bad); ok {
			t.Errorf("ParseSize(%q) should have failed", bad)
		}
	}
}

// What this tool prints has to read back in, or --min-reclaim cannot be set
// from a number the user just saw.
func TestParseSizeRoundTripsFormatSize(t *testing.T) {
	for _, n := range []uint64{1 << 20, 3<<30 + 512<<20, 42 << 10} {
		s := FormatSize(n)
		got, ok := ParseSize(s)
		if !ok {
			t.Fatalf("FormatSize(%d) = %q, which does not parse", n, s)
		}
		// The printed form is rounded to one decimal, so the round trip is
		// close rather than exact.
		diff := int64(got) - int64(n)
		if diff < 0 {
			diff = -diff
		}
		if float64(diff) > float64(n)*0.02 {
			t.Errorf("FormatSize(%d) = %q parsed back as %d", n, s, got)
		}
	}
}
