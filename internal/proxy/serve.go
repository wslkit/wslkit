package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// A PAC script answers per URL, and nothing inside a distribution can run one.
// Writing one answer into the distribution's environment, which is what
// `proxy apply` does, is right for the common case and wrong for the case a
// script exists to handle: an internal host that must go direct, a build
// server with its own proxy, a rule that changes when the laptop moves.
//
// This is the other half. A forward proxy on the Windows side, which the
// distribution points at unconditionally, and which asks Windows the same
// question a browser would ask for every request it receives. The answer is
// per URL, it is the same answer the rest of the machine gets, and it follows
// the laptop from the office to home without anything inside the distribution
// changing.

// Resolver answers "what proxy applies to this URL".
type Resolver interface {
	Upstream(ctx context.Context, rawURL string) (Resolution, error)
}

// Resolution is where one request should go.
type Resolution struct {
	// Direct means connect to the origin itself.
	Direct bool
	// Proxies are the upstreams to try, in order, as "host:port".
	Proxies []string
	// Source explains the decision, for the log.
	Source string
}

// StaticResolver sends everything the same way. It is what --upstream uses, and
// what the tests use.
type StaticResolver struct {
	// Proxy is the upstream, or empty for direct.
	Proxy string
}

func (s StaticResolver) Upstream(ctx context.Context, rawURL string) (Resolution, error) {
	if s.Proxy == "" {
		return Resolution{Direct: true, Source: "configured to go direct"}, nil
	}
	return Resolution{Proxies: []string{stripScheme(s.Proxy)}, Source: "configured upstream"}, nil
}

// DefaultServePort is what the proxy listens on.
//
// Not 3128 or 8080: those are what a real proxy on the machine would be using,
// and a port clash here would present as the distribution talking to something
// that is not this.
const DefaultServePort = 18080

// ServeOptions configures the proxy.
type ServeOptions struct {
	// Binds are the addresses to listen on. The gateway address is what a
	// NAT-mode distribution can reach; loopback is for mirrored mode and for
	// testing from Windows itself.
	Binds []string
	// Port is the port on each of them.
	Port int
	// Resolver decides where each request goes.
	Resolver Resolver
	// DialTimeout bounds connecting to an origin or an upstream.
	DialTimeout time.Duration
	// IdleTimeout closes a tunnel that has gone quiet in both directions.
	IdleTimeout time.Duration
	// Log receives one line per request. Nil discards them.
	Log func(string)
}

// DefaultDialTimeout is how long to wait for an origin or upstream to answer.
// Long enough for a slow corporate proxy, short enough that a wrong address
// fails while somebody is still watching.
const DefaultDialTimeout = 30 * time.Second

// Server is a running forward proxy.
type Server struct {
	opts      ServeOptions
	listeners []net.Listener
	mu        sync.Mutex
	closed    bool
}

// NewServer prepares a proxy. Nothing listens until Serve is called.
//
// The port is taken literally, including zero, which the network stack reads as
// "any free port". The default belongs to the command line, not here: a package
// that silently rewrites a zero is one a test cannot ask for an ephemeral port.
func NewServer(o ServeOptions) *Server {
	if o.DialTimeout <= 0 {
		o.DialTimeout = DefaultDialTimeout
	}
	if o.Resolver == nil {
		o.Resolver = StaticResolver{}
	}
	return &Server{opts: o}
}

// Addrs are the addresses actually bound, which is what the caller has to tell
// the distribution to use.
func (s *Server) Addrs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, l := range s.listeners {
		out = append(out, l.Addr().String())
	}
	return out
}

// Listen binds the addresses. It is separate from Serve so a caller can report
// the addresses, and any failure to bind, before committing to the loop.
//
// A bind that fails is reported and skipped rather than fatal: the gateway
// address changes when WSL rebuilds its network, and a proxy listening on
// loopback only is still useful in mirrored mode and still testable.
func (s *Server) Listen() ([]string, []error) {
	var warnings []error
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, host := range s.opts.Binds {
		addr := net.JoinHostPort(host, fmt.Sprint(s.opts.Port))
		l, err := net.Listen("tcp", addr)
		if err != nil {
			warnings = append(warnings, fmt.Errorf("listening on %s: %w", addr, err))
			continue
		}
		s.listeners = append(s.listeners, l)
	}
	var addrs []string
	for _, l := range s.listeners {
		addrs = append(addrs, l.Addr().String())
	}
	return addrs, warnings
}

// ErrNoListeners means nothing could be bound, so there is no proxy.
var ErrNoListeners = errors.New("proxy: nothing could be bound to listen on")

