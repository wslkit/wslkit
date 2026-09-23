package cli

import (
	"testing"
	"time"
)

func TestChooseTopMode(t *testing.T) {
	const two = 2 * time.Second
	for _, c := range []struct {
		name                          string
		once, watch, jsonOut, console bool
		interval                      time.Duration
		want                          topMode
		wantErr                       bool
	}{
		{name: "plain top on a console refreshes", console: true, interval: two, want: topWatch},
		{name: "piped, it prints once and exits", console: false, interval: two, want: topReport},
		{name: "--json prints once, even on a console", jsonOut: true, console: true, interval: two, want: topReport},
		{name: "--once prints once on a console", once: true, console: true, interval: two, want: topReport},
		{name: "--watch refreshes when piped", watch: true, console: false, interval: two, want: topWatch},
		{name: "--watch --json streams objects", watch: true, jsonOut: true, interval: two, want: topWatch},
		{name: "--interval 0 is one instant sample", console: true, interval: 0, want: topReport},
		{name: "--watch --once is a contradiction", watch: true, once: true, interval: two, wantErr: true},
		{name: "--watch --interval 0 has nothing to redraw against", watch: true, interval: 0, wantErr: true},
	} {
		got, err := chooseTopMode(c.once, c.watch, c.jsonOut, c.console, c.interval)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err %v", c.name, err)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
