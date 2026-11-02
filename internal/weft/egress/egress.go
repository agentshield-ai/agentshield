// Package egress restricts the outbound HTTP requests Weft makes on behalf of
// users and projects: remote scorers, remote models, webhooks, OAuth token
// endpoints, LLM provider base URLs and reference-bucket tests.
//
// Every destination IP is checked when the connection is dialled (a
// net.Dialer ControlContext hook), so a hostname that resolves, or later
// re-resolves (DNS rebinding), to an internal address is refused, and so is
// every redirect hop. By default Weft refuses loopback, private (RFC 1918,
// IPv6 ULA), link-local, carrier-grade NAT, unspecified, multicast and
// reserved addresses, cloud metadata endpoints, and the IPv4-mapped, NAT64 and
// 6to4 forms of all of those. This matches what Weave documents for its own
// outbound requests ("loopback, private, internal and cloud metadata addresses
// are rejected"), plus an operator allowlist of hosts, host:port pairs and
// CIDRs (Weave: WF_SCORING_WORKER_REMOTE_SCORER_ALLOWED_HOSTS and
// _ALLOWED_PRIVATE_CIDRS).
//
// Destinations the operator configures on the command line (the PII analyzer,
// the OIDC issuer, the S3/GCS files backend) do not go through this package:
// they are trusted configuration, not user input.
package egress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrBlocked is returned (wrapped) when a destination is not allowed. Its
// message is deliberately generic: callers return it to API clients, and it
// must not reveal which internal address a hostname resolved to.
var ErrBlocked = errors.New("destination not allowed")

// IsBlocked reports whether err comes from a refused destination.
func IsBlocked(err error) bool { return errors.Is(err, ErrBlocked) }

// Sanitize returns ErrBlocked in place of any error caused by a refused
// destination (dropping the URL and resolved address an *url.Error or
// *net.OpError would print), and err unchanged otherwise.
func Sanitize(err error) error {
	if IsBlocked(err) {
		return ErrBlocked
	}
	return err
}

// Config configures a Policy.
type Config struct {
	// Allow lists destinations that are always allowed, even when they are
	// internal: "host" (any port), "host:port", "*.example.com" (subdomains
	// at any depth, not the domain itself), IP literals ("10.0.0.5",
	// "[fd00::1]:8080") and CIDRs ("10.0.0.0/8"). Hostname entries match the
	// name in the URL; IP and CIDR entries match the address dialled.
	Allow []string
	// AllowPrivate allows loopback, private, link-local, CGNAT and reserved
	// addresses. Cloud metadata endpoints, unspecified and multicast
	// addresses stay blocked unless listed in Allow.
	AllowPrivate bool
	// Logger receives a warning for every blocked attempt (default slog).
	Logger *slog.Logger
	// Resolver, when set, resolves hostnames instead of the system resolver
	// (tests).
	Resolver Resolver
}

// Resolver resolves a hostname to addresses.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Policy decides which destinations outbound requests may reach.
type Policy struct {
	allowPrivate bool
	hosts        map[string]bool // host or canonical IP, any port
	hostPorts    map[string]bool // "host:port" (IPv6 bracketed)
	wildcards    []wildcard
	nets         []netip.Prefix
	log          *slog.Logger
	resolver     Resolver
}

type wildcard struct{ suffix, port string } // suffix ".example.com"; port "" = any

