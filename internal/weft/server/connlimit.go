package server

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// Connection and request-body limits (D2): a client that opens many
// connections, or sends a request body a few bytes at a time, ties up a
// connection (and its goroutine and buffers) each. The listener caps open
// connections server wide and per client address; the request body must
// keep arriving (bodyPaceMin bytes every bodyPaceWindow) or the read times
// out, besides the server's whole-request ReadTimeout.

// LimitListener returns l accepting at most max open connections (0: no
// limit; Accept waits for one to close) and at most perIP from one
// address (0: no limit; more are closed at once). Connections from a
// trusted proxy are not capped per address: they carry many clients, which
// clientLimit caps per request instead.
func LimitListener(l net.Listener, max, perIP int, trusted []netip.Prefix, log *slog.Logger) net.Listener {
	if log == nil {
		log = slog.Default()
	}
	ll := &limitListener{Listener: l, perIP: perIP, trusted: trusted, byIP: map[netip.Addr]int{}, log: log}
	if max > 0 {
		ll.sem = make(chan struct{}, max)
	}
	return ll
}

type limitListener struct {
	net.Listener
	sem     chan struct{}
	perIP   int
	trusted []netip.Prefix
	log     *slog.Logger

	mu         sync.Mutex
	byIP       map[netip.Addr]int
	refused    int
	lastWarned time.Time
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		if l.sem != nil {
			l.sem <- struct{}{}
		}
		c, err := l.Listener.Accept()
		if err != nil {
			l.release()
			return nil, err
		}
		ip, ok := l.admit(c)
		if !ok {
			_ = c.Close()
			l.release()
			continue
		}
		return &limitConn{Conn: c, l: l, ip: ip}, nil
	}
}

func (l *limitListener) release() {
	if l.sem != nil {
		<-l.sem
	}
}

// admit counts c against its address's cap.
func (l *limitListener) admit(c net.Conn) (netip.Addr, bool) {
	if l.perIP <= 0 {
		return netip.Addr{}, true
	}
	ap, err := netip.ParseAddrPort(c.RemoteAddr().String())
	if err != nil {
		return netip.Addr{}, true
	}
	ip := ap.Addr().Unmap()
	for _, p := range l.trusted {
		if p.Contains(ip) {
			return netip.Addr{}, true
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byIP[ip] >= l.perIP {
		l.refused++
		if time.Since(l.lastWarned) > time.Minute {
			l.log.Warn("connections refused: too many open connections from one address", "remote", ip.String(),
				"max_per_address", l.perIP, "refused_since_last_warning", l.refused)
			l.lastWarned, l.refused = time.Now(), 0
		}
		return netip.Addr{}, false
	}
	l.byIP[ip]++
	return ip, true
}

func (l *limitListener) done(ip netip.Addr) {
	if ip.IsValid() {
		l.mu.Lock()
		if l.byIP[ip]--; l.byIP[ip] <= 0 {
			delete(l.byIP, ip)
		}
		l.mu.Unlock()
	}
	l.release()
}

type limitConn struct {
	net.Conn
	l    *limitListener
	ip   netip.Addr
	once sync.Once
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.l.done(c.ip) })
	return err
}

// clientLimit caps the requests in flight per client address for requests
// that came through a trusted proxy (whose connections the listener does
// not cap per address): 429 beyond Config.MaxRequestsPerClient.
func (s *Server) clientLimit(next http.Handler) http.Handler {
	if s.cfg.MaxRequestsPerClient <= 0 || len(s.cfg.TrustedProxies) == 0 {
		return next
	}
	var mu sync.Mutex
	inFlight := map[string]int{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !viaTrustedProxy(r) {
			next.ServeHTTP(w, r)
			return
		}
		ip := clientIP(r)
		mu.Lock()
		if inFlight[ip] >= s.cfg.MaxRequestsPerClient {
			mu.Unlock()
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "too many requests in flight from this address")
			return
		}
		inFlight[ip]++
		mu.Unlock()
		defer func() {
			mu.Lock()
			if inFlight[ip]--; inFlight[ip] <= 0 {
				delete(inFlight, ip)
			}
			mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}

// Request-body pacing: every bodyPaceWindow the body must have delivered
// bodyPaceMin more bytes (about 1 KB/s), or the next read fails with a
// timeout (the request gets a 400 and the connection is closed); the
// whole request stays bounded by the server's ReadTimeout (bodyReadTotal).
var (
	bodyPaceWindow = 30 * time.Second
	bodyPaceMin    = int64(32 << 10)
	bodyReadTotal  = 2 * time.Minute
)

// paceBody arms the read deadline of r's body (netGuard).
func paceBody(r *http.Request, rc *http.ResponseController) *http.Request {
	if !hasBody(r) {
		return r
	}
	now := time.Now()
	p := &pacedBody{ReadCloser: r.Body, rc: rc, until: now.Add(bodyReadTotal)}
	if !p.arm(now) {
		return r // the writer cannot set read deadlines (tests)
	}
	r2 := *r
	r2.Body = p
	return &r2
}

type pacedBody struct {
	io.ReadCloser
	rc     *http.ResponseController
	until  time.Time
	window int64
	done   bool
}

func (p *pacedBody) arm(now time.Time) bool {
	d := now.Add(bodyPaceWindow)
	if d.After(p.until) {
		d = p.until
	}
	return p.rc.SetReadDeadline(d) == nil
}

func (p *pacedBody) Read(b []byte) (int, error) {
	n, err := p.ReadCloser.Read(b)
	if p.done {
		return n, err
	}
	if err != nil { // at EOF net/http clears the deadline itself
		p.done = true
		return n, err
	}
	if p.window += int64(n); p.window >= bodyPaceMin {
		p.window = 0
		p.arm(time.Now())
	}
	return n, err
}
