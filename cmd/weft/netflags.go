package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/server"
)

// netFlags configure which hosts and proxies the server trusts
// (internal/weft/server/netguard.go) and its connection timeouts.
type netFlags struct {
	allowedHosts, trustedProxies *string
}

func registerNetFlags() *netFlags {
	return &netFlags{
		allowedHosts: flag.String("allowed-hosts", os.Getenv("WEFT_ALLOWED_HOSTS"),
			"comma-separated host names the server answers to besides IP addresses, localhost and the listen address's name, "+
				"e.g. weft.example.com or .example.com for subdomains; * answers any (env WEFT_ALLOWED_HOSTS). Requests for other hosts get 421 (DNS rebinding)"),
		trustedProxies: flag.String("trusted-proxies", os.Getenv("WEFT_TRUSTED_PROXIES"),
			"comma-separated IP addresses or CIDR prefixes of reverse proxies whose X-Forwarded-For / X-Real-IP / X-Forwarded-Host "+
				"are believed (env WEFT_TRUSTED_PROXIES); otherwise the client address is the socket peer"),
	}
}

// apply fills the host and proxy settings of cfg for a server listening on addr.
func (f *netFlags) apply(cfg *server.Config, addr string) error {
	proxies, err := server.ParseTrustedProxies(*f.trustedProxies)
	if err != nil {
		return fmt.Errorf("invalid --trusted-proxies: %v", err)
	}
	cfg.TrustedProxies = proxies
	cfg.EnforceHosts = true
	cfg.AllowedHosts = splitList(*f.allowedHosts)
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" && net.ParseIP(host) == nil {
		cfg.AllowedHosts = append(cfg.AllowedHosts, host) // the listen address's name
	}
	return nil
}

// newHTTPServer is the http.Server with its timeouts: headers within 10 s,
// the whole request within 5 min (64 MiB uploads on slow links), idle
// keep-alive connections closed after 2 min. Responses have no overall
// deadline, so streams and exports run as long as they move; instead each
// write must finish within 2 min (netguard.go), which cuts off clients
// that stop reading.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 5 * time.Minute,
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