// New builds a Policy, validating every allow entry.
func New(cfg Config) (*Policy, error) {
	p := &Policy{allowPrivate: cfg.AllowPrivate, hosts: map[string]bool{}, hostPorts: map[string]bool{},
		log: cfg.Logger, resolver: cfg.Resolver}
	for _, raw := range cfg.Allow {
		if err := p.add(strings.TrimSpace(raw)); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// MustNew is New that panics on a bad entry (tests and constants).
func MustNew(cfg Config) *Policy {
	p, err := New(cfg)
	if err != nil {
		panic(err)
	}
	return p
}

// ParseList splits a comma-separated allow list.
func ParseList(s string) []string {
	var out []string
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

func (p *Policy) add(e string) error {
	if e == "" {
		return nil
	}
	bad := func(why string) error { return fmt.Errorf("egress allow entry %q: %s", e, why) }
	if strings.Contains(e, "://") {
		return bad("use a host, host:port or CIDR, not a URL")
	}
	if strings.Contains(e, "/") {
		pfx, err := netip.ParsePrefix(e)
		if err != nil {
			return bad("invalid CIDR")
		}
		if pfx.Addr().Is4In6() {
			pfx = netip.PrefixFrom(pfx.Addr().Unmap(), max(pfx.Bits()-96, 0))
		}
		p.nets = append(p.nets, pfx.Masked())
		return nil
	}
	host, port := e, ""
	if h, pt, err := net.SplitHostPort(e); err == nil {
		host, port = h, pt
	} else if strings.HasPrefix(e, "[") && strings.HasSuffix(e, "]") {
		host = e[1 : len(e)-1]
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return bad("invalid port")
		}
	}
	host = canonicalHost(host)
	if host == "" {
		return bad("empty host")
	}
	if strings.HasPrefix(host, "*.") {
		dom := host[1:]
		if len(dom) < 2 || strings.Contains(dom[1:], "*") {
			return bad("invalid wildcard")
		}
		p.wildcards = append(p.wildcards, wildcard{suffix: dom, port: port})
		return nil
	}
	if strings.Contains(host, "*") {
		return bad("a wildcard must be a leading \"*.\"")
	}
	if port == "" {
		p.hosts[host] = true
	} else {
		p.hostPorts[net.JoinHostPort(host, port)] = true
	}
	return nil
}

// canonicalHost lowercases a hostname, drops a trailing dot and IPv6
// brackets, and renders IP literals canonically (IPv4-mapped as IPv4).
func canonicalHost(h string) string {
	h = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(h), "["), "]")
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if i := strings.IndexByte(h, '%'); i >= 0 { // zone
		h = h[:i]
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Unmap().String()
	}
	return h
}

// hostAllowed reports whether the URL host (a name or IP literal) is on the
// allowlist.
func (p *Policy) hostAllowed(host, port string) bool {
	host = canonicalHost(host)
	if p.hosts[host] || p.hostPorts[net.JoinHostPort(host, port)] {
		return true
	}
	for _, w := range p.wildcards {
		if strings.HasSuffix(host, w.suffix) && len(host) > len(w.suffix) && (w.port == "" || w.port == port) {
			return true
		}
	}
	return false
}

