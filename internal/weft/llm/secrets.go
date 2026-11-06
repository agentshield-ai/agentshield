package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Every outbound use of a stored secret goes through Client.UseSecret, which
// decides whether the secret may be used for this project and destination:
//
//   - visibility: a project sees its own secrets, its team's and the
//     server's (the narrowest scope that has the name wins); bucket
//     credentials are never returned;
//   - server secrets: a built-in provider route (openai/, anthropic/, ...)
//     may use them for any project. A project's own object (custom
//     provider, remote scorer, predict model or runtime, OAuth config,
//     automation) may use one only when the secret lists allowed hosts and
//     the destination is one of them, or while ServerSecretsOpen reports
//     that every principal of the server is a server admin (an open server,
//     static keys, admin keys only: everything is a server admin's object);
//   - allowed hosts: a secret with allowed hosts is only sent there (for a
//     URL-valued secret, the URL itself must be on the list);
//   - transport: a secret is only sent over https, except to a loopback
//     host (local development) or when the operator allows http
//     (AllowInsecureSecretHTTP), and except to provider base URLs the
//     operator configured.

// AllowInsecureSecretHTTP lets stored secrets be sent over plain http to
// non-loopback hosts. cmd/weft sets it from --secrets-allow-insecure-http.
var AllowInsecureSecretHTTP bool

// ErrSecretUnavailable is wrapped by every refusal from UseSecret.
var ErrSecretUnavailable = errors.New("secret unavailable")

// SecretError explains why a secret cannot be used. Its text never contains
// the secret's value.
type SecretError struct {
	Name string
	Msg  string
	// NotFound: no secret of that name is visible to the project.
	NotFound bool
}

func (e *SecretError) Error() string { return e.Msg }
func (e *SecretError) Unwrap() error { return ErrSecretUnavailable }

// IsSecretNotFound reports whether err says no such secret is visible.
func IsSecretNotFound(err error) bool {
	var se *SecretError
	return errors.As(err, &se) && se.NotFound
}

// SecretUse is one use of a stored secret.
type SecretUse struct {
	// ProjectID is the project whose object or request names the secret;
	// "" for server-level uses (model availability), which see server
	// secrets only.
	ProjectID string
	Name      string
	// Dest is the URL the secret is sent to. Empty when the secret's value
	// is itself the destination URL (URLValued).
	Dest      string
	URLValued bool
	// Builtin: named by a built-in provider route, not by a project's
	// object; server secrets are always usable.
	Builtin bool
	// MinScope, when set, requires the secret to be at least this narrow
	// (a key used with a base URL stored in a project must be the
	// project's own).
	MinScope string
	// OperatorDest: the destination is operator configuration (a built-in
	// or environment base URL), so plain http is accepted.
	OperatorDest bool
	// AllowHTTP accepts plain http for this use (an operator flag of the
	// feature, e.g. --remote-scorer-allow-insecure-http).
	AllowHTTP bool
}

