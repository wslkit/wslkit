// Package exec is the only place a fix touches the machine.
package exec

import (
	"fmt"
	"io"
	"os"
	osexec "os/exec"

	"github.com/wslkit/wslkit/internal/fix"
)

// Real runs exec steps as child processes with inherited stdio, and prints notes.
type Real struct {
	Out io.Writer
}

func (r Real) Run(s fix.Step) error {
	if os.Getenv("WSLKIT_TEST") == "1" {
		panic("fix/exec.Real used under WSLKIT_TEST=1")
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
	case "file_copy":
		if len(s.Args) != 2 {
			return fmt.Errorf("file_copy needs source and destination")
		}
		b, err := os.ReadFile(s.Args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "copy %s -> %s\n", s.Args[0], s.Args[1])
		return os.WriteFile(s.Args[1], b, 0o600)
	case "file_write":
		if len(s.Args) != 2 {
			return fmt.Errorf("file_write needs path and content")
		}
		fmt.Fprintf(out, "write %s (%d bytes)\n", s.Args[0], len(s.Args[1]))
		return os.WriteFile(s.Args[0], []byte(s.Args[1]), 0o600)
	case "file_delete":
		if len(s.Args) != 1 {
			return fmt.Errorf("file_delete needs one path")
		}
		// A directory is never what a fix meant to delete, and os.Remove
		// would take an empty one.
		if st, err := os.Lstat(s.Args[0]); err == nil && st.IsDir() {
			return fmt.Errorf("refusing to delete a directory: %s", s.Args[0])
		}
		fmt.Fprintf(out, "delete %s\n", s.Args[0])
		if err := os.Remove(s.Args[0]); err != nil && !os.IsNotExist(err) {
			return err
		}
		// Already gone is the intended state, not a failure: it makes a
		// half-applied plan safe to run again.
		return nil
	case "wmi_method":
		fmt.Fprintf(out, "wmi %s\n", s.Description)
		return runWMIMethod(s)
	default:
		return fmt.Errorf("unsupported step kind %q", s.Kind)
	}
}
