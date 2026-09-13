// Package relay copies bytes between two duplex endpoints, propagating
// half-closes, so a local client and a remote service see one connection.
package relay

import (
	"io"
	"sync"
)

// Duplex is what both ends must provide: Read/Write/Close plus CloseWrite for
// half-close. net.TCPConn, mux.Stream and go-winio pipes all satisfy it (pipes
// via an adapter).
type Duplex interface {
	io.ReadWriteCloser
	CloseWrite() error
}

// Filter may rewrite or intercept traffic in one direction. Returning ok=false
// means the filter consumed the bytes and wrote any reply itself.
type Filter interface {
	// Forward is called with data flowing from src to dst; it returns the bytes
	// to actually forward (possibly empty) or an error that ends the relay.
	Forward(data []byte, replyTo io.Writer) ([]byte, error)
}

// Pipe relays a<->b until both directions are done or one errors. Filters are
// optional: aToB filters data from a to b, bToA the reverse.
func Pipe(a, b Duplex, aToB, bToA Filter) error {
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs <- copyDir(a, b, aToB) }()
	go func() { defer wg.Done(); errs <- copyDir(b, a, bToA) }()
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
	var first error
	for i := 0; i < 2; i++ {
		if e := <-errs; e != nil && first == nil {
			first = e
		}
	}
	return first
}

// copyDir moves src -> dst until EOF, then half-closes dst. A filter may reply
// to src directly (e.g. answering an unsupported request locally).
func copyDir(src, dst Duplex, f Filter) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			out := buf[:n]
			if f != nil {
				var ferr error
				out, ferr = f.Forward(out, src)
				if ferr != nil {
					return ferr
				}
			}
			if len(out) > 0 {
				if _, werr := dst.Write(out); werr != nil {
					return werr
				}
			}
		}
		if err != nil {
			_ = dst.CloseWrite()
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
