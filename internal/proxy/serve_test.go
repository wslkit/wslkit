package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// startProxy runs a server on loopback and returns its address.
func startProxy(t *testing.T, o ServeOptions) (string, *Server) {
	t.Helper()
	o.Binds = []string{"127.0.0.1"}
	o.Port = 0 // any free port
	s := NewServer(o)
	addrs, warns := s.Listen()
	for _, w := range warns {
		t.Fatalf("bind: %v", w)
	}
	if len(addrs) != 1 {
		t.Fatalf("addrs = %v", addrs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		s.Close()
		<-done
	})
	return addrs[0], s
}

// clientThrough builds an HTTP client that talks through the proxy, the way a
// program inside a distribution would once http_proxy is set.
func clientThrough(t *testing.T, proxyAddr string, roots *x509.CertPool) *http.Client {
	t.Helper()
	u, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(u),
			TLSClientConfig: &tls.Config{RootCAs: roots},
		},
	}
}

// The plain case: an ordinary HTTP request, forwarded straight to the origin.
func TestServeForwardsPlainHTTPDirect(t *testing.T) {
	origin := httptestServer(t, "hello from the origin")
	addr, _ := startProxy(t, ServeOptions{Resolver: StaticResolver{}})

	resp, err := clientThrough(t, addr, nil).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from the origin" {
		t.Errorf("body = %q", body)
	}
}

// The case that matters most, because nearly everything is TLS now: CONNECT,
// tunnelled straight to the origin.
func TestServeTunnelsCONNECTDirect(t *testing.T) {
	origin, roots := httptestTLSServer(t, "hello over TLS")
	addr, _ := startProxy(t, ServeOptions{Resolver: StaticResolver{}})

	resp, err := clientThrough(t, addr, roots).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello over TLS" {
		t.Errorf("body = %q", body)
	}
}

// Two proxies in a chain, which is what a PAC script returning an upstream
// actually produces: the client's proxy has to open a tunnel through another
// one and then get out of the way.
func TestServeChainsThroughAnUpstream(t *testing.T) {
	origin, roots := httptestTLSServer(t, "through two proxies")

	upstreamAddr, _ := startProxy(t, ServeOptions{Resolver: StaticResolver{}})
	frontAddr, _ := startProxy(t, ServeOptions{Resolver: StaticResolver{Proxy: upstreamAddr}})

	resp, err := clientThrough(t, frontAddr, roots).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "through two proxies" {
		t.Errorf("body = %q", body)
	}
}

// Plain HTTP through an upstream takes a different path in the code than a
// tunnel does, and gets its own test for that reason.
func TestServeForwardsPlainHTTPThroughAnUpstream(t *testing.T) {
	origin := httptestServer(t, "plain through two")
	upstreamAddr, _ := startProxy(t, ServeOptions{Resolver: StaticResolver{}})
	frontAddr, _ := startProxy(t, ServeOptions{Resolver: StaticResolver{Proxy: upstreamAddr}})

	resp, err := clientThrough(t, frontAddr, nil).Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "plain through two" {
		t.Errorf("body = %q", body)
	}
}

// A resolver can answer differently per URL. That is the whole reason this
// exists rather than one address in the environment.
func TestServeAsksPerRequest(t *testing.T) {
	internal := httptestServer(t, "internal")
	external := httptestServer(t, "external")
	upstreamAddr, _ := startProxy(t, ServeOptions{Resolver: StaticResolver{}})

	internalHost := hostOf(t, internal.URL)
	var asked []string
	var mu sync.Mutex
	r := funcResolver(func(ctx context.Context, rawURL string) (Resolution, error) {
		mu.Lock()
		asked = append(asked, rawURL)
		mu.Unlock()
		if strings.Contains(rawURL, internalHost) {
			return Resolution{Direct: true, Source: "internal"}, nil
		}
		return Resolution{Proxies: []string{upstreamAddr}, Source: "external"}, nil
	})
	addr, _ := startProxy(t, ServeOptions{Resolver: r})
	client := clientThrough(t, addr, nil)

	for _, u := range []string{internal.URL, external.URL} {
		resp, err := client.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 {
		t.Fatalf("the resolver was asked %d times, want one per request: %v", len(asked), asked)
	}
}

// An upstream that demands NTLM is the failure a corporate user will actually
// hit, and the fix is not in this tool. Saying so beats a bare 502.
func TestUpstreamAuthenticationIsNamed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 1024)
				_, _ = c.Read(buf)
				_, _ = c.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: NTLM\r\n\r\n"))
			}()
		}
	}()

	var logged []string
	var mu sync.Mutex
	addr, _ := startProxy(t, ServeOptions{
		Resolver: StaticResolver{Proxy: ln.Addr().String()},
		Log:      func(s string) { mu.Lock(); logged = append(logged, s); mu.Unlock() },
	})

	// A CONNECT, because that is where the 407 shows up.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	fmt.Fprint(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	// Read one response rather than to end of stream: the proxy answers the
	// failed CONNECT and keeps the connection open, so reading to EOF would
	// wait for a close that correctly never comes.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %s", resp.Status)
	}
	if !strings.Contains(string(body), "NTLM or Kerberos") {
		t.Errorf("the answer should name what is wrong and what can fix it: %q", body)
	}
}

// Pointing a browser at the proxy's own address is a mistake somebody will
// make, and the answer should say what this is.
func TestBrowsingToTheProxyExplainsItself(t *testing.T) {
	addr, _ := startProxy(t, ServeOptions{Resolver: StaticResolver{}})
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "forward proxy") {
		t.Errorf("status %d, body %q", resp.StatusCode, body)
	}
}

func TestWithDefaultPort(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":      "example.com:443",
		"example.com:8443": "example.com:8443",
		"[::1]":            "[::1]:443",
		"[::1]:9":          "[::1]:9",
	} {
		if got := withDefaultPort(in, "443"); got != want {
			t.Errorf("withDefaultPort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripScheme(t *testing.T) {
	for in, want := range map[string]string{
		"http://p:8080": "p:8080",
		"p:8080":        "p:8080",
		"http://p:80/":  "p:80",
	} {
		if got := stripScheme(in); got != want {
			t.Errorf("stripScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

// funcResolver adapts a function.
type funcResolver func(ctx context.Context, rawURL string) (Resolution, error)

func (f funcResolver) Upstream(ctx context.Context, rawURL string) (Resolution, error) {
	return f(ctx, rawURL)
}

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname() + ":" + u.Port()
}

// httptestServer and httptestTLSServer are small wrappers so each test reads as
// what it is testing rather than as server setup.
func httptestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func httptestTLSServer(t *testing.T, body string) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	return s, pool
}
