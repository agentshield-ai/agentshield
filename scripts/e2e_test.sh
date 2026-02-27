#!/usr/bin/env bash
# e2e_test.sh — End-to-end test for AgentShield + OpenClaw integration.
#
# Uses `openclaw --profile e2e` for full OpenClaw-side isolation (config,
# workspace, credentials, sessions) and env-var overrides for AgentShield
# port/install-dir isolation.  Zero interference with production.
#
# Usage:
#   bash scripts/e2e_test.sh [--keep]
#
# Options:
#   --keep  Keep ~/.openclaw-e2e/ after test for debugging
#
# Ports:
#   OpenClaw gateway : 28789 (test)  vs 18789 (production)
#   AgentShield      : 28433 (test)  vs  8433 (production)
set -euo pipefail

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
PROFILE="e2e"
PROFILE_DIR="$HOME/.openclaw-e2e"
GW_PORT=28789
AS_PORT=28433
E2E_TOKEN="$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | base64 | tr -d '\n' | head -c 64)"
KEEP=0

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
INSTALLER="$ROOT_DIR/plugins/openclaw/skill/install.sh"
VALIDATOR="$SCRIPT_DIR/e2e_validate.sh"
CASES="$SCRIPT_DIR/e2e_cases.json"

# Tracked PIDs for cleanup
GW_PID=""
AS_PID=""

# Colours
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
info()  { echo -e "${CYAN}[e2e]${NC} $1"; }
ok()    { echo -e "${GREEN}[e2e]${NC} $1"; }
warn()  { echo -e "${YELLOW}[e2e]${NC} $1"; }
fail()  { echo -e "${RED}[e2e]${NC} $1" >&2; }

# ---------------------------------------------------------------------------
# Parse args
# ---------------------------------------------------------------------------
for arg in "$@"; do
    case "$arg" in
        --keep) KEEP=1 ;;
        *) fail "Unknown argument: $arg"; exit 1 ;;
    esac
done

