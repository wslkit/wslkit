//go:build windows

package exec

import (
	"context"
	"fmt"
	"time"

	"github.com/wslkit/wslkit/internal/fix"
	"github.com/wslkit/wslkit/internal/winapi/wmi"
)

func runWMIMethod(s fix.Step) error {
	call, err := fix.DecodeWMIMethod(s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rv, _, err := wmi.ExecMethod(ctx, call.Namespace, call.Class, call.Method, call.Params)
	if err != nil {
		return err
	}
	if rv != 0 {
		return fmt.Errorf("%s.%s returned %d", call.Class, call.Method, rv)
	}
	return nil
}
