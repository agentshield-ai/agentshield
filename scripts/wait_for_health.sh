#!/usr/bin/env bash
# Poll an HTTP health endpoint until it succeeds or a timeout elapses.
#
# Usage: wait_for_health.sh [URL] [TIMEOUT_SECONDS]
#   URL              defaults to http://localhost:8433/health
#   TIMEOUT_SECONDS  defaults to 30

set -euo pipefail

url="${1:-http://localhost:8433/health}"
timeout="${2:-30}"

for i in $(seq 1 "$timeout"); do
  if curl -sf "$url" >/dev/null; then
    echo "ready after ${i}s: $url"
    exit 0
  fi
  sleep 1
done

echo "failed to become ready within ${timeout}s: $url" >&2
exit 1
