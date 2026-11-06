# Weft security

What Weft protects, against whom, how to deploy it safely, and what it does
not try to prevent. The mechanisms are described in detail in
[README.md](README.md#security-notes) and [API.md](API.md#conventions); this
page is the overview an operator needs.

## Threat model

Weft stores what agents did (prompts, responses, tool calls, reasoning,
AgentShield verdicts) and runs evaluations that call model providers with
stored credentials. It is meant to be shared by a team on one server. It
treats as potentially hostile:

- **anyone who can reach the port** without a credential;
- **a holder of a credential** with less access than another: a viewer, a
  member of one project, a project-scoped or read-only API key;
- **a telemetry sender** (any agent, harness or collector posting to
  `/otel/*` or the span API with a write credential);
- **recorded content**: prompts, tool output, file names, attribute values
  and dataset rows written by a model or a web page;
- **a remote endpoint** Weft calls (a model provider, a remote scorer, a
  webhook, an OAuth token URL), which may answer with anything.

It does not defend against the operator, a server admin, or someone with
the server's files (the database and its key files): they can read
everything.

### Who can do what

| Principal | Can | Cannot |
|---|---|---|
| Unauthenticated client (auth on) | `GET /health`, `GET /api/v1/server`, the SSO sign-in flow | read or write anything else; its request body is not read before it authenticates |
| Unauthenticated client (auth off) | everything, as a server admin | act through a visitor's browser from another site (Host, Origin and content-type checks) |
| Viewer of a project | read its spans, objects, tables, feedback, detections, sessions; run read queries (bounded, see below) | write; read other projects; list secret values |
| Member of a project | also write spans and feedback, push AgentShield verdicts, save objects (providers, scorers, automations), run evaluations, set **project** secrets, delete spans, claim a plugin source for a session | manage members, keys or project settings; use another project's secrets; send a team or server secret anywhere but its allowed hosts |
| Admin of a project | also manage its members and keys, retention, sampling and redaction settings, delete it, restore a session's source, read its audit entries | touch other projects |
| Team admin | admin of every project of the team; manage team secrets | other teams; server secrets |
| Server admin (static `--api-keys`, admin keys, admin users) | everything: users, teams, server secrets, reference buckets, bulk export, the whole audit log | read a secret's value through the API |
| API key | what its user may, optionally narrowed to projects or read-only; never broader than the credential that created it | outlive its user being disabled |

A request is checked against every project it names (query, body, batch
items, path); a body that names a project the caller may not use is refused
(`403`), and a body that is not exactly one JSON value is refused (`400`).

## Deployment checklist

1. **Turn authentication on.** Without `--api-keys`, managed keys or
   `--oidc-issuer` anyone who reaches the port is a server admin. Weft
   listens on `127.0.0.1` by default and warns loudly when it listens
   elsewhere without authentication. With SSO, set `--oidc-allowed-domains`
   and/or `--oidc-group-roles`; leave `--oidc-assume-email-verified` off
   unless your provider only issues verified addresses.
2. **Name the hosts.** Set `--allowed-hosts` to the names users reach Weft
   by (DNS rebinding protection), and `--cors-origins` only if another web
   origin must call the API.
3. **Put TLS in front.** Weft serves plain HTTP; terminate TLS at a reverse
   proxy, set `--trusted-proxies` to the proxy's address (so the audit log
   and per-client limits see the real client) and `--secure-cookies` when
   the proxy terminates TLS.
4. **Limit connections and memory.** The defaults (`--max-connections`
   4096, `--max-connections-per-ip` 128, `--response-budget-mb` 512) suit a
   team server; lower them on a small machine. Request bodies are capped
   (64 MiB written, 4 MiB for reads) and decoded within a 512 MiB body
   budget; headers must arrive in 10 s, a body at least 32 KiB every 30 s
   and within 2 minutes. Weft has no request-rate limit: put one at the
   proxy if the server is exposed.
5. **Scope secrets.** Keep provider keys as project secrets where possible.
   Give team and server secrets **allowed hosts**: without them, projects'
   own providers, scorers, predict models, OAuth configurations and
   automations cannot use them at all (only the built-in providers can),
   and the Settings page says so. Leave `--secrets-allow-insecure-http` off.
