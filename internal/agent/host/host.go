// Package host is the Windows side of the guest agent: it listens for guest
// connections, answers OPEN requests against an allow list, and relays. The
// listener is injected so tests can use TCP loopback instead of Hyper-V sockets.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wslkit/wslkit/internal/agent/config"
	"github.com/wslkit/wslkit/internal/agent/filter"
	"github.com/wslkit/wslkit/internal/agent/mux"
	"github.com/wslkit/wslkit/internal/agent/proto"
	"github.com/wslkit/wslkit/internal/agent/relay"
)

// Dialer connects to a Windows-side target (see package target).
type Dialer func(ctx context.Context, target string) (relay.Duplex, error)

// Status is written to StatusPath while the daemon runs and read by
// `wslkit agent status` and the doctor.
type Status struct {
	PID        int             `json:"pid"`
	Version    string          `json:"version"`
	StartedAt  time.Time       `json:"started_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	VMID       string          `json:"vm_id,omitempty"`
	Listening  string          `json:"listening,omitempty"` // "hvsock:<vmid>:<port>" or the test address
	Sessions   []SessionStatus `json:"sessions"`
	LastError  string          `json:"last_error,omitempty"`
	Reconnects int             `json:"reconnects"`
}

type SessionStatus struct {
	Distro  string    `json:"distro"`
	Since   time.Time `json:"since"`
	Streams int32     `json:"streams"`
	Opened  int64     `json:"opened_total"`
	Refused int64     `json:"refused_total"`
}

// StatusPath is the status file location.
func StatusPath() string { return filepath.Join(config.HostDir(), "status.json") }

// ReadStatus loads the default status file; ok is false when absent.
func ReadStatus() (Status, bool, error) { return ReadStatusFrom(StatusPath()) }

// ReadStatusFrom loads a status file; ok is false when absent.
func ReadStatusFrom(path string) (Status, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, err
	}
	var s Status
	if err := json.Unmarshal(b, &s); err != nil {
		return Status{}, false, err
	}
	return s, true, nil
}

// Daemon serves guest sessions.
type Daemon struct {
	Config  config.Host
	Version string
	Dial    Dialer
	Logger  *log.Logger
	// StatusFile, when non-empty, receives Status snapshots.
	StatusFile string

	mu       sync.Mutex
	status   Status
	sessions map[*session]struct{}
	// inflight counts session handlers so Serve can drain before returning:
	// nothing writes the status file after Serve exits.
	inflight sync.WaitGroup
}

type session struct {
	sess    *mux.Session
	distro  string
	since   time.Time
	streams atomic.Int32
	opened  atomic.Int64
	refused atomic.Int64
}

// Serve accepts connections from l until ctx ends or Accept fails. It returns
// the Accept error so the caller can rebind (the VM id changes on shutdown).
func (d *Daemon) Serve(ctx context.Context, l net.Listener, listening string) error {
	if d.Logger == nil {
		d.Logger = log.New(os.Stderr, "wslkit agent: ", log.LstdFlags)
	}
	d.mu.Lock()
	if d.sessions == nil {
		d.sessions = map[*session]struct{}{}
		d.status.StartedAt = time.Now()
		d.status.PID = os.Getpid()
		d.status.Version = d.Version
	}
	d.status.Listening = listening
	d.status.LastError = ""
	d.mu.Unlock()
	d.writeStatus()

	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			d.inflight.Wait() // drain handlers before the caller tears anything down
			if ctx.Err() != nil {
				return ctx.Err()
			}
			d.setError(err)
			return err
		}
		d.inflight.Add(1)
		go func() {
			defer d.inflight.Done()
			d.handle(ctx, c)
		}()
	}
}

func (d *Daemon) setError(err error) {
	d.mu.Lock()
	d.status.LastError = err.Error()
	d.mu.Unlock()
	d.writeStatus()
}

// SetVMID records the VM id the daemon is bound to.
func (d *Daemon) SetVMID(id string) {
	d.mu.Lock()
	d.status.VMID = id
	d.status.Reconnects++
	d.mu.Unlock()
	d.writeStatus()
}

func (d *Daemon) handle(ctx context.Context, c net.Conn) {
	sess, err := mux.Handshake(c, proto.Hello{Role: proto.RoleHost, Name: "windows", AgentVersion: d.Version, Caps: []string{"sock"}}, 10*time.Second)
	if err != nil {
		d.Logger.Printf("handshake failed from %s: %v", c.RemoteAddr(), err)
		return
	}
	s := &session{sess: sess, distro: sess.Peer().Name, since: time.Now()}
	d.mu.Lock()
	d.sessions[s] = struct{}{}
	d.mu.Unlock()
	d.writeStatus()
	d.Logger.Printf("guest %q connected (agent %s)", s.distro, sess.Peer().AgentVersion)
	defer func() {
		d.mu.Lock()
		delete(d.sessions, s)
		d.mu.Unlock()
		d.writeStatus()
		d.Logger.Printf("guest %q disconnected: %v", s.distro, sess.Err())
	}()

	// Heartbeat keeps dead vsock peers from lingering forever.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := sess.Ping(); err != nil {
					return
				}
			case <-sess.Done():
				return
			}
		}
	}()

	var streams sync.WaitGroup
	defer streams.Wait()
	for {
		req, err := sess.Accept(ctx)
		if err != nil {
			return
		}
		streams.Add(1)
		go func() {
			defer streams.Done()
			d.open(ctx, s, req)
		}()
	}
}

func (d *Daemon) open(ctx context.Context, s *session, req *mux.OpenRequest) {
	if !d.Config.Allowed(req.Target) {
		s.refused.Add(1)
		d.Logger.Printf("guest %q: refused open of %s (not in allow list)", s.distro, req.Target)
		_ = req.Reject("target not allowed by the Windows-side wslkit configuration")
		d.writeStatus()
		return
	}
	toTarget, fromTarget, ok := filter.New(d.Config.FilterFor(req.Target))
	if !ok {
		_ = req.Reject("unknown filter configured for target")
		return
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	remote, err := d.Dial(dctx, req.Target)
	cancel()
	if err != nil {
		s.refused.Add(1)
		d.Logger.Printf("guest %q: dial %s: %v", s.distro, req.Target, err)
		_ = req.Reject(err.Error())
		d.writeStatus()
		return
	}
	st, err := req.Accept()
	if err != nil {
		_ = remote.Close()
		return
	}
	s.opened.Add(1)
	s.streams.Add(1)
	d.writeStatus()
	_ = relay.Pipe(st, remote, toTarget, fromTarget)
	s.streams.Add(-1)
	d.writeStatus()
}

// Snapshot returns the current status.
func (d *Daemon) Snapshot() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.status
	st.UpdatedAt = time.Now()
	st.Sessions = make([]SessionStatus, 0, len(d.sessions))
	for s := range d.sessions {
		st.Sessions = append(st.Sessions, SessionStatus{Distro: s.distro, Since: s.since, Streams: s.streams.Load(), Opened: s.opened.Load(), Refused: s.refused.Load()})
	}
	return st
}

func (d *Daemon) writeStatus() {
	if d.StatusFile == "" {
		return
	}
	st := d.Snapshot()
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(d.StatusFile), 0o700)
	tmp := d.StatusFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, d.StatusFile)
	}
}

// Alive reports whether a status file describes a live process. It is a
// heuristic: pid reuse is possible, so callers should also check UpdatedAt.
func Alive(s Status) bool {
	if s.PID <= 0 {
		return false
	}
	p, err := os.FindProcess(s.PID)
	if err != nil {
		return false
	}
	// On Windows FindProcess succeeds only for existing processes.
	_ = p
	return time.Since(s.UpdatedAt) < 2*time.Minute || len(s.Sessions) > 0
}

// String renders a status for humans.
func (s Status) String() string {
	out := fmt.Sprintf("pid %d, version %s, listening %s, vm %s, started %s\n", s.PID, s.Version, orDash(s.Listening), orDash(s.VMID), s.StartedAt.Local().Format(time.RFC3339))
	if s.LastError != "" {
		out += "last error: " + s.LastError + "\n"
	}
	if len(s.Sessions) == 0 {
		out += "no guest connected\n"
	}
	for _, g := range s.Sessions {
		out += fmt.Sprintf("guest %-20s since %s  streams %d  opened %d  refused %d\n", g.Distro, g.Since.Local().Format("15:04:05"), g.Streams, g.Opened, g.Refused)
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