// Serve runs until the context is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	listeners := append([]net.Listener(nil), s.listeners...)
	s.mu.Unlock()
	if len(listeners) == 0 {
		return ErrNoListeners
	}

	srv := &http.Server{
		Handler: s,
		// A proxy holds tunnels open for as long as the client wants them,
		// so there is no write deadline to set. Reading the request
		// headers is bounded, which is what stops a stuck client from
		// holding a goroutine forever.
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		// Close rather than Shutdown: an in-flight CONNECT tunnel is a
		// hijacked connection that Shutdown does not track, and waiting
		// for one to end could be all day.
		_ = srv.Close()
	}()

	errs := make(chan error, len(listeners))
	var wg sync.WaitGroup
	for _, l := range listeners {
		wg.Add(1)
		go func(l net.Listener) {
			defer wg.Done()
			if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}(l)
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil && ctx.Err() == nil {
		return err
	}
	return ctx.Err()
}

// Close stops listening.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for _, l := range s.listeners {
		_ = l.Close()
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.opts.Log != nil {
		s.opts.Log(fmt.Sprintf(format, args...))
	}
}

// ServeHTTP handles both shapes a proxy sees: a CONNECT for anything over TLS,
// and an ordinary request with an absolute URL for plain HTTP.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
		return
	}
	if !r.URL.IsAbs() {
		// A request with a relative path is somebody pointing a browser at
		// the proxy's own address. Saying what this is beats a confusing
		// 400 from the transport.
		http.Error(w, "This is the wslkit forward proxy. Point a client's proxy setting at it rather than browsing to it.", http.StatusBadRequest)
		return
	}
	s.handleHTTP(w, r)
}

