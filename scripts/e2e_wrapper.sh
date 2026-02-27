#!/usr/bin/env bash
# e2e_wrapper.sh — Process-group isolation launcher for e2e_test.sh.
#
# On Linux with systemd: wraps in a cgroup scope so all children are
# killed on exit (defence against orphan processes).
# On macOS / other: uses setsid for process-group isolation.
#
# All arguments are forwarded to e2e_test.sh.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
E2E_SCRIPT="$SCRIPT_DIR/e2e_test.sh"

if [[ ! -f "$E2E_SCRIPT" ]]; then
    echo "[e2e-wrapper] Error: $E2E_SCRIPT not found" >&2
    exit 1
fi

if [[ "$(uname)" == "Linux" ]] && command -v systemd-run >/dev/null 2>&1 \
   && systemctl --user status >/dev/null 2>&1; then
    UNIT_NAME="agentshield-e2e-$$"
    # Clean up any stale scope from a previous aborted run
    systemctl --user reset-failed agentshield-e2e.scope 2>/dev/null || true
    echo "[e2e-wrapper] Using systemd-run --user --scope for process isolation (unit=$UNIT_NAME)"
    exec systemd-run --user --scope --unit="$UNIT_NAME" \
        bash "$E2E_SCRIPT" "$@"
elif command -v setsid >/dev/null 2>&1; then
    echo "[e2e-wrapper] Using setsid for process-group isolation"
    exec setsid bash "$E2E_SCRIPT" "$@"
else
    echo "[e2e-wrapper] No isolation method available, running directly"
    exec bash "$E2E_SCRIPT" "$@"
fi
