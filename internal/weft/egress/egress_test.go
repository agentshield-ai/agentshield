package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		ip   string
		want Category
	}{
		{"8.8.8.8", Public},
		{"1.1.1.1", Public},
		{"2606:4700:4700::1111", Public},
		{"127.0.0.1", Loopback},
		{"127.255.0.9", Loopback},
		{"::1", Loopback},
		{"10.0.0.1", Private},
		{"172.16.0.1", Private},
		{"172.31.255.255", Private},
		{"172.32.0.1", Public},
		{"192.168.1.1", Private},
		{"fc00::1", Private},
		{"fd12:3456::1", Private},
		{"169.254.1.1", LinkLocal},
		{"fe80::1", LinkLocal},
		{"169.254.169.254", Metadata},
		{"169.254.170.2", Metadata},
		{"fd00:ec2::254", Metadata},
		{"100.100.100.200", Metadata},
		{"100.64.0.1", CGNAT},
		{"100.127.255.254", CGNAT},
		{"0.0.0.0", Unspecified},
		{"0.1.2.3", Unspecified},
		{"::", Unspecified},
		{"224.0.0.1", Multicast},
		{"ff02::1", Multicast},
		{"255.255.255.255", Reserved},
		{"240.0.0.1", Reserved},
		{"198.18.0.1", Reserved},
		// IPv4-mapped, IPv4-compatible, NAT64 and 6to4 forms
		{"::ffff:127.0.0.1", Loopback},
		{"::ffff:10.1.2.3", Private},
		{"::ffff:169.254.169.254", Metadata},
		{"::ffff:8.8.8.8", Public},
		{"::127.0.0.1", Loopback},
		{"64:ff9b::a00:1", Private},            // 10.0.0.1
		{"64:ff9b::a9fe:a9fe", Metadata},       // 169.254.169.254
		{"64:ff9b::808:808", Public},           // 8.8.8.8
		{"2002:7f00:1::1", Loopback},           // 127.0.0.1
		{"2002:c0a8:101::1", Private},          // 192.168.1.1
		{"2002:808:808::1", Public},            // 8.8.8.8
		{"::ffff:100.64.1.1", CGNAT},           // mapped CGNAT
		{"::ffff:0.0.0.0", Unspecified},        // mapped unspecified
		{"::ffff:224.0.0.251", Multicast},      // mapped multicast
		{"::ffff:255.255.255.255", Reserved},   // mapped broadcast
		{"::ffff:172.20.0.5", Private},         // mapped RFC 1918
		{"::ffff:192.168.0.1", Private},        // mapped RFC 1918
		{"::ffff:169.254.0.1", LinkLocal},      // mapped link-local
		{"::ffff:127.1.2.3", Loopback},         // mapped loopback range
		{"64:ff9b::7f00:1", Loopback},          // NAT64 loopback
		{"64:ff9b::6440:1", CGNAT},             // NAT64 CGNAT 100.64.0.1
		{"2002:a9fe:a9fe::1", Metadata},        // 6to4 metadata
		{"fd00:ec2:0:0:0:0:0:254", Metadata},   // uncompressed IMDS
		{"FD00:EC2::254", Metadata},            // upper case
		{"fe80::a9fe:a9fe", LinkLocal},         // link-local v6
		{"2001:db8::1", Public},                // documentation, not internal
		{"::ffff:1.2.3.4", Public},             // mapped public
		{"64:ff9b::102:304", Public},           // NAT64 public 1.2.3.4
		{"2002:102:304::", Public},             // 6to4 public
		{"fec0::1", Private},                   // deprecated site-local
		{"100::1", Reserved},                   // discard-only
		{"192.0.0.8", Reserved},                // IETF protocol assignments
		{"172.15.255.255", Public},             // just below 172.16/12
		{"100.63.255.255", Public},             // just below CGNAT
		{"100.128.0.0", Public},                // just above CGNAT
		{"11.0.0.1", Public},                   // just above 10/8
		{"192.169.0.1", Public},                // just above 192.168/16
		{"126.255.255.255", Public},            // just below loopback
		{"128.0.0.1", Public},                  // just above loopback
		{"169.253.255.255", Public},            // just below link-local
		{"169.255.0.1", Public},                // just above link-local
		{"223.255.255.255", Public},            // just below multicast
		{"febf::1", LinkLocal},                 // top of fe80::/10
		{"fbff::1", Public},                    // just below fc00::/7
		{"fe00::1", Public},                    // between ULA and link-local
		{"::2", Unspecified},                   // IPv4-compatible 0.0.0.2
		{"::8.8.8.8", Public},                  // IPv4-compatible public
		{"::ffff:a9fe:a9fe", Metadata},         // mapped metadata, hex form
		{"::ffff:7f00:1", Loopback},            // mapped loopback, hex form
		{"0:0:0:0:0:ffff:7f00:0001", Loopback}, // fully expanded mapped
		{"0000:0000:0000:0000:0000:0000:0000:0001", Loopback},
	}
	for _, c := range cases {
		t.Run(c.ip, func(t *testing.T) {
			if got := Classify(netip.MustParseAddr(c.ip)); got != c.want {
				t.Fatalf("Classify(%s) = %q, want %q", c.ip, got, c.want)
			}
		})
	}
}

