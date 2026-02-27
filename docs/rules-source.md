# Rule Source Strategy

AgentShield consumes an upstream, engine-agnostic rule catalogue from `agentshield-ai/sigma-ai` via git subtree.

## Layout

The `rules/` directory is a **git subtree** tracking `sigma-ai/main`. The actual YAML rule files live at `rules/rules/ai_agent/*.yml`.

## Syncing upstream rules

```bash
# one-time remote setup (already done if you've cloned with subtree)
git remote add sigma-ai https://github.com/agentshield-ai/sigma-ai.git

# pull latest from main
scripts/sync_sigma_ai.sh main

# or pull from a feature branch
scripts/sync_sigma_ai.sh feat/new-rules
```

This runs `git subtree pull --prefix=rules --squash`, creating a single squash-merge commit with the upstream changes.

## CI sync check

A daily GitHub Actions workflow (`sigma-ai-sync-check.yml`) clones the upstream repo and verifies every upstream rule file exists locally. If the subtree is stale, the check fails.

## Why this structure

- `sigma-ai` is the canonical sharing surface for AI-agent Sigma rules.
- AgentShield is an enforcement/evaluation engine that consumes shared rules.
- Non-AgentShield users can adopt the rules without engine lock-in.
- The subtree approach keeps full rule content in-repo (no submodule checkout needed) while maintaining a clean pull path from upstream.
