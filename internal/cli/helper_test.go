package cli

import "bytes"

// newApp builds an App whose output can be inspected, so a test asserts on what
// the user would see rather than on internal state.
func newApp() (*App, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &App{Version: "test", Stdout: &out, Stderr: &errb}, &out, &errb
}
