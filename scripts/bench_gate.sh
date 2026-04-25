#!/usr/bin/env bash
# bench_gate.sh — fail CI on benchmark regressions vs base branch.
#
# Reads benchstat output produced by `benchstat -alpha 0.05 base.txt pr.txt`
# (modern golang.org/x/perf benchstat) and exits non-zero if any row in the
# sec/op or allocs/op tables shows a delta worse than REGRESSION_THRESHOLD_PCT
# with p-value below P_VALUE_MAX. B/op is advisory only.
#
# Usage: scripts/bench_gate.sh diff.txt
#
# Env:
#   REGRESSION_THRESHOLD_PCT  default 10
#   P_VALUE_MAX               default 0.05
#
# benchstat tables look like:
#         │ base.txt   │              pr.txt              │
#         │   sec/op   │   sec/op     vs base             │
#   Foo-4    1.001µ ± 1%  1.502µ ± 1%  +50.00% (p=0.002 n=6)
#   Bar-4    501.5n ± 2%  501.5n ± 2%        ~ (p=1.000 n=6)
# A "~" delta or no p-value means no significant comparison: ignored.
# Section is identified by the second header line containing "sec/op" or
# "allocs/op" or "B/op".

set -uo pipefail

DIFF_FILE="${1:-diff.txt}"
THRESHOLD="${REGRESSION_THRESHOLD_PCT:-10}"
PVAL_MAX="${P_VALUE_MAX:-0.05}"

if [[ ! -s "$DIFF_FILE" ]]; then
    echo "bench_gate: diff file '$DIFF_FILE' missing or empty" >&2
    exit 1
fi

awk -v threshold="$THRESHOLD" -v pval_max="$PVAL_MAX" '
    BEGIN { fail = 0; section = ""; rows = 0 }

    # Skip empty/whitespace-only lines.
    /^[[:space:]]*$/ { next }

    # Section header detection. benchstat prints two header lines per table;
    # the second one names the metric. Box-drawing char │ separates columns.
    /sec\/op/   { section = "sec";    next }
    /allocs\/op/ { section = "allocs"; next }
    /B\/op/      { section = "bytes";  next }

    # Footnotes start with a digit-superscript char like ¹ — skip.
    /^[[:space:]]*¹/ { next }

    # geomean rows are summary aggregates; ignore.
    /^[[:space:]]*geomean/ { next }

    # We only enforce sec/op and allocs/op.
    section != "sec" && section != "allocs" { next }

    # A data row begins with a benchmark name token (no leading whitespace
    # or a small amount); contains a "vs base" column with a delta.
    {
        # Find delta-pct and p-value tokens.
        delta_pct = ""
        pval = ""
        for (i = 1; i <= NF; i++) {
            if ($i ~ /^[-+][0-9]+\.[0-9]+%$/) {
                delta_pct = $i
            } else if ($i == "~") {
                delta_pct = "~"
            }
            if ($i ~ /^\(p=/) {
                pv = $i
                gsub(/[\(\)p=]/, "", pv)
                pval = pv
            }
        }

        # No delta column means this is not a data row (header noise).
        if (delta_pct == "") next

        rows++

        # Insignificant comparison.
        if (delta_pct == "~") next

        sign = substr(delta_pct, 1, 1)
        num  = substr(delta_pct, 2, length(delta_pct) - 2) + 0
        if (sign == "-") num = -num

        # Negative or small positive deltas are not regressions.
        if (num < threshold) next

        pv = (pval == "" ? 1 : pval + 0)
        if (pv > pval_max) next

        printf("REGRESSION %s [%s]: %s (p=%s)\n", $1, section, delta_pct, pval) > "/dev/stderr"
        fail = 1
    }

    END {
        if (rows == 0) {
            print "bench_gate: no benchmark rows parsed; check benchstat output" > "/dev/stderr"
            exit 2
        }
        exit fail
    }
' "$DIFF_FILE"
status=$?

if (( status == 0 )); then
    echo "bench_gate: OK (no regressions above ${THRESHOLD}% with p<${PVAL_MAX})"
fi

exit $status
