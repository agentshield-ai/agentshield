# v1.0.0 OSS Release Checklist

## Completed in `release/v1.0.0-oss`

- [x] Added OSS governance docs (`SECURITY.md`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SUPPORT.md`)
- [x] Added GitHub hygiene (`.github` issue template + PR template)
- [x] Added CI workflow (gofmt check, go vet, go test)
- [x] Added security workflow (`govulncheck`)
- [x] Added offline forensic frontend (`forensics-ui/`) with direct SQLite loading
- [x] Expanded security/runtime test coverage and fixed high-FP stats path
- [x] Local verification: `go vet ./...` and `go test ./...` pass

## Blockers / Decisions before tag

- [ ] **Formatting debt**: repository currently has existing files that fail strict `gofmt -l .` gate.
  - Decision needed: (A) run repo-wide gofmt in a dedicated formatting commit, or (B) scope CI format check to changed paths temporarily.
- [ ] Confirm if `forensics-ui/vendor/sql-wasm.*` should be tracked in git for offline guarantees (currently included).
- [ ] Review and finalize release notes + known limitations.

## Recommended immediate next actions

1. Resolve gofmt policy decision and update CI accordingly.
2. Push `release/v1.0.0-oss` branch and open PR to `main`.
3. Run CI green on PR.
4. Create tag `v1.0.0-rc1`.
5. Smoke test install + demo flows.
6. Promote to `v1.0.0`.
