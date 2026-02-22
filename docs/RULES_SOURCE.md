# Rule Source Strategy

AgentShield now consumes an upstream, engine-agnostic rule catalog from:
- `agentshield-ai/sigma-ai`

## Layout

- Upstream snapshot lives at: `rules/upstream/sigma-ai/`
- AgentShield-specific local rules may continue to live under `rules/`

## Syncing upstream rules

```bash
# one-time remote setup
git remote add sigma-ai https://github.com/agentshield-ai/sigma-ai.git

# sync latest main
scripts/sync_sigma_ai.sh main

# or sync a feature branch
scripts/sync_sigma_ai.sh feat/skills-abuse-rules
```

## Why this split

- `sigma-ai` is the canonical sharing surface for AI-agent Sigma rules.
- AgentShield remains an enforcement/evaluation engine that consumes shared rules.
- This enables non-AgentShield users to adopt rules without engine lock-in.
