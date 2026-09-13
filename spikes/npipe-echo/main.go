//go:build windows

// Spike: serve a named pipe that upper-cases whatever it receives, so the
// wslkit sock path (guest AF_UNIX -> vsock -> daemon -> named pipe) can be
// verified end to end on a real machine without the OpenSSH agent.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/Microsoft/go-winio"
)

func main() {
	name := `\\.\pipe\wslkit-test-echo`
	if len(os.Args) > 1 {
		name = os.Args[1]
	}
	l, err := winio.ListenPipe(name, nil)
	if err != nil {
		panic(err)
	}
	defer func() { _ = l.Close() }()
	fmt.Println("serving", name)
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = c.Close() }()
			buf := make([]byte, 4096)
			for {
				n, err := c.Read(buf)
				if n > 0 {
					if _, werr := c.Write(bytes.ToUpper(buf[:n])); werr != nil {
						return
					}
				}
				if err != nil {
					if err != io.EOF {
						fmt.Println("read:", err)
					}
					return
				}
			}
		}()
	}
}