// handleConnect tunnels. Everything over TLS arrives this way, which on a
// modern machine is nearly everything.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if target == "" {
		target = r.URL.Host
	}
	res, err := s.opts.Resolver.Upstream(r.Context(), "https://"+target)
	if err != nil {
		s.logf("CONNECT %s: resolving: %v", target, err)
		http.Error(w, "proxy resolution failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	upstream, err := s.dialFor(r.Context(), res, target, true)
	if err != nil {
		s.logf("CONNECT %s via %s: %v", target, describeRes(res), err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = upstream.Close() }()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "proxy: this connection cannot be tunnelled", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		s.logf("CONNECT %s: hijack: %v", target, err)
		return
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		return
	}
	s.logf("CONNECT %s via %s", target, describeRes(res))
	// Anything the client sent while the response was in flight is already
	// in the buffer, and dropping it would break the first TLS record.
	if buf != nil && buf.Reader.Buffered() > 0 {
		if _, err := io.CopyN(upstream, buf, int64(buf.Reader.Buffered())); err != nil {
			return
		}
	}
	tunnel(client, upstream, s.opts.IdleTimeout)
}

// handleHTTP forwards a plain request.
func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	res, err := s.opts.Resolver.Upstream(r.Context(), r.URL.String())
	if err != nil {
		s.logf("%s %s: resolving: %v", r.Method, r.URL, err)
		http.Error(w, "proxy resolution failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: s.opts.DialTimeout}).DialContext,
		ResponseHeaderTimeout: s.opts.DialTimeout,
		DisableKeepAlives:     true,
	}
	if !res.Direct && len(res.Proxies) > 0 {
		up := "http://" + stripScheme(res.Proxies[0])
		proxyURL, perr := parseProxyURL(up)
		if perr != nil {
			http.Error(w, "proxy: the upstream address is not usable: "+perr.Error(), http.StatusBadGateway)
			return
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	defer transport.CloseIdleConnections()

	outbound := r.Clone(r.Context())
	outbound.RequestURI = ""
	// Hop-by-hop headers belong to the connection this proxy just accepted,
	// not to the request it is making.
	for _, h := range hopByHop {
		outbound.Header.Del(h)
	}

	resp, err := transport.RoundTrip(outbound)
	if err != nil {
		s.logf("%s %s via %s: %v", r.Method, r.URL, describeRes(res), err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	s.logf("%s %s via %s -> %s", r.Method, r.URL, describeRes(res), resp.Status)

	for k, vv := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// dialFor opens the connection a resolution calls for: straight to the origin,
// or to an upstream with a CONNECT already negotiated through it.
//
// Every upstream in the list is tried in turn. A PAC script returns more than
// one for a reason, and giving up on the first is how a machine loses its
// network when one proxy in a pool is down.
func (s *Server) dialFor(ctx context.Context, res Resolution, target string, tunnelled bool) (net.Conn, error) {
	d := &net.Dialer{Timeout: s.opts.DialTimeout}
	if res.Direct || len(res.Proxies) == 0 {
		conn, err := d.DialContext(ctx, "tcp", withDefaultPort(target, "443"))
		if err != nil {
			return nil, fmt.Errorf("connecting to %s: %w", target, err)
		}
		return conn, nil
	}

	var last error
	for _, p := range res.Proxies {
		addr := withDefaultPort(stripScheme(p), "80")
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			last = fmt.Errorf("connecting to the proxy %s: %w", addr, err)
			continue
		}
		if !tunnelled {
			return conn, nil
		}
		if err := negotiateConnect(conn, target, s.opts.DialTimeout); err != nil {
			_ = conn.Close()
			last = err
			continue
		}
		return conn, nil
	}
	return nil, last
}

// negotiateConnect asks an upstream proxy to open a tunnel.
func negotiateConnect(conn net.Conn, target string, timeout time.Duration) error {
	hostPort := withDefaultPort(target, "443")
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: keep-alive\r\n\r\n", hostPort, hostPort)
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("sending CONNECT to the upstream proxy: %w", err)
	}
	status, err := readStatusLine(conn)
	if err != nil {
		return err
	}
	if timeout > 0 {
		// The tunnel itself has no deadline: it lives as long as the client
		// wants it to.
		_ = conn.SetDeadline(time.Time{})
	}
	switch {
	case strings.Contains(status, " 200"):
		return nil
	case strings.Contains(status, " 407"):
		// The one failure worth naming, because the fix is not in this
		// tool. Windows can authenticate to this proxy and Go cannot:
		// NTLM and Kerberos need SSPI.
		return fmt.Errorf("the upstream proxy demands authentication (%s). wslkit cannot answer an NTLM or Kerberos challenge yet; a proxy that authenticates with Windows credentials, such as px, can sit in front of it", strings.TrimSpace(status))
	default:
		return fmt.Errorf("the upstream proxy refused the tunnel: %s", strings.TrimSpace(status))
	}
}

// readStatusLine reads the response to a CONNECT, then drains its headers.
func readStatusLine(conn net.Conn) (string, error) {
	var line strings.Builder
	buf := make([]byte, 1)
	first := ""
	for {
		if _, err := io.ReadFull(conn, buf); err != nil {
			return "", fmt.Errorf("reading the upstream proxy's answer: %w", err)
		}
		if buf[0] == '\n' {
			s := strings.TrimRight(line.String(), "\r")
			if first == "" {
				first = s
			}
			if s == "" {
				// One blank line ends the headers, and the tunnel bytes
				// start immediately after it. Reading one byte at a time
				// is what keeps this from swallowing the first of them.
				return first, nil
			}
			line.Reset()
			continue
		}
		line.WriteByte(buf[0])
		if line.Len() > 8192 {
			return "", fmt.Errorf("the upstream proxy sent a header line that is too long to be real")
		}
	}
}

// tunnel copies in both directions until either end closes.
func tunnel(a, b net.Conn, idle time.Duration) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		defer func() { done <- struct{}{} }()
		if idle > 0 {
			_ = src.SetReadDeadline(time.Now().Add(idle))
		}
		_, _ = io.Copy(dst, src)
		// Closing the write side rather than the whole connection lets the
		// other direction finish, which is what a half-closed HTTP upload
		// needs.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

// hopByHop are the headers that belong to one connection and must not be
// forwarded.
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func isHopByHop(h string) bool {
	for _, v := range hopByHop {
		if strings.EqualFold(h, v) {
			return true
		}
	}
	return false
}

// withDefaultPort adds a port to a bare host.
//
// The brackets around an IPv6 literal are stripped before joining, because
// JoinHostPort adds its own and "[[::1]]:443" is not an address.
func withDefaultPort(hostPort, port string) string {
	if _, _, err := net.SplitHostPort(hostPort); err == nil {
		return hostPort
	}
	return net.JoinHostPort(strings.Trim(hostPort, "[]"), port)
}

// stripScheme removes a scheme prefix from a proxy address, which a PAC script
// or a user may or may not have included.
func stripScheme(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	return strings.TrimSuffix(s, "/")
}

// describeRes names where a request went, for the log.
func describeRes(res Resolution) string {
	if res.Direct || len(res.Proxies) == 0 {
		return "direct"
	}
	return res.Proxies[0]
}

// parseProxyURL turns an upstream address into a URL for the transport.
func parseProxyURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%q has no host", s)
	}
	return u, nil
}
