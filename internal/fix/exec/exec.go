// Package exec is the only place a fix touches the machine.
package exec

import (
	"fmt"
	"io"
	"os"
	osexec "os/exec"

	"github.com/wslkit/wsldoctor/internal/fix"
)

// Real runs exec steps as child processes with inherited stdio, and prints notes.
type Real struct {
	Out io.Writer
}

func (r Real) Run(s fix.Step) error {
	if os.Getenv("WSLDOCTOR_TEST") == "1" {
		panic("fix/exec.Real used under WSLDOCTOR_TEST=1")
	}
	out := r.Out
	if out == nil {
		out = os.Stdout
	}
	switch s.Kind {
	case "exec":
		if len(s.Args) == 0 {
			return fmt.Errorf("exec step with no args")
		}
		fmt.Fprintf(out, "$ %v\n", s.Args)
		cmd := osexec.Command(s.Args[0], s.Args[1:]...)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = out, os.Stderr, os.Stdin
		return cmd.Run()
	case "note":
		fmt.Fprintln(out, s.Description)
		return nil
	default:
		return fmt.Errorf("unsupported step kind %q", s.Kind)
	}
}
