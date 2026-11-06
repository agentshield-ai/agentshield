package server

import (
	"context"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Network-level request checks, applied to every request before routing
// (docs/weft/API.md "Browsers, hosts and proxies"):
//
//   - Host allowlist (DNS rebinding): with Config.EnforceHosts, the Host
//     header must be an IP address, localhost (or *.localhost), the listen
//     address's host name, the SSO redirect URL's host or one of
//     Config.AllowedHosts (--allowed-hosts; ".example.com" matches
//     subdomains, "*" turns the check off). Others get 421.
//   - Origin: a state-changing request (not GET/HEAD/OPTIONS) whose Origin
//     header is present must come from this server's own origin or from one
//     of --cors-origins, whatever the credential (403). Clients that send no
//     Origin (SDKs, plugins, curl, collectors) are unaffected.
//   - Content type: a state-changing request with a body must say what it
//     sends: application/json (or +json) on JSON routes, protobuf or JSON on
//     OTLP routes, or multipart/form-data (uploads). Anything else, notably
//     text/plain and form encoding, which browsers may send cross-site
//     without a CORS preflight, gets 415.
//   - Client address: the socket peer, or, only when the peer is one of
//     Config.TrustedProxies (--trusted-proxies), the last address in
//     X-Forwarded-For (or X-Real-IP) that is not itself a trusted proxy.
//   - Slow readers: every response write must finish within writeStall.

// writeStall bounds one response write: a client that stops reading is
// cut off, while streams that keep moving run as long as they need.
const writeStall = 2 * time.Minute

func (s *Server) netGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = s.realIP(r)
		if s.cfg.EnforceHosts && !s.hostAllowed(r.Host) {
			s.log.Debug("request refused: host not allowed", "host", clip(r.Host, 100), "remote", clientIP(r))
			writeError(w, http.StatusMisdirectedRequest, "this server does not answer to host "+clip(r.Host, 100)+
				"; add it to --allowed-hosts")
			return
		}
		if !isSafeMethod(r.Method) {
			if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r) && !s.originAllowed(o) {
				writeError(w, http.StatusForbidden, "cross-origin request refused (allow the origin with --cors-origins)")
				return
			}
			if hasBody(r) {
				if msg := contentTypeProblem(r); msg != "" {
					writeError(w, http.StatusUnsupportedMediaType, msg)
					return
				}
			}
		}
		// A deadline left by the previous request on this connection must
		// not cut this one short; the final flush after the handler gets
		// its own.
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Time{})
		next.ServeHTTP(&stallWriter{ResponseWriter: w, rc: rc}, r)
		_ = rc.SetWriteDeadline(time.Now().Add(writeStall))
	})
}

func hasBody(r *http.Request) bool {
	return r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0
}

// contentTypeProblem explains why a request body's content type is
// refused, or returns "".
func contentTypeProblem(r *http.Request) string {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		mt = ""
	}
	isJSON := mt == "application/json" || (strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json"))
	switch {
	case isOTLPPath(r.URL.Path):
		if isJSON || mt == "application/x-protobuf" || mt == "application/protobuf" {
			return ""
		}
		return "send Content-Type: application/x-protobuf or application/json"
	case isJSON, mt == "multipart/form-data":
		return ""
	}
	return "send Content-Type: application/json"
}

// hostAllowed checks a Host header against the allowlist.
func (s *Server) hostAllowed(hostport string) bool {
	if hostport == "" {
		return true
	}
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if _, err := netip.ParseAddr(host); err == nil {
		return true // a rebinding attack needs a name
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	for _, a := range s.allowedHosts() {
		switch {
		case a == "*", a == host:
			return true
		case strings.HasPrefix(a, ".") && strings.HasSuffix(host, a):
			return true
		}
	}
	return false
}

// allowedHosts is Config.AllowedHosts plus the SSO redirect URL's host.
func (s *Server) allowedHosts() []string {
	out := make([]string, 0, len(s.cfg.AllowedHosts)+1)
	for _, h := range s.cfg.AllowedHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		h = strings.TrimPrefix(h, "*")
		if hh, _, err := net.SplitHostPort(h); err == nil {
			h = hh
		}
		if h != "" {
			out = append(out, h)
		}
	}
	if o := s.cfg.IAM.OIDC; o != nil {
		if u, err := url.Parse(o.RedirectURL); err == nil && u.Hostname() != "" {
			out = append(out, strings.ToLower(u.Hostname()))
		}
	}
	return out
}

// realIP sets r.RemoteAddr to the client's address when the peer is a
// trusted proxy; otherwise the peer's address stands.
func (s *Server) realIP(r *http.Request) *http.Request {
	if len(s.cfg.TrustedProxies) == 0 {
		return r
	}
	peer, ok := parseIP(clientIP(r))
	if !ok || !s.trustedProxy(peer) {
		return r
	}
	client := ""
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		parts := strings.Split(strings.Join(xff, ","), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip, ok := parseIP(strings.TrimSpace(parts[i]))
			if !ok {
				break
			}
			client = ip.String()
			if !s.trustedProxy(ip) {
				break
			}
		}
	} else if ip, ok := parseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ok {
		client = ip.String()
	}
	ctx := r.Context()
	if fh := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); fh != "" {
		ctx = context.WithValue(ctx, forwardedHostKey{}, fh)
	}
	r2 := r.Clone(ctx)
	if client != "" {
		r2.RemoteAddr = client
	}
	return r2
}

// forwardedHostKey holds the X-Forwarded-Host of a trusted proxy.
type forwardedHostKey struct{}

func parseIP(v string) (netip.Addr, bool) {
	if h, _, err := net.SplitHostPort(v); err == nil {
		v = h
	}
	ip, err := netip.ParseAddr(strings.Trim(v, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

func (s *Server) trustedProxy(ip netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseTrustedProxies parses --trusted-proxies: IP addresses and CIDR
// prefixes, comma-separated.
func ParseTrustedProxies(v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			p, err := netip.ParsePrefix(part)
			if err != nil {
				return nil, err
			}
			out = append(out, p.Masked())
			continue
		}
		ip, err := netip.ParseAddr(part)
		if err != nil {
			return nil, err
		}
		ip = ip.Unmap()
		out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
	}
	return out, nil
}

// stallWriter gives every write writeStall to complete.
type stallWriter struct {
	http.ResponseWriter
	rc *http.ResponseController
}

func (w *stallWriter) Write(b []byte) (int, error) {
	_ = w.rc.SetWriteDeadline(time.Now().Add(writeStall))
	return w.ResponseWriter.Write(b)
}

func (w *stallWriter) Flush() {
	_ = w.rc.SetWriteDeadline(time.Now().Add(writeStall))
	_ = w.rc.Flush()
}

// Unwrap returns the wrapped writer (http.ResponseController).
func (w *stallWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