// UseSecret resolves a secret for a use, enforcing scope, allowed hosts and
// transport. It returns the value and the record (its scope).
func (c *Client) UseSecret(ctx context.Context, u SecretUse) (string, *store.SecretRecord, error) {
	notFound := func() error {
		where := "this project, its team or the server"
		if u.ProjectID == "" {
			where = "the server"
		}
		return &SecretError{Name: u.Name, NotFound: true, Msg: fmt.Sprintf("secret %q is not set (or not available) in %s", u.Name, where)}
	}
	if c.Secret == nil || u.Name == "" {
		return "", nil, notFound()
	}
	rec, err := c.Secret(ctx, u.ProjectID, u.Name)
	if err != nil {
		return "", nil, fmt.Errorf("reading secret %q: %w", u.Name, err)
	}
	if rec == nil || rec.Value == "" {
		return "", nil, notFound()
	}
	refuse := func(format string, args ...any) error {
		return &SecretError{Name: u.Name, Msg: fmt.Sprintf("secret %q ", u.Name) + fmt.Sprintf(format, args...)}
	}
	if u.MinScope != "" && store.SecretScopeRank(rec.Scope) < store.SecretScopeRank(u.MinScope) {
		return "", nil, refuse("is a %s secret but must be a %s secret here (the base URL it would be sent to is a %s secret)",
			rec.Scope, u.MinScope, u.MinScope)
	}
	dest := u.Dest
	if u.URLValued {
		dest = strings.TrimSpace(rec.Value)
	}
	du, err := url.Parse(strings.TrimSpace(dest))
	if err != nil || (du.Scheme != "http" && du.Scheme != "https") || du.Hostname() == "" {
		if u.URLValued {
			return "", nil, refuse("does not hold an absolute http(s) URL")
		}
		return "", nil, refuse("can only be sent to an absolute http(s) URL")
	}
	host, port := du.Hostname(), du.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[du.Scheme]
	}
	hostOK := len(rec.AllowedHosts) > 0 && store.SecretHostAllowed(rec.AllowedHosts, host, port)
	if len(rec.AllowedHosts) > 0 && !hostOK {
		if u.URLValued {
			return "", nil, refuse("holds a URL whose host is not one of its allowed hosts")
		}
		return "", nil, refuse("may not be sent to %s (its allowed hosts: %s)", host, strings.Join(rec.AllowedHosts, ", "))
	}
	if rec.Scope == store.SecretScopeServer && !u.Builtin && !hostOK && !c.serverSecretsOpen(ctx) {
		return "", nil, refuse("is a server secret: a project can use it only toward the allowed hosts a server admin sets on it. " +
			"Store the value as a project or team secret, or ask a server admin to add allowed hosts")
	}
	if du.Scheme != "https" && !u.OperatorDest && !u.AllowHTTP && !AllowInsecureSecretHTTP && !IsLoopbackHost(host) {
		if u.URLValued {
			return "", nil, refuse("holds an http:// URL; secrets are only used over https (the operator can allow http with --secrets-allow-insecure-http)")
		}
		return "", nil, refuse("is only sent over https (the operator can allow http with --secrets-allow-insecure-http)")
	}
	return rec.Value, rec, nil
}

// LookupSecret returns a stored secret that projectID's object sends to
// dest (see UseSecret). It never reads the server environment.
func (c *Client) LookupSecret(ctx context.Context, projectID, name, dest string) (string, error) {
	v, _, err := c.UseSecret(ctx, SecretUse{ProjectID: projectID, Name: name, Dest: dest})
	return v, err
}

// LookupURLSecret returns a stored secret whose value is the destination
// URL (an automation's url_secret).
func (c *Client) LookupURLSecret(ctx context.Context, projectID, name string) (string, error) {
	v, _, err := c.UseSecret(ctx, SecretUse{ProjectID: projectID, Name: name, URLValued: true})
	return strings.TrimSpace(v), err
}

// CheckCredentialTransport refuses sending a credential (a stored secret or
// a token obtained with one) to dest over plain http, except to loopback
// hosts or with allowHTTP / AllowInsecureSecretHTTP.
func CheckCredentialTransport(dest string, allowHTTP bool) error {
	u, err := url.Parse(strings.TrimSpace(dest))
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("credentials can only be sent to an absolute https URL")
	}
	if u.Scheme == "https" || allowHTTP || AllowInsecureSecretHTTP || IsLoopbackHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("credentials are only sent over https (the operator can allow http with --secrets-allow-insecure-http)")
}

func (c *Client) serverSecretsOpen(ctx context.Context) bool {
	return c.ServerSecretsOpen != nil && c.ServerSecretsOpen(ctx)
}

// IsLoopbackHost reports whether a URL host is literally a loopback name or
// address ("localhost", "*.localhost", 127.0.0.0/8, ::1).
func IsLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Unmap().IsLoopback()
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// StaticSecrets is a SecretFunc over fixed values, each a project secret of
// every project (tests and tools).
func StaticSecrets(m map[string]string) SecretFunc {
	return func(_ context.Context, projectID, name string) (*store.SecretRecord, error) {
		v, ok := m[name]
		if !ok {
			return nil, nil
		}
		return &store.SecretRecord{SecretInfo: store.SecretInfo{Name: name, Scope: store.SecretScopeProject, ScopeID: projectID,
			Kind: store.SecretKindSecret, AllowedHosts: []string{}}, Value: v}, nil
	}
}
