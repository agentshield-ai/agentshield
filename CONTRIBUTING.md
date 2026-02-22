# Contributing

Thanks for contributing to AgentShield.

## Dev setup

```bash
go test ./...
```

## Pull request expectations

- Keep changes scoped and reviewable
- Add/adjust tests for behavior changes
- Preserve security invariants (input validation, SSRF protections, auth requirements)
- Update docs for user-visible changes

## Commit guidance

- Use clear, imperative commit messages
- Prefer small logical commits over large mixed diffs

## Before submitting

```bash
/usr/local/go/bin/gofmt -w ./...
/usr/local/go/bin/go test ./...
```

## Security-sensitive changes

For changes involving auth, remote fetches, execution pathways, or config validation:
- include explicit regression tests
- include threat/risk notes in PR description
