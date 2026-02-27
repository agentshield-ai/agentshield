#!/usr/bin/env bash
# e2e_validate.sh — TAP-format test runner for AgentShield evaluate endpoint.
# Reads e2e_cases.json and POSTs each case to the engine, comparing the
# returned action against the expected outcome.
#
# Env vars:
#   AGENTSHIELD_TEST_PORT  — engine port (default: 28433)
#   AGENTSHIELD_TEST_TOKEN — auth token (required)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CASES_FILE="${1:-$SCRIPT_DIR/e2e_cases.json}"
PORT="${AGENTSHIELD_TEST_PORT:-28433}"
TOKEN="${AGENTSHIELD_TEST_TOKEN:?AGENTSHIELD_TEST_TOKEN must be set}"
BASE_URL="http://127.0.0.1:${PORT}"

for dep in jq curl; do
    command -v "$dep" >/dev/null 2>&1 || { echo "Bail out! Missing dependency: $dep" >&2; exit 1; }
done

[[ -f "$CASES_FILE" ]] || { echo "Bail out! Cases file not found: $CASES_FILE" >&2; exit 1; }

NUM_CASES=$(jq length "$CASES_FILE")
echo "TAP version 13"
echo "1..$NUM_CASES"

PASS=0; FAIL=0; IDX=0

while IFS= read -r case_json; do
    IDX=$((IDX + 1))
    CASE_ID=$(echo "$case_json" | jq -r '.id')
    CMD=$(echo "$case_json" | jq -r '.command')
    EXPECTED=$(echo "$case_json" | jq -r '.expected')

    PAYLOAD=$(jq -n \
        --arg eid "e2e-${CASE_ID}" \
        --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        --arg cmd "$CMD" \
        '{
            event_id: $eid,
            timestamp: $ts,
            event_type: "tool_call",
            tool_name: "exec",
            source: "openclaw",
            command: $cmd,
            params: { command: $cmd },
            agent_id: "e2e-agent",
            session_id: "e2e-session",
            working_dir: "/tmp",
            data: {}
        }')

    HTTP_CODE=""
    RESP=""
    RESP=$(curl -s -w "\n%{http_code}" -X POST "${BASE_URL}/api/v1/evaluate" \
        -H "Authorization: Bearer ${TOKEN}" \
        -H "Content-Type: application/json" \
        -d "$PAYLOAD" 2>/dev/null) || true

    HTTP_CODE=$(echo "$RESP" | tail -1)
    BODY=$(echo "$RESP" | sed '$d')

    if [[ "$HTTP_CODE" != "200" ]]; then
        FAIL=$((FAIL + 1))
        echo "not ok $IDX - $CASE_ID # HTTP $HTTP_CODE"
        echo "  ---"
        echo "  response: $BODY"
        echo "  ..."
        continue
    fi

    ACTION=$(echo "$BODY" | jq -r '.action // empty' 2>/dev/null || true)

    # Normalise: anything that isn't "block" counts as "allow"
    if [[ "$ACTION" == "block" ]]; then
        ACTUAL="block"
    else
        ACTUAL="allow"
    fi

    if [[ "$ACTUAL" == "$EXPECTED" ]]; then
        PASS=$((PASS + 1))
        echo "ok $IDX - $CASE_ID (expected=$EXPECTED got=$ACTUAL)"
    else
        FAIL=$((FAIL + 1))
        echo "not ok $IDX - $CASE_ID (expected=$EXPECTED got=$ACTUAL)"
        echo "  ---"
        echo "  action: $ACTION"
        echo "  body: $BODY"
        echo "  ..."
    fi
done < <(jq -c '.[]' "$CASES_FILE")

echo "# pass $PASS / $NUM_CASES"
if [[ $FAIL -gt 0 ]]; then
    echo "# fail $FAIL / $NUM_CASES"
    exit 1
fi
exit 0
