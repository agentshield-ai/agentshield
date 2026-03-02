# Rule Source Strategy

This document explains how AgentShield consumes detection rules from the upstream, engine-agnostic rule catalogue maintained at [`agentshield-ai/sigma-ai`](https://github.com/agentshield-ai/sigma-ai) via a git subtree.

## Layout

The `rules/` directory is a **git subtree** tracking `sigma-ai/main`. The YAML rule files reside at `rules/rules/ai_agent/*.yml`.

## Syncing Upstream Rules

```bash
# One-time remote setup (already done if you cloned with the subtree)
git remote add sigma-ai https://github.com/agentshield-ai/sigma-ai.git

# Pull latest from main
scripts/sync_sigma_ai.sh main

# Or pull from a feature branch
scripts/sync_sigma_ai.sh feat/new-rules
```

Under the hood, the sync script runs `git subtree pull --prefix=rules --squash`, creating a single squash-merge commit containing the upstream changes.

## CI Sync Check

A daily GitHub Actions workflow (`.github/workflows/sigma-ai-sync-check.yml`) clones the upstream repository and verifies that every upstream rule file exists locally. If the subtree is stale, the check fails.

## Rationale

- **`sigma-ai`** is the canonical sharing surface for AI-agent Sigma rules.
- **AgentShield** is an enforcement and evaluation engine that consumes those shared rules.
- Non-AgentShield users can adopt the rules independently, avoiding engine lock-in.
- The subtree approach keeps full rule content in the repository (no submodule checkout step required) whilst maintaining a clean pull path from upstream.