# ---------------------------------------------------------------------------
# Cleanup (runs on EXIT, INT, TERM)
# ---------------------------------------------------------------------------
cleanup() {
    local exit_code=$?
    set +e
    info "Cleaning up..."

    # Kill AgentShield engine
    if [[ -n "$AS_PID" ]] && kill -0 "$AS_PID" 2>/dev/null; then
        kill "$AS_PID" 2>/dev/null
        wait "$AS_PID" 2>/dev/null
        info "Stopped AgentShield engine (PID $AS_PID)"
    fi
    # Also check PID file
    if [[ -f "$PROFILE_DIR/.agentshield/agentshield-e2e.pid" ]]; then
        local pid
        pid=$(cat "$PROFILE_DIR/.agentshield/agentshield-e2e.pid" 2>/dev/null)
        if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null
            wait "$pid" 2>/dev/null
        fi
    fi

    # Kill OpenClaw gateway
    if [[ -n "$GW_PID" ]] && kill -0 "$GW_PID" 2>/dev/null; then
        kill "$GW_PID" 2>/dev/null
        wait "$GW_PID" 2>/dev/null
        info "Stopped OpenClaw gateway (PID $GW_PID)"
    fi

    # Port backstop — kill anything still listening on test ports
    if command -v fuser >/dev/null 2>&1; then
        fuser -k "${GW_PORT}/tcp" 2>/dev/null || true
        fuser -k "${AS_PORT}/tcp" 2>/dev/null || true
    elif command -v lsof >/dev/null 2>&1; then
        local pids
        pids=$(lsof -ti :"$GW_PORT" 2>/dev/null || true)
        [[ -n "$pids" ]] && kill $pids 2>/dev/null || true
        pids=$(lsof -ti :"$AS_PORT" 2>/dev/null || true)
        [[ -n "$pids" ]] && kill $pids 2>/dev/null || true
    fi

    # Remove profile directory
    if [[ "$KEEP" -eq 0 ]] && [[ -d "$PROFILE_DIR" ]]; then
        rm -rf "$PROFILE_DIR"
        info "Removed $PROFILE_DIR"
    elif [[ "$KEEP" -eq 1 ]]; then
        info "Kept $PROFILE_DIR for debugging"
    fi

    exit "$exit_code"
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------------------
# Phase 0: Pre-flight
# ---------------------------------------------------------------------------
info "=== Phase 0: Pre-flight checks ==="

# Check required binaries
for dep in openclaw curl jq; do
    command -v "$dep" >/dev/null 2>&1 || { fail "Missing required binary: $dep"; exit 1; }
done
ok "Dependencies present: openclaw, curl, jq"

# Check required files
for f in "$INSTALLER" "$VALIDATOR" "$CASES"; do
    [[ -f "$f" ]] || { fail "Missing file: $f"; exit 1; }
done
ok "Test files present"

# Check test ports are free
port_in_use() {
    if command -v ss >/dev/null 2>&1; then
        ss -tlnp 2>/dev/null | grep -q ":$1 " && return 0
    elif command -v lsof >/dev/null 2>&1; then
        lsof -i :"$1" -sTCP:LISTEN >/dev/null 2>&1 && return 0
    elif command -v netstat >/dev/null 2>&1; then
        netstat -tlnp 2>/dev/null | grep -q ":$1 " && return 0
    fi
    return 1
}

if port_in_use "$GW_PORT"; then
    fail "Port $GW_PORT already in use — would collide with test gateway"
    exit 1
fi
if port_in_use "$AS_PORT"; then
    fail "Port $AS_PORT already in use — would collide with test engine"
    exit 1
fi
ok "Test ports $GW_PORT and $AS_PORT are free"

# ---------------------------------------------------------------------------
# Phase 1: Start isolated OpenClaw gateway
# ---------------------------------------------------------------------------
info "=== Phase 1: Start isolated OpenClaw gateway ==="

openclaw --profile "$PROFILE" gateway run \
    --port "$GW_PORT" \
    --allow-unconfigured \
    --token "$E2E_TOKEN" &
GW_PID=$!
info "OpenClaw gateway starting (PID $GW_PID, port $GW_PORT)..."

# Wait for gateway to accept connections (WebSocket gateway, not HTTP)
GW_HEALTHY=0
for i in $(seq 1 30); do
    # Check the process is still alive
    if ! kill -0 "$GW_PID" 2>/dev/null; then
        fail "OpenClaw gateway process died"
        exit 1
    fi
    # Check port is listening via TCP connect
    if (echo > "/dev/tcp/127.0.0.1/${GW_PORT}") 2>/dev/null; then
        GW_HEALTHY=1
        break
    fi
    sleep 1
done

if [[ "$GW_HEALTHY" -ne 1 ]]; then
    fail "OpenClaw gateway failed to start on port $GW_PORT within 30s"
    exit 1
fi
ok "OpenClaw gateway listening on port $GW_PORT"

# ---------------------------------------------------------------------------
# Phase 2: Install AgentShield into the e2e profile
# ---------------------------------------------------------------------------
info "=== Phase 2: Install AgentShield into e2e profile ==="

export AGENTSHIELD_HOME="$PROFILE_DIR/.agentshield"
export AGENTSHIELD_PORT="$AS_PORT"
export AGENTSHIELD_E2E_MODE=1

# Pre-populate the engine binary from production install (if available)
# to avoid needing Go or a GitHub download during the test.
PROD_BINARY="$HOME/.agentshield/agentshield-engine"
if [[ -x "$PROD_BINARY" ]]; then
    mkdir -p "$AGENTSHIELD_HOME"
    cp "$PROD_BINARY" "$AGENTSHIELD_HOME/agentshield-engine"
    chmod +x "$AGENTSHIELD_HOME/agentshield-engine"
    info "Copied production binary to test install dir"
fi

# Pre-populate rules from production install (the rules repo may not be
# accessible, and the fallback basic.yml is not a valid sigma rule).
PROD_RULES="$HOME/.agentshield/rules"
if [[ -d "$PROD_RULES" ]] && ls "$PROD_RULES"/*.yml >/dev/null 2>&1; then
    mkdir -p "$AGENTSHIELD_HOME/rules"
    cp "$PROD_RULES"/*.yml "$AGENTSHIELD_HOME/rules/"
    info "Copied $(ls "$AGENTSHIELD_HOME/rules/"*.yml | wc -l) production rules to test install"
fi

# Run the installer.  The installer calls `openclaw config patch` — we need
# it to patch the *e2e* profile's config, not production.  We achieve this by
# wrapping `openclaw` so that --profile e2e is always injected.
WRAPPER_DIR=$(mktemp -d)
cat > "$WRAPPER_DIR/openclaw" << 'WRAPPER_EOF'
#!/usr/bin/env bash
# Wrapper: inject --profile e2e into all openclaw calls
REAL_OPENCLAW="$(command -v openclaw 2>/dev/null)"
# Remove this wrapper dir from PATH to find the real binary
CLEAN_PATH="$(echo "$PATH" | tr ':' '\n' | grep -v "$(dirname "$0")" | tr '\n' ':')"
CLEAN_PATH="${CLEAN_PATH%:}"
if [[ -z "$REAL_OPENCLAW" ]]; then
    REAL_OPENCLAW="$(PATH="$CLEAN_PATH" command -v openclaw)"
fi
exec "$REAL_OPENCLAW" --profile e2e "$@"
WRAPPER_EOF
chmod +x "$WRAPPER_DIR/openclaw"
export PATH="$WRAPPER_DIR:$PATH"

info "Running installer (AGENTSHIELD_HOME=$AGENTSHIELD_HOME, port=$AS_PORT)..."
INSTALL_LOG="$AGENTSHIELD_HOME/install.log"
bash "$INSTALLER" >"$INSTALL_LOG" 2>&1
INSTALL_RC=$?
cat "$INSTALL_LOG"
if [[ $INSTALL_RC -ne 0 ]]; then
    fail "Installer exited with code $INSTALL_RC"
    exit 1
fi

# Clean up wrapper
rm -rf "$WRAPPER_DIR"
# Restore PATH (remove the wrapper dir)
export PATH="${PATH#*:}"

# Record AgentShield PID if the installer wrote one
if [[ -f "$AGENTSHIELD_HOME/agentshield-e2e.pid" ]]; then
    AS_PID=$(cat "$AGENTSHIELD_HOME/agentshield-e2e.pid")
    info "AgentShield engine PID: $AS_PID"
fi

# ---------------------------------------------------------------------------
# Phase 3: Health-check AgentShield engine
# ---------------------------------------------------------------------------
info "=== Phase 3: Health-check AgentShield engine ==="

# Extract auth token from the generated config
AS_TOKEN=$(grep "token:" "$AGENTSHIELD_HOME/config.yaml" | awk '{print $2}' | tr -d '"')

AS_HEALTHY=0
for i in $(seq 1 30); do
    if curl -sf -H "Authorization: Bearer $AS_TOKEN" \
        "http://127.0.0.1:${AS_PORT}/api/v1/health" >/dev/null 2>&1; then
        AS_HEALTHY=1
        break
    fi
    sleep 1
done

if [[ "$AS_HEALTHY" -ne 1 ]]; then
    fail "AgentShield engine failed health check on port $AS_PORT within 30s"
    # Dump logs for debugging
    if [[ -f "$AGENTSHIELD_HOME/engine.log" ]]; then
        fail "Engine log tail:"
        tail -20 "$AGENTSHIELD_HOME/engine.log" >&2 || true
    fi
    exit 1
fi
ok "AgentShield engine healthy on port $AS_PORT"

# ---------------------------------------------------------------------------
# Phase 4: Run test cases
# ---------------------------------------------------------------------------
info "=== Phase 4: Run test cases ==="

export AGENTSHIELD_TEST_PORT="$AS_PORT"
export AGENTSHIELD_TEST_TOKEN="$AS_TOKEN"

RESULT=0
bash "$VALIDATOR" "$CASES" || RESULT=$?

# ---------------------------------------------------------------------------
# Phase 5: Report results
# ---------------------------------------------------------------------------
info "=== Phase 5: Results ==="

if [[ "$RESULT" -eq 0 ]]; then
    ok "All test cases passed"
else
    fail "$RESULT test case(s) had failures"
fi

# Verify production gateway is still healthy (if running)
if (echo > /dev/tcp/127.0.0.1/18789) 2>/dev/null; then
    ok "Production gateway (port 18789) still healthy"
else
    warn "Production gateway (port 18789) not responding (may not be running)"
fi

exit "$RESULT"