// ipAllowed reports whether a dialled address is explicitly allowed.
func (p *Policy) ipAllowed(ip netip.Addr, port string) bool {
	ip = ip.Unmap()
	if p.hosts[ip.String()] || p.hostPorts[net.JoinHostPort(ip.String(), port)] {
		return true
	}
	for _, n := range p.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Category names why an address is internal ("" for a public address).
type Category string

const (
	Public      Category = ""
	Metadata    Category = "cloud-metadata"
	Unspecified Category = "unspecified"
	Loopback    Category = "loopback"
	Private     Category = "private"
	LinkLocal   Category = "link-local"
	CGNAT       Category = "cgnat"
	Multicast   Category = "multicast"
	Reserved    Category = "reserved"
)

var metadataAddrs = map[netip.Addr]bool{
	netip.MustParseAddr("169.254.169.254"): true, // AWS, GCP, Azure, OpenStack, ...
	netip.MustParseAddr("169.254.170.2"):   true, // AWS ECS task metadata
	netip.MustParseAddr("169.254.170.23"):  true, // AWS EKS pod identity
	netip.MustParseAddr("100.100.100.200"): true, // Alibaba Cloud
	netip.MustParseAddr("fd00:ec2::254"):   true, // AWS IMDS over IPv6
	netip.MustParseAddr("fd00:ec2::23"):    true, // AWS EKS pod identity over IPv6
}

type rangeCat struct {
	p netip.Prefix
	c Category
}

var ranges = func() []rangeCat {
	var out []rangeCat
	for _, r := range []struct {
		cidr string
		c    Category
	}{
		{"0.0.0.0/8", Unspecified}, {"::/128", Unspecified},
		{"127.0.0.0/8", Loopback}, {"::1/128", Loopback},
		{"10.0.0.0/8", Private}, {"172.16.0.0/12", Private}, {"192.168.0.0/16", Private},
		{"fc00::/7", Private}, {"fec0::/10", Private}, // ULA; deprecated site-local
		{"169.254.0.0/16", LinkLocal}, {"fe80::/10", LinkLocal},
		{"100.64.0.0/10", CGNAT},
		{"224.0.0.0/4", Multicast}, {"ff00::/8", Multicast},
		{"240.0.0.0/4", Reserved}, // includes 255.255.255.255
		{"192.0.0.0/24", Reserved}, {"198.18.0.0/15", Reserved},
		{"100::/64", Reserved}, // discard-only
	} {
		out = append(out, rangeCat{netip.MustParsePrefix(r.cidr), r.c})
	}
	return out
}()

var (
	nat64   = netip.MustParsePrefix("64:ff9b::/96")
	sixTo4  = netip.MustParsePrefix("2002::/16")
	v4Compt = netip.MustParsePrefix("::/96")
)

// Classify names the internal category of ip, or Public. IPv4-mapped
// (::ffff:a.b.c.d), IPv4-compatible (::a.b.c.d), NAT64 (64:ff9b::/96) and 6to4
// (2002::/16) addresses are classified by the IPv4 address they embed.
func Classify(ip netip.Addr) Category {
	ip = ip.Unmap()
	if metadataAddrs[ip] {
		return Metadata
	}
	if ip.Is6() {
		b := ip.As16()
		var emb netip.Addr
		switch {
		case nat64.Contains(ip):
			emb = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
		case sixTo4.Contains(ip):
			emb = netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]})
		case v4Compt.Contains(ip) && ip != netip.IPv6Unspecified() && ip != netip.IPv6Loopback():
			emb = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
		}
		if emb.IsValid() {
			if c := Classify(emb); c != Public {
				return c
			}
		}
	}
	for _, r := range ranges {
		if r.p.Contains(ip) {
			return r.c
		}
	}
	return Public
}

// Check returns nil when ip:port may be dialled, and an error wrapping
// ErrBlocked otherwise. host is the name from the URL, for the log only.
func (p *Policy) Check(host string, ip netip.Addr, port string) error {
	if p.ipAllowed(ip, port) {
		return nil
	}
	c := Classify(ip)
	if c == Public {
		return nil
	}
	if p.allowPrivate && c != Metadata && c != Unspecified && c != Multicast {
		return nil
	}
	l := p.log
	if l == nil {
		l = slog.Default()
	}
	l.Warn("egress: blocked outbound request", "host", host, "ip", ip.String(), "port", port, "reason", string(c),
		"hint", "allow it with --egress-allow (WEFT_EGRESS_ALLOW) or --egress-allow-private")
	return &blockedError{category: c}
}

type blockedError struct{ category Category }

func (e *blockedError) Error() string { return ErrBlocked.Error() }
func (e *blockedError) Unwrap() error { return ErrBlocked }

// CheckURL checks a URL before a request is made: the scheme must be http or
// https, and the host (resolved now) must be allowed. Dial-time checks still
// apply; this gives callers an early, clean error.
func (p *Policy) CheckURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("invalid URL: must be an absolute http(s) URL")
	}
	return p.checkHost(ctx, u.Hostname(), portOf(u))
}

