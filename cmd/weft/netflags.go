package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/server"
)

// netFlags configure which hosts and proxies the server trusts
// (internal/weft/server/netguard.go) and its connection timeouts.
type netFlags struct {
	allowedHosts, trustedProxies *string
	maxConns, maxConnsPerIP      *int
	responseBudgetMB             *int
}

// Default connection caps: --max-connections, --max-connections-per-ip.
const (
	defaultMaxConns      = 4096
	defaultMaxConnsPerIP = 128
)

func registerNetFlags() *netFlags {
	return &netFlags{
		allowedHosts: flag.String("allowed-hosts", os.Getenv("WEFT_ALLOWED_HOSTS"),
			"comma-separated host names the server answers to besides IP addresses, localhost and the listen address's name, "+
				"e.g. weft.example.com or .example.com for subdomains; * answers any (env WEFT_ALLOWED_HOSTS). Requests for other hosts get 421 (DNS rebinding)"),
		trustedProxies: flag.String("trusted-proxies", os.Getenv("WEFT_TRUSTED_PROXIES"),
			"comma-separated IP addresses or CIDR prefixes of reverse proxies whose X-Forwarded-For / X-Real-IP / X-Forwarded-Host "+
				"are believed (env WEFT_TRUSTED_PROXIES); otherwise the client address is the socket peer"),
		maxConns: flag.Int("max-connections", envInt("WEFT_MAX_CONNECTIONS", defaultMaxConns),
			"most open client connections; more wait to be accepted (env WEFT_MAX_CONNECTIONS; 0 for no limit)"),
		maxConnsPerIP: flag.Int("max-connections-per-ip", envInt("WEFT_MAX_CONNECTIONS_PER_IP", defaultMaxConnsPerIP),
			"most open connections from one client address; more are closed at once. Behind --trusted-proxies, the "+
				"requests in flight per client address are capped at the same number instead (env WEFT_MAX_CONNECTIONS_PER_IP; 0 for no limit)"),
		responseBudgetMB: flag.Int("response-budget-mb", envInt("WEFT_RESPONSE_BUDGET_MB", server.DefaultResponseBudget>>20),
			"memory, in MiB, that read responses (span, object, table and feedback lists) may be built in at once, server wide; "+
				"queries wait for it, then get 503 (env WEFT_RESPONSE_BUDGET_MB)"),
	}
}

func envInt(name string, def int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// apply fills the host and proxy settings of cfg for a server listening on addr.
func (f *netFlags) apply(cfg *server.Config, addr string) error {
	proxies, err := server.ParseTrustedProxies(*f.trustedProxies)
	if err != nil {
		return fmt.Errorf("invalid --trusted-proxies: %v", err)
	}
	cfg.TrustedProxies = proxies
	if *f.maxConns < 0 || *f.maxConnsPerIP < 0 || *f.responseBudgetMB < 0 {
		return fmt.Errorf("--max-connections, --max-connections-per-ip and --response-budget-mb must not be negative")
	}
	cfg.MaxRequestsPerClient = *f.maxConnsPerIP
	cfg.ResponseBudget = int64(*f.responseBudgetMB) << 20
	cfg.EnforceHosts = true
	cfg.AllowedHosts = splitList(*f.allowedHosts)
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" && net.ParseIP(host) == nil {
		cfg.AllowedHosts = append(cfg.AllowedHosts, host) // the listen address's name
	}
	return nil
}

// listen opens the listening socket with the connection caps
// (server.LimitListener).
func (f *netFlags) listen(addr string, cfg server.Config, log *slog.Logger) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return server.LimitListener(ln, *f.maxConns, *f.maxConnsPerIP, cfg.TrustedProxies, log), nil
}

// newHTTPServer is the http.Server with its timeouts: headers within 10 s,
// the whole request within 2 min (64 MiB at 0.5 MB/s; a body must also
// keep arriving, about 1 KB/s, netguard.go), idle keep-alive connections
// closed after 2 min. Responses have no overall deadline, so streams and
// exports run as long as they move; instead each write must finish within
// 2 min (netguard.go), which cuts off clients that stop reading.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute,
		IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20}
}

// warnOpenBind warns loudly when the server has no authentication and its
// listen address can be reached from other machines: an unspecified
// address, a non-loopback IP, or a host name that does not resolve only to
// loopback addresses.
func warnOpenBind(addr string, authRequired bool, log *slog.Logger, logsToFile bool) {
	if authRequired {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	exposed, why := bindExposed(host)
	if !exposed {
		return
	}
	msg := "AUTHENTICATION IS DISABLED and the server listens on " + addr + " (" + why + "): anyone who can reach it can read " +
		"and change all data and use the stored provider keys. Set --api-keys (or create an admin key, or configure SSO), " +
		"or listen on 127.0.0.1"
	log.Warn(msg)
	if logsToFile { // also on the terminal, so it is seen at startup
		fmt.Fprintln(os.Stderr, "WARNING: "+msg)
	}
}

// bindExposed reports a listen host reachable from other machines.
func bindExposed(host string) (bool, string) {
	if host == "" {
		return true, "all interfaces"
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() {
			return true, "all interfaces"
		}
		return !ip.IsLoopback(), "a non-loopback address"
	}
	if strings.EqualFold(host, "localhost") {
		return false, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return true, "a host name that does not resolve"
	}
	for _, ip := range ips {
		if !ip.IP.IsLoopback() {
			return true, "a host name with a non-loopback address"
		}
	}
	return false, ""
}
