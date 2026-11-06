package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/server"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// iamFlags configure SSO, browser sessions and the audit log.
type iamFlags struct {
	issuer, clientID, clientSecret, redirectURL, domains, groupsClaim, groupRoles, adminEmails, scopes *string
	sessionTTL, secureCookies, auditRetention                                                          *string
	assumeVerified                                                                                     *bool
}

func registerIAMFlags() *iamFlags {
	return &iamFlags{
		issuer:   flag.String("oidc-issuer", os.Getenv("WEFT_OIDC_ISSUER"), "OIDC issuer URL for single sign-on, e.g. https://accounts.google.com (env WEFT_OIDC_ISSUER); empty disables SSO"),
		clientID: flag.String("oidc-client-id", os.Getenv("WEFT_OIDC_CLIENT_ID"), "OIDC client id (env WEFT_OIDC_CLIENT_ID)"),
		clientSecret: flag.String("oidc-client-secret", os.Getenv("WEFT_OIDC_CLIENT_SECRET"),
			"OIDC client secret (env WEFT_OIDC_CLIENT_SECRET; prefer the env var, flags show in process lists); empty for a public PKCE client"),
		redirectURL: flag.String("oidc-redirect-url", os.Getenv("WEFT_OIDC_REDIRECT_URL"),
			"OIDC redirect URL registered with the provider: <public URL>/auth/oidc/callback (env WEFT_OIDC_REDIRECT_URL)"),
		domains: flag.String("oidc-allowed-domains", os.Getenv("WEFT_OIDC_ALLOWED_DOMAINS"),
			"comma-separated email domains allowed to sign in (env WEFT_OIDC_ALLOWED_DOMAINS)"),
		groupsClaim: flag.String("oidc-groups-claim", envOr("WEFT_OIDC_GROUPS_CLAIM", "groups"), "ID-token claim holding the user's groups (env WEFT_OIDC_GROUPS_CLAIM)"),
		groupRoles: flag.String("oidc-group-roles", os.Getenv("WEFT_OIDC_GROUP_ROLES"),
			"comma-separated group mappings: group=admin (server admin) or group=viewer|member|admin@team (env WEFT_OIDC_GROUP_ROLES)"),
		adminEmails: flag.String("oidc-admin-emails", os.Getenv("WEFT_OIDC_ADMIN_EMAILS"),
			"comma-separated verified emails made server admins when they sign in (env WEFT_OIDC_ADMIN_EMAILS)"),
		assumeVerified: flag.Bool("oidc-assume-email-verified", os.Getenv("WEFT_OIDC_ASSUME_EMAIL_VERIFIED") == "true",
			"treat emails without an email_verified claim as verified, for identity providers that only issue verified addresses but omit the claim "+
				"(env WEFT_OIDC_ASSUME_EMAIL_VERIFIED=true); off: such emails do not link existing accounts or pass --oidc-allowed-domains"),
		scopes:         flag.String("oidc-scopes", envOr("WEFT_OIDC_SCOPES", "openid,email,profile"), "OIDC scopes to request (env WEFT_OIDC_SCOPES)"),
		sessionTTL:     flag.String("session-ttl", envOr("WEFT_SESSION_TTL", "12h"), "lifetime of a browser session (env WEFT_SESSION_TTL)"),
		secureCookies:  flag.String("secure-cookies", os.Getenv("WEFT_SECURE_COOKIES"), "true: always mark cookies Secure, e.g. behind a TLS proxy (env WEFT_SECURE_COOKIES)"),
		auditRetention: flag.String("audit-retention-days", envOr("WEFT_AUDIT_RETENTION_DAYS", "90"), "days to keep audit log entries; -1 keeps them forever (env WEFT_AUDIT_RETENTION_DAYS)"),
	}
}

// apply fills cfg.IAM. The session-signing key is derived from the secret
// encryption key (WEFT_SECRET_KEY or <db>.key), stretched with Argon2id
// (store.SessionSigningKey), unless WEFT_SESSION_KEY is set.
func (f *iamFlags) apply(cfg *server.Config, st *store.Store, log *slog.Logger) error {
	ttl, err := time.ParseDuration(*f.sessionTTL)
	if err != nil || ttl < time.Minute || ttl > 30*24*time.Hour {
		return fmt.Errorf("invalid --session-ttl %q (1m to 720h)", *f.sessionTTL)
	}
	days, err := strconv.Atoi(*f.auditRetention)
	if err != nil {
		return fmt.Errorf("invalid --audit-retention-days %q", *f.auditRetention)
	}
	if days == 0 {
		days = -1
	}
	cfg.IAM.SessionTTL, cfg.IAM.AuditRetentionDays = ttl, days
	if *f.secureCookies != "" {
		if cfg.IAM.SecureCookies, err = strconv.ParseBool(*f.secureCookies); err != nil {
			return fmt.Errorf("invalid --secure-cookies %q", *f.secureCookies)
		}
	}
	if k := os.Getenv("WEFT_SESSION_KEY"); k != "" {
		if len(k) < 32 {
			return fmt.Errorf("WEFT_SESSION_KEY must be at least 32 characters")
		}
		cfg.IAM.SessionKey = []byte(k)
	} else if k, err := st.SessionSigningKey(context.Background()); err == nil {
		// Since v2 the key is stretched: sessions signed with the earlier
		// (unstretched) key end once on upgrade, and users sign in again.
		cfg.IAM.SessionKey = k
	} else {
		log.Warn("no session key; browser sessions end when the server restarts", "err", err)
	}

	if *f.issuer == "" {
		if *f.clientID != "" || *f.redirectURL != "" {
			return fmt.Errorf("--oidc-client-id and --oidc-redirect-url need --oidc-issuer")
		}
		return nil
	}
	if *f.clientID == "" || *f.redirectURL == "" {
		return fmt.Errorf("--oidc-issuer needs --oidc-client-id and --oidc-redirect-url")
	}
	if !strings.HasSuffix(*f.redirectURL, "/auth/oidc/callback") {
		return fmt.Errorf("--oidc-redirect-url must end in /auth/oidc/callback")
	}
	roles, err := server.ParseGroupRoles(*f.groupRoles)
	if err != nil {
		return err
	}
	o := &server.OIDCConfig{Issuer: *f.issuer, ClientID: *f.clientID, ClientSecret: *f.clientSecret, RedirectURL: *f.redirectURL,
		AllowedDomains: splitList(*f.domains), GroupsClaim: *f.groupsClaim, GroupRoles: roles, AdminEmails: splitList(*f.adminEmails),
		Scopes: splitList(strings.ReplaceAll(*f.scopes, " ", ",")), AssumeEmailVerified: *f.assumeVerified}
	if len(o.AllowedDomains) == 0 && len(o.GroupRoles) == 0 {
		log.Warn("SSO: any account of the identity provider can sign in (it has no access until granted a role); set --oidc-allowed-domains or --oidc-group-roles to restrict sign-in")
	}
	if !strings.HasPrefix(o.RedirectURL, "https://") {
		log.Warn("SSO redirect URL is not https; session cookies are sent without the Secure attribute")
	}
	cfg.IAM.OIDC = o
	return nil
}
