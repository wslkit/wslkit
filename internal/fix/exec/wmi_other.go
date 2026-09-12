//go:build !windows

package exec

import (
	"errors"

	"github.com/wslkit/wslkit/internal/fix"
)

func runWMIMethod(fix.Step) error {
	return errors.New("wmi_method steps can only run on Windows")
}