func TestCheckAllowFlags(t *testing.T) {
	ip := netip.MustParseAddr
	cases := []struct {
		name  string
		cfg   Config
		addr  string
		port  string
		allow bool
	}{
		{"default public", Config{}, "93.184.216.34", "443", true},
		{"default loopback", Config{}, "127.0.0.1", "80", false},
		{"default ipv6 loopback", Config{}, "::1", "80", false},
		{"default mapped loopback", Config{}, "::ffff:127.0.0.1", "80", false},
		{"default private", Config{}, "10.1.2.3", "80", false},
		{"default ula", Config{}, "fd12::1", "80", false},
		{"default metadata", Config{}, "169.254.169.254", "80", false},
		{"default metadata v6", Config{}, "fd00:ec2::254", "80", false},
		{"default cgnat", Config{}, "100.64.0.1", "80", false},
		{"default unspecified", Config{}, "0.0.0.0", "80", false},
		{"default multicast", Config{}, "239.1.1.1", "80", false},
		{"allow ip any port", Config{Allow: []string{"127.0.0.1"}}, "127.0.0.1", "9999", true},
		{"allow ip:port match", Config{Allow: []string{"127.0.0.1:8080"}}, "127.0.0.1", "8080", true},
		{"allow ip:port other port", Config{Allow: []string{"127.0.0.1:8080"}}, "127.0.0.1", "8081", false},
		{"allow v6 bracketed port", Config{Allow: []string{"[::1]:8080"}}, "::1", "8080", true},
		{"allow v6 bare", Config{Allow: []string{"::1"}}, "::1", "1", true},
		{"allow v4 matches mapped", Config{Allow: []string{"127.0.0.1"}}, "::ffff:127.0.0.1", "80", true},
		{"allow cidr", Config{Allow: []string{"10.0.0.0/8"}}, "10.9.9.9", "80", true},
		{"allow cidr miss", Config{Allow: []string{"10.0.0.0/8"}}, "192.168.0.1", "80", false},
		{"allow v6 cidr", Config{Allow: []string{"fd00::/8"}}, "fd12::1", "80", true},
		{"allow mapped cidr", Config{Allow: []string{"::ffff:10.0.0.0/104"}}, "10.0.0.7", "80", true},
		{"allow metadata explicitly", Config{Allow: []string{"169.254.169.254"}}, "169.254.169.254", "80", true},
		{"private flag loopback", Config{AllowPrivate: true}, "127.0.0.1", "80", true},
		{"private flag rfc1918", Config{AllowPrivate: true}, "192.168.1.5", "80", true},
		{"private flag ula", Config{AllowPrivate: true}, "fd12::1", "80", true},
		{"private flag link-local", Config{AllowPrivate: true}, "169.254.10.10", "80", true},
		{"private flag cgnat", Config{AllowPrivate: true}, "100.64.0.1", "80", true},
		{"private flag keeps metadata blocked", Config{AllowPrivate: true}, "169.254.169.254", "80", false},
		{"private flag keeps v6 metadata blocked", Config{AllowPrivate: true}, "fd00:ec2::254", "80", false},
		{"private flag keeps mapped metadata blocked", Config{AllowPrivate: true}, "::ffff:169.254.169.254", "80", false},
		{"private flag keeps unspecified blocked", Config{AllowPrivate: true}, "0.0.0.0", "80", false},
		{"private flag keeps multicast blocked", Config{AllowPrivate: true}, "224.0.0.1", "80", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.cfg.Logger = discardLogger()
			p := MustNew(c.cfg)
			err := p.Check("h", ip(c.addr), c.port)
			if (err == nil) != c.allow {
				t.Fatalf("Check(%s:%s) err=%v, want allow=%v", c.addr, c.port, err, c.allow)
			}
			if err != nil && (!IsBlocked(err) || err.Error() != "destination not allowed") {
				t.Fatalf("error %q is not the generic blocked error", err)
			}
		})
	}
}

