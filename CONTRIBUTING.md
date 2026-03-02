# Contributing to AgentShield

Thank you for your interest in contributing to AgentShield. This guide covers everything needed to get started, from setting up a development environment to submitting a pull request.

## Prerequisites

| Tool       | Version | Purpose                  |
|------------|---------|--------------------------|
| Go         | 1.24+   | Engine development       |
| Node.js    | 18+     | Plugin development       |
| Make       | any     | Build automation         |
| Git        | 2.x+    | Version control          |

## Getting Started

1. **Fork** the repository on GitHub, then clone your fork:
   ```bash
   git clone https://github.com/<your-username>/agentshield.git
   cd agentshield
   ```
2. **Install dependencies, build, and run the test suite:**
   ```bash
   make deps
   make build
   make test
   ```
   All three commands should complete without errors before you begin development.

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

This project uses [Conventional Commits](https://www.conventionalcommits.org/). Each commit message should begin with a type prefix:

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
3. Ensure all tests pass locally (`make test` for Go; `npm test` in the relevant plugin directory).
4. Push your branch and open a pull request against `main`.
5. Fill in the PR template -- describe **what** changed and **why**.
6. Address any review feedback promptly.

## Testing

### Engine (Go)

```bash
go test ./...                      # all packages
go test -v ./internal/engine/...   # specific package
go test -v -run TestName ./...     # single test function
```

### Plugins (Node.js)

```bash
cd plugins/<name>
npm install
npm test
```

## Contributing Detection Rules

Detection rules use a Sigma-based YAML format. All rules reside in a flat directory at `rules/rules/ai_agent/`. To add a new rule:

1. Place the `.yml` file in `rules/rules/ai_agent/`.
2. Follow the naming convention `ai_agent_<description>.yml`.
3. Include the required fields: `id`, `title`, `description`, `author`, `date`,
   `status`, `level`, `logsource`, `tags`, and `detection`.
4. Set `logsource.product` to `ai_agent` and `logsource.category` to `agent_events`.
5. Tag rules with MITRE ATT&CK references (e.g., `attack.execution`,
   `attack.t1059`).
6. Add tests that exercise both matching and non-matching events.

See the [rules README](rules/README.md) for a fully annotated example and details on rule maturity levels and custom extension fields.

## Code of Conduct

This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md).
Please read it before participating.

## Questions?

Open a [discussion](https://github.com/agentshield-ai/agentshield/discussions) or
reach out at **security@agentshield.ai** for security-related queries.