6. **Restrict outbound requests.** Keep the egress guard's default (no
   internal destinations); allow local model servers narrowly with
   `--egress-allow host:port` rather than `--egress-allow-private`.
7. **Set retention.** Per project (**Settings → Data → Retention**,
   `/project/ttl_settings/update`), and `--audit-retention-days` for the
   audit log (default 90).
8. **Protect and back up the files.** Back up the database together with
   `<db>.key` (or wherever `WEFT_SECRET_KEY` comes from): without it the
   stored secrets cannot be decrypted. Keep the database encryption
   passphrase (`--db-encryption-key-file`) in a secret manager, separate
   from the backups: losing it loses the data. Turn on database encryption
   at rest, or use an encrypted volume; keep the files mode 0600 (Weft warns
   at startup when they are not).
9. **Decide what the plugins capture.** The harness plugins send prompts,
   responses and tool input and output by default; set
   `WEFT_CAPTURE_CONTENT=false` (or `capture_content: false`) to send only
   structure, names, timing and usage, and turn on the project's
   sensitive-data redaction (optionally with `--pii-analyzer-url`) if
   content is sent.
10. **Keep the compatibility adapter off** (`--weave-compat`) unless a
    client needs it: it serves many more routes.

## What is logged, and what never is

- **The server log** (stderr or `--log-file`) carries ids, names, counts,
  sizes, durations, status codes and refusal reasons: one line per write,
  ingest batch and refusal. It never carries API keys, `Authorization` or
  cookie headers, provider keys or other secret values, prompt text, tool
  arguments or outputs, request or response bodies, or attribute values
  from telemetry. See [Logs](guides/setup.md#logs).
- **The audit log** (database table, **Settings → Access → Audit log**) records every
  write, every refused request, admin actions, sign-ins and session source
  switches and restores: actor, auth method, key id (never the key),
  method, path (cut at 1 KiB), project, object and span ids, status,
  outcome and client address. It never records bodies, headers or secret
  values. Unauthenticated refusals are recorded at most 20 per client
  address and minute, the rest as one summary entry.
- **Secrets** are stored encrypted (AES-256-GCM, key from `WEFT_SECRET_KEY`
  or `<db>.key`) and are never returned by the API; errors about a secret
  name it but never show its value, and are decided from its scope before
  its value is read, so they do not reveal what it holds.
- **Recorded data** is stored as sent, apart from the project's redaction
  settings; it is shown to everyone with viewer access to the project.

## Trust boundaries that remain by design

- **Any project writer can push AgentShield verdicts** (`/api/v1/shield/ingest`,
  OTLP `evaluate` spans) into the project; they are not signed by the
  engine. Treat detections as reports from the project's writers.
- **Any project writer can claim a plugin source** (`X-Weft-Source`), and
  one span claiming it hides the session's native telemetry from view. The
  hidden spans are kept: deleting the claiming spans shows them again, every
  switch is audited, and a project admin can restore the session and stop
  honouring plugin claims for it ([API.md](API.md#session-sources)).
- **Team secrets are usable by every member of the team's projects**
  toward the secret's allowed hosts, and by the built-in providers; server
  secrets likewise across the server. A member can spend those keys on the
  allowed hosts; they cannot read them or send them elsewhere.
- **Viewers can run expensive but bounded queries.** Filters, sorts and
  lists can make the database work hard; responses are capped (64 MiB a
  page) and built within the server-wide response budget, so a burst of
  heavy queries is answered slowly or with `503`, not by running out of
  memory.
- **A member can use the project's own secrets** with objects of their
  choosing (a provider pointed at their own host), toward the secret's
  allowed hosts if it has any: set allowed hosts on project secrets that
  only one service should receive.
- **Recorded content is untrusted.** The UI renders it as text and serves
  uploaded files sandboxed, and outbound requests built from it go through
  the egress guard; an LLM judge or scorer that reads it can still be
  influenced by what it says.

## Reporting a vulnerability

Please report suspected vulnerabilities privately: open a GitHub security
advisory on this repository ("Report a vulnerability" under the Security
tab), not a public issue. Include the version (`GET /api/v1/server`), the
configuration (flags, with secrets removed) and steps to reproduce. We aim to
acknowledge reports within a few working days and to publish a fix and an
advisory once it is available.
