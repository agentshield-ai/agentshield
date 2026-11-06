package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func serveLimited(t *testing.T, max, perIP int, h http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = hs.Serve(LimitListener(ln, max, perIP, nil, nil)) }()
	t.Cleanup(func() { _ = hs.Close() })
	return ln.Addr().String()
}

// get sends a GET on c and reports whether a response came within d.
func get(c net.Conn, d time.Duration) bool {
	_ = c.SetDeadline(time.Now().Add(d))
	if _, err := fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		return false
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	return err == nil && strings.HasPrefix(line, "HTTP/1.1 200")
}

// One address gets at most perIP open connections; more are closed.
func TestLimitListenerPerAddress(t *testing.T) {
	addr := serveLimited(t, 0, 2, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	var conns []net.Conn
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		conns = append(conns, c)
	}
	if !get(conns[0], 2*time.Second) || !get(conns[1], 2*time.Second) {
		t.Fatal("connections within the cap were not served")
	}
	if get(conns[2], 2*time.Second) {
		t.Fatal("a third connection from the address was served")
	}
	conns[0].Close()
	time.Sleep(100 * time.Millisecond)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !get(c, 2*time.Second) {
		t.Fatal("a connection after one closed was not served")
	}
}

// At most max connections are open; the next waits for one to close.
func TestLimitListenerGlobal(t *testing.T) {
	addr := serveLimited(t, 2, 0, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	a, _ := net.Dial("tcp", addr)
	b, _ := net.Dial("tcp", addr)
	defer b.Close()
	if !get(a, 2*time.Second) || !get(b, 2*time.Second) {
		t.Fatal("connections within the cap were not served")
	}
	c, _ := net.Dial("tcp", addr)
	defer c.Close()
	done := make(chan bool, 1)
	go func() { done <- get(c, 5*time.Second) }()
	select {
	case <-done:
		t.Fatal("a connection over the cap was served while the others were open")
	case <-time.After(300 * time.Millisecond):
	}
	a.Close()
	if !<-done {
		t.Fatal("the waiting connection was not served once one closed")
	}
}

// A request body that stops arriving is cut off after bodyPaceWindow,
// long before the server's ReadTimeout; a steady body is read whole.
func TestBodyPacing(t *testing.T) {
	defer func(w time.Duration, m int64) { bodyPaceWindow, bodyPaceMin = w, m }(bodyPaceWindow, bodyPaceMin)
	bodyPaceWindow, bodyPaceMin = 300*time.Millisecond, 1000
	s := &Server{}
	h := s.netGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "reading body: "+err.Error())
			return
		}
		fmt.Fprintf(w, "%d", n)
	}))
	hs := httptest.NewUnstartedServer(h)
	hs.Config.ReadTimeout = 30 * time.Second
	hs.Start()
	defer hs.Close()

	c, err := net.Dial("tcp", hs.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	fmt.Fprintf(c, "POST / HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100000\r\n\r\n")
	go func() {
		for i := 0; i < 100; i++ { // 10 bytes every 100 ms: 100 B/s
			if _, err := c.Write([]byte("          ")); err != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, _ := bufio.NewReader(c).ReadString('\n')
	if took := time.Since(start); !strings.HasPrefix(line, "HTTP/1.1 400") || took > 3*time.Second {
		t.Fatalf("drip body: %q after %v", line, took)
	}

	// 2000 bytes every 100 ms keeps going, for longer than one window.
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < 10; i++ {
			_, _ = pw.Write([]byte(strings.Repeat("x", 2000)))
			time.Sleep(100 * time.Millisecond)
		}
		pw.Close()
	}()
	req, _ := http.NewRequest(http.MethodPost, hs.URL, pr)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "20000" {
		t.Fatalf("steady body: %d %s", resp.StatusCode, b)
	}
}

// Behind a trusted proxy, requests in flight are capped per client address.
func TestClientLimitBehindProxy(t *testing.T) {
	s := &Server{cfg: Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, MaxRequestsPerClient: 1}}
	release := make(chan struct{})
	var entered sync.WaitGroup
	entered.Add(1)
	h := s.clientAddr(s.clientLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Block") != "" {
			entered.Done()
			<-release
		}
	})))
	hs := httptest.NewServer(h)
	defer hs.Close()
	do := func(client string, block bool) int {
		req, _ := http.NewRequest(http.MethodGet, hs.URL, nil)
		req.Header.Set("X-Forwarded-For", client)
		if block {
			req.Header.Set("X-Block", "1")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	go do("203.0.113.7", true)
	entered.Wait()
	if code := do("203.0.113.7", false); code != http.StatusTooManyRequests {
		t.Fatalf("second request in flight from the client: %d", code)
	}
	if code := do("203.0.113.8", false); code != 200 {
		t.Fatalf("another client behind the proxy: %d", code)
	}
	close(release)
}