func TestAllowEntryParsing(t *testing.T) {
	good := []string{"localhost", "LOCALHOST.", "localhost:11434", "*.internal.example", "*.svc:8080", "10.0.0.0/8",
		"fd00::/8", "[::1]", "[::1]:80", "::1", "127.0.0.1:6519", " api.example.com "}
	for _, e := range good {
		if _, err := New(Config{Allow: []string{e}}); err != nil {
			t.Errorf("New(%q): %v", e, err)
		}
	}
	bad := []string{"http://localhost", "10.0.0.0/33", "host:0", "host:99999", "host:abc", "foo*.bar", "*.", "a.*.b"}
	for _, e := range bad {
		if _, err := New(Config{Allow: []string{e}}); err == nil {
			t.Errorf("New(%q) accepted a bad entry", e)
		}
	}
	if got := ParseList(" a, ,b:1 ,10.0.0.0/8,"); strings.Join(got, "|") != "a|b:1|10.0.0.0/8" {
		t.Errorf("ParseList = %q", got)
	}
}

func TestHostAllowlist(t *testing.T) {
	p := MustNew(Config{Allow: []string{"localhost:11434", "*.svc.cluster.local", "Ollama.Internal", "*.mock:8080"}})
	cases := []struct {
		host, port string
		want       bool
	}{
		{"localhost", "11434", true},
		{"localhost", "11435", false},
		{"LOCALHOST.", "11434", true},
		{"vllm.default.svc.cluster.local", "8000", true},
		{"a.b.svc.cluster.local", "1", true},
		{"svc.cluster.local", "80", false}, // the domain itself is not covered by *.
		{"evilsvc.cluster.local", "80", false},
		{"ollama.internal", "443", true},
		{"x.mock", "8080", true},
		{"x.mock", "8081", false},
	}
	for _, c := range cases {
		if got := p.hostAllowed(c.host, c.port); got != c.want {
			t.Errorf("hostAllowed(%s, %s) = %v, want %v", c.host, c.port, got, c.want)
		}
	}
}

// ---------------------------------------------------------------- end to end through NewClient

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := f[strings.ToLower(host)]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func withPolicy(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = discardLogger()
	}
	prev := SetDefault(MustNew(cfg))
	t.Cleanup(func() { SetDefault(prev) })
}

func okServer(t *testing.T, network, addr string) *httptest.Server {
	t.Helper()
	l, err := net.Listen(network, addr)
	if err != nil {
		t.Skipf("cannot listen on %s %s: %v", network, addr, err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "reached")
	}))
	srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, raw string) (string, error) {
	t.Helper()
	c := NewClient(5*time.Second, true)
	resp, err := c.Get(raw)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

func port(t *testing.T, srv *httptest.Server) string {
	u, _ := url.Parse(srv.URL)
	return u.Port()
}

func assertBlocked(t *testing.T, err error, leak ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("request reached an internal address")
	}
	if !IsBlocked(err) {
		t.Fatalf("error %v is not ErrBlocked", err)
	}
	clean := Sanitize(err).Error()
	if clean != "destination not allowed" {
		t.Fatalf("sanitized error = %q", clean)
	}
	for _, s := range leak {
		if strings.Contains(err.Error(), s) && s != "" {
			// The url.Error carries the URL the caller asked for, but never a
			// resolved address it did not ask for.
			t.Fatalf("error %q leaks %q", err, s)
		}
	}
}

func TestClientBlocksLoopbackByDefault(t *testing.T) {
	withPolicy(t, Config{})
	srv := okServer(t, "tcp4", "127.0.0.1:0")
	_, err := get(t, srv.URL)
	assertBlocked(t, err)
}

func TestClientBlocksMappedLoopback(t *testing.T) {
	withPolicy(t, Config{})
	srv4 := okServer(t, "tcp4", "127.0.0.1:0")
	for _, h := range []string{"[::ffff:127.0.0.1]", "[::ffff:7f00:1]", "[::127.0.0.1]", "[64:ff9b::7f00:1]"} {
		_, err := get(t, "http://"+h+":"+port(t, srv4)+"/")
		assertBlocked(t, err)
	}
}

func TestClientBlocksIPv6Loopback(t *testing.T) {
	withPolicy(t, Config{})
	_, err := get(t, "http://[::1]:9/")
	assertBlocked(t, err)
	srv6 := okServer(t, "tcp6", "[::1]:0") // skips where IPv6 is unavailable
	_, err = get(t, srv6.URL)
	assertBlocked(t, err)
}

