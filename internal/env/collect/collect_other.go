//go:build !windows

package collect

import (
	"context"
	"errors"

	"github.com/wslkit/wslkit/internal/env"
)

var ErrUnsupported = errors.New("live collection is only available on Windows; use --from-snapshot")

// Run is not available off Windows; the CLI falls back to snapshots.
func Run(ctx context.Context, o Options) (*env.Env, error) {
	return nil, ErrUnsupported
}