func portOf(u *url.URL) string {
	if pt := u.Port(); pt != "" {
		return pt
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// checkHost resolves host and checks every address.
func (p *Policy) checkHost(ctx context.Context, host, port string) error {
	if p.hostAllowed(host, port) {
		return nil
	}
	if ip, err := netip.ParseAddr(canonicalHost(host)); err == nil {
		return p.Check(host, ip, port)
	}
	addrs, err := p.lookup(ctx, host)
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if err := p.Check(host, a, port); err != nil {
			return err
		}
	}
	return nil
}

func (p *Policy) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if p.resolver != nil {
		return p.resolver.LookupNetIP(ctx, "ip", host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// ---------------------------------------------------------------- default policy

var current atomic.Pointer[Policy]

func init() { current.Store(MustNew(Config{})) }

// SetDefault replaces the process-wide policy used by every client from
// NewClient/NewTransport. It returns the previous policy.
func SetDefault(p *Policy) *Policy {
	if p == nil {
		p = MustNew(Config{})
	}
	return current.Swap(p)
}

// Default returns the process-wide policy.
func Default() *Policy { return current.Load() }

// ---------------------------------------------------------------- transport

// proxies records proxy addresses chosen by the transport's Proxy function:
// the proxy is operator configuration (HTTPS_PROXY), so dialling it is not
// checked, and the destination is checked before the proxy is used.
var proxies sync.Map // "host:port" -> true

// DialContext dials addr after checking every address it connects to against
// the default policy. Allowed hostnames and the configured HTTP proxy are
// dialled without checks.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return dial(ctx, Default(), network, addr)
}

var baseDialer = net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

func dial(ctx context.Context, p *Policy, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if _, ok := proxies.Load(addr); ok || p.hostAllowed(host, port) {
		return baseDialer.DialContext(ctx, network, addr)
	}
	if ip, err := netip.ParseAddr(canonicalHost(host)); err == nil {
		// Literal addresses are refused before a socket is even created.
		if err := p.Check(host, ip, port); err != nil {
			return nil, err
		}
	}
	d := baseDialer
	d.ControlContext = func(_ context.Context, _ string, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return &blockedError{category: Reserved}
		}
		return p.Check(host, ap.Addr(), strconv.Itoa(int(ap.Port())))
	}
	if p.resolver != nil {
		if _, err := netip.ParseAddr(canonicalHost(host)); err != nil {
			addrs, err := p.resolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			var first error
			for _, a := range addrs {
				c, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
				if err == nil {
					return c, nil
				}
				if first == nil {
					first = err
				}
			}
			if first == nil {
				first = fmt.Errorf("no addresses for %s", host)
			}
			return nil, first
		}
	}
	return d.DialContext(ctx, network, addr)
}

// proxyFromEnvironment picks the HTTP(S) proxy (a variable for tests).
var proxyFromEnvironment = http.ProxyFromEnvironment

// NewTransport returns an HTTP transport whose connections are checked
// against the default policy at dial time (so redirects and DNS rebinding are
// covered). With an HTTP(S) proxy from the environment, the destination is
// resolved and checked before the request is handed to the proxy.
func NewTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = DialContext
	t.Proxy = func(req *http.Request) (*url.URL, error) {
		pu, err := proxyFromEnvironment(req)
		if err != nil || pu == nil {
			return pu, err
		}
		// The proxy resolves the name itself. Check what it resolves to here;
		// if this host cannot resolve it (DNS only through the proxy), the
		// proxy, which the operator configured, decides.
		if err := Default().checkHost(req.Context(), req.URL.Hostname(), portOf(req.URL)); err != nil && IsBlocked(err) {
			return nil, err
		}
		proxies.Store(net.JoinHostPort(pu.Hostname(), portOf(pu)), true)
		return pu, nil
	}
	return t
}

// sanitizingTransport replaces blocked-destination errors with ErrBlocked so
// no resolved address leaks into error text.
type sanitizingTransport struct{ http.RoundTripper }

func (t sanitizingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.RoundTripper.RoundTrip(r)
	return resp, Sanitize(err)
}

// NewClient returns an *http.Client using NewTransport. Redirects are followed
// (each hop is dialled through the guard) unless followRedirects is false, in
// which case a 3xx response is returned as is.
func NewClient(timeout time.Duration, followRedirects bool) *http.Client {
	c := &http.Client{Timeout: timeout, Transport: sanitizingTransport{NewTransport()}}
	if !followRedirects {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c
}

// HostPortOf returns "host:port" for an absolute URL (for allowing an
// operator-configured base URL), or "" when raw is not one.
func HostPortOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return net.JoinHostPort(u.Hostname(), portOf(u))
}