func TestClientBlocksMetadataAndPrivateLiterals(t *testing.T) {
	withPolicy(t, Config{})
	for _, u := range []string{"http://169.254.169.254/latest/meta-data/", "http://[fd00:ec2::254]/", "http://10.255.255.1:81/",
		"http://[::ffff:169.254.169.254]/", "http://0.0.0.0:80/", "http://100.64.0.1/"} {
		_, err := get(t, u)
		assertBlocked(t, err)
	}
}

func TestClientBlocksHostnameResolvingToPrivate(t *testing.T) {
	withPolicy(t, Config{Resolver: fakeResolver{
		"internal.example": {netip.MustParseAddr("10.1.2.3")},
		"meta.example":     {netip.MustParseAddr("169.254.169.254")},
		"mixed.example":    {netip.MustParseAddr("127.0.0.1")},
	}})
	for _, h := range []string{"internal.example", "meta.example", "mixed.example"} {
		_, err := get(t, "http://"+h+"/")
		// The resolved address must not appear in the error text.
		assertBlocked(t, err, "10.1.2.3", "169.254.169.254", "127.0.0.1")
	}
	// The real resolver: localhost resolves to loopback.
	withPolicy(t, Config{})
	srv := okServer(t, "tcp4", "127.0.0.1:0")
	_, err := get(t, "http://localhost:"+port(t, srv)+"/")
	assertBlocked(t, err, "127.0.0.1")
}

func TestClientBlocksRedirectToPrivate(t *testing.T) {
	target := okServer(t, "tcp4", "127.0.0.1:0") // internal, not allowed
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-internal":
			http.Redirect(w, r, target.URL+"/secret", http.StatusFound)
		case "/to-metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		default:
			_, _ = io.WriteString(w, "front")
		}
	}))
	defer front.Close()
	// Only the front server is allowed (by exact host:port).
	withPolicy(t, Config{Allow: []string{"127.0.0.1:" + port(t, front)}})
	if body, err := get(t, front.URL+"/"); err != nil || body != "front" {
		t.Fatalf("allowed host: %q, %v", body, err)
	}
	for _, p := range []string{"/to-internal", "/to-metadata"} {
		_, err := get(t, front.URL+p)
		assertBlocked(t, err)
	}
	// Without following redirects, the 302 comes back and nothing is dialled.
	c := NewClient(5*time.Second, false)
	resp, err := c.Get(front.URL + "/to-internal")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestClientAllowFlags(t *testing.T) {
	srv := okServer(t, "tcp4", "127.0.0.1:0")
	p := port(t, srv)
	for _, cfg := range []Config{
		{Allow: []string{"127.0.0.1"}},
		{Allow: []string{"127.0.0.1:" + p}},
		{Allow: []string{"127.0.0.0/8"}},
		{Allow: []string{"localhost"}},
		{AllowPrivate: true},
	} {
		t.Run(fmt.Sprint(cfg.Allow, cfg.AllowPrivate), func(t *testing.T) {
			withPolicy(t, cfg)
			host := "127.0.0.1"
			if len(cfg.Allow) == 1 && cfg.Allow[0] == "localhost" {
				host = "localhost"
			}
			body, err := get(t, "http://"+host+":"+p+"/")
			if err != nil || body != "reached" {
				t.Fatalf("got %q, %v", body, err)
			}
		})
	}
	// host:port for another port does not open this one.
	withPolicy(t, Config{Allow: []string{"127.0.0.1:1"}})
	_, err := get(t, srv.URL)
	assertBlocked(t, err)
	// --egress-allow-private still refuses metadata.
	withPolicy(t, Config{AllowPrivate: true})
	_, err = get(t, "http://169.254.169.254/")
	assertBlocked(t, err)
}

func TestCheckURL(t *testing.T) {
	p := MustNew(Config{Logger: discardLogger(), Resolver: fakeResolver{"pub.example": {netip.MustParseAddr("93.184.216.34")}}})
	ctx := context.Background()
	if err := p.CheckURL(ctx, "https://pub.example/x"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"http://127.0.0.1/", "http://[::1]:8/", "http://169.254.169.254/"} {
		if err := p.CheckURL(ctx, u); !IsBlocked(err) {
			t.Errorf("CheckURL(%s) = %v", u, err)
		}
	}
	if err := p.CheckURL(ctx, "file:///etc/passwd"); err == nil || IsBlocked(err) {
		t.Errorf("non-http scheme: %v", err)
	}
}

