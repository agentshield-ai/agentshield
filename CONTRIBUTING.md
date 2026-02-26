# Contributing to AgentShield

Thank you for your interest in contributing to AgentShield!

## Prerequisites

| Tool       | Version | Purpose                  |
|------------|---------|--------------------------|
| Go         | 1.24+   | Engine development       |
| Node.js    | 18+     | Plugin development       |
| Make       | any     | Build automation         |

## Getting Started

1. **Fork** the repository and clone your fork:
   ```bash
   git clone https://github.com/<you>/agentshield.git && cd agentshield
   ```
2. **Install dependencies, build, and test:**
   ```bash
   make deps && make build && make test
   ```

## Development Workflow

### Branch Naming

Create a branch from `main` using the pattern `<type>/<short-description>`:

- `feat/new-rule` -- new feature
- `fix/auth-bypass` -- bug fix
- `docs/api-guide` -- documentation
- `refactor/sigma-parser` -- restructuring
- `test/triage-edge-cases` -- tests
- `chore/update-deps` -- maintenance

### Commit Messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add credential-exfiltration detection rule
fix: correct regex anchoring in sigma evaluator
docs: update API endpoint reference
refactor: extract triage logic into standalone package
test: add edge-case tests for deep-triage scoring
chore: bump Go version to 1.24
```

### Pull Request Process

1. Create a feature branch from `main`.
2. Make your changes in small, focused commits.
3. Ensure all tests pass locally (`make test`).
4. Push your branch and open a PR against `main`.
5. Fill in the PR template -- describe **what** changed and **why**.
6. Address any review feedback.

## Testing

### Engine (Go)

```bash
go test ./...           # all packages
go test ./pkg/sigma/... # specific package
```

### Plugins (Node.js)

```bash
cd plugins/<name>
npm install
npm test
```

## Contributing Detection Rules

Detection rules use a Sigma-based YAML format. To add a new rule:

1. Place the `.yml` file in the appropriate `rules/<category>/` directory
   (e.g., `execution`, `exfiltration`, `prompt_injection`).
2. Follow the naming convention `agent_<description>.yml`.
3. Include required fields: `id`, `title`, `description`, `author`, `date`,
   `status`, `level`, `logsource`, `tags`, and `detection`.
4. Tag rules with MITRE ATT&CK references (e.g., `attack.execution`,
   `attack.t1059`).
5. Add tests that exercise both matching and non-matching events.

See `rules/execution/agent_rce_injection.yml` for a reference example.

## Code of Conduct

This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md).
Please read it before participating.

## Questions?

Open a [discussion](https://github.com/agentshield-ai/agentshield/discussions) or
reach out at **security@agentshield.ai** for security-related queries.
