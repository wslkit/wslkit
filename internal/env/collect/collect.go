// Package collect gathers the Env. The framework here is portable; the
// collectors themselves are in collect_windows.go and only build on Windows.
package collect

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/wslkit/wsldoctor/internal/env"
)

type Options struct {
	Tool        string
	AllowVMWake bool
	Timeout     time.Duration // per collector
	EventWindow time.Duration
}

func (o Options) withDefaults() Options {
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Second
	}
	if o.EventWindow == 0 {
		o.EventWindow = 7 * 24 * time.Hour
	}
	if o.Tool == "" {
		o.Tool = "wsldoctor"
	}
	return o
}

type collector struct {
	name string
	run  func(ctx context.Context, e *env.Env, o Options) error
}

// runAll executes collectors concurrently. Each writes only its own fields of
// Env; nothing reads Env until all have returned. A collector that exceeds its
// deadline is abandoned (its goroutine may finish later and write into fields
// nobody reads again).
func runAll(ctx context.Context, e *env.Env, o Options, cs []collector) {
	var mu sync.Mutex
	e.Collectors = map[string]env.CollectorStat{}
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Add(1)
		go func(c collector) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, o.Timeout)
			defer cancel()
			start := time.Now()
			done := make(chan error, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						done <- fmt.Errorf("panic: %v", r)
					}
				}()
				done <- c.run(cctx, e, o)
			}()
			stat := env.CollectorStat{}
			select {
			case err := <-done:
				if err != nil {
					stat.Err = err.Error()
				}
			case <-cctx.Done():
				stat.TimedOut = true
				stat.Err = "timed out after " + o.Timeout.String()
			}
			stat.DurationMS = time.Since(start).Milliseconds()
			mu.Lock()
			e.Collectors[c.name] = stat
			mu.Unlock()
		}(c)
	}
	wg.Wait()
}