func TestSanitize(t *testing.T) {
	if Sanitize(nil) != nil {
		t.Fatal("nil")
	}
	other := errors.New("boom")
	if Sanitize(other) != other {
		t.Fatal("other errors pass through")
	}
	wrapped := fmt.Errorf("dial tcp 10.0.0.1:80: %w", &blockedError{category: Private})
	if got := Sanitize(wrapped); got != ErrBlocked {
		t.Fatalf("Sanitize = %v", got)
	}
}

func TestHostPortOf(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:11434/v1": "localhost:11434",
		"https://api.example.com":   "api.example.com:443",
		"http://[::1]/v1":           "[::1]:80",
		"not a url":                 "",
		"":                          "",
	} {
		if got := HostPortOf(in); got != want {
			t.Errorf("HostPortOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Through an HTTP proxy the dial goes to the proxy, so the destination is
// resolved and checked before the request is handed over.
func TestClientChecksDestinationBehindProxy(t *testing.T) {
	var seen []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Host)
		_, _ = io.WriteString(w, "via-proxy")
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	prevProxy := proxyFromEnvironment
	proxyFromEnvironment = func(*http.Request) (*url.URL, error) { return pu, nil }
	defer func() { proxyFromEnvironment = prevProxy }()
	withPolicy(t, Config{Resolver: fakeResolver{
		"pub.example": {netip.MustParseAddr("93.184.216.34")},
		"int.example": {netip.MustParseAddr("10.0.0.1")},
	}})
	// The proxy itself is on loopback: operator configuration, not checked.
	if body, err := get(t, "http://pub.example/x"); err != nil || body != "via-proxy" {
		t.Fatalf("public via proxy: %q %v", body, err)
	}
	_, err := get(t, "http://int.example/")
	assertBlocked(t, err, "10.0.0.1") // the resolved address is not revealed
	for _, u := range []string{"http://169.254.169.254/latest/meta-data/", "http://[::ffff:10.0.0.1]/"} {
		_, err := get(t, u)
		assertBlocked(t, err)
	}
	// A name the server cannot resolve cannot be checked: refused (fail
	// closed) unless the operator allows it by name.
	_, err = get(t, "http://only-the-proxy-knows.example/")
	assertBlocked(t, err)
	if len(seen) != 1 {
		t.Fatalf("proxy saw %v", seen)
	}
	withPolicy(t, Config{Allow: []string{"only-the-proxy-knows.example"}, Resolver: fakeResolver{}})
	if body, err := get(t, "http://only-the-proxy-knows.example/"); err != nil || body != "via-proxy" {
		t.Fatalf("allowed unresolvable via proxy: %q %v", body, err)
	}
}

// The proxy's own address is only reachable as the proxy for a request that
// uses it. A request that targets the proxy address directly (NO_PROXY
// excludes loopback) is checked like any other, even after earlier requests
// went through the proxy (audit 3, finding 1).
func TestProxyAddressNotReachableDirectly(t *testing.T) {
	var direct int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			direct++ // a proxied request carries an absolute URL
		}
		_, _ = io.WriteString(w, "proxy")
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	prevProxy := proxyFromEnvironment
	proxyFromEnvironment = func(r *http.Request) (*url.URL, error) {
		if r.URL.Hostname() == pu.Hostname() {
			return nil, nil // like NO_PROXY for loopback
		}
		return pu, nil
	}
	defer func() { proxyFromEnvironment = prevProxy }()
	withPolicy(t, Config{Resolver: fakeResolver{"pub.example": {netip.MustParseAddr("93.184.216.34")}}})
	for i := 0; i < 2; i++ {
		if body, err := get(t, "http://pub.example/x"); err != nil || body != "proxy" {
			t.Fatalf("public via proxy: %q %v", body, err)
		}
		_, err := get(t, proxy.URL+"/__status")
		assertBlocked(t, err)
	}
	if direct != 0 {
		t.Fatalf("proxy address reached directly %d times", direct)
	}
}

// Without a proxy, a name that resolves to both public and internal
// addresses is refused, as it is behind a proxy (audit 3, finding 3).
func TestMixedResolutionRefusedDirect(t *testing.T) {
	prevProxy := proxyFromEnvironment
	proxyFromEnvironment = func(*http.Request) (*url.URL, error) { return nil, nil }
	defer func() { proxyFromEnvironment = prevProxy }()
	withPolicy(t, Config{Resolver: fakeResolver{
		"mixed.example": {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.7")},
	}})
	_, err := get(t, "http://mixed.example/")
	assertBlocked(t, err, "10.0.0.7")
}
