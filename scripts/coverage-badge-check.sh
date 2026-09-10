#!/usr/bin/env bash
#
# coverage-badge-check.sh — enforce that .github/badges/coverage.json
# matches the current coverage.out total within a small tolerance. Keeps
# the README badge honest: a stale badge is a subtle but load-bearing
# credibility issue — enterprise buyers reading the README should see
# a number that matches the CI reality, not a snapshot from months ago.
#
# Usage: scripts/coverage-badge-check.sh [coverprofile] [tolerance-pp]
# Defaults: coverage.out, 0.5
#
# Exits 0 when the badge value is within tolerance-pp of the current
# total, otherwise 1 with a diagnostic that explains exactly what to
# change in the JSON. Deliberately does NOT rewrite the JSON — that
# stays a human decision so drift can't sneak in via CI auto-commit.
#
set -euo pipefail

PROFILE="${1:-coverage.out}"
TOLERANCE_PP="${2:-0.5}"
BADGE_JSON=".github/badges/coverage.json"

if [ ! -f "$PROFILE" ]; then
    echo "coverage-badge-check: no profile at $PROFILE (run 'make cover' first)" >&2
    exit 2
fi
if [ ! -f "$BADGE_JSON" ]; then
    echo "coverage-badge-check: no badge JSON at $BADGE_JSON" >&2
    exit 2
fi

# Extract the "total:" line from `go tool cover -func` — same source
# scripts/coverage-gate.sh reads from, so the two checks agree by
# construction.
TOTAL=$(go tool cover -func="$PROFILE" | awk '/^total:/ {gsub(/%/,"",$3); print $3}')
if [ -z "$TOTAL" ]; then
    echo "coverage-badge-check: could not parse total from $PROFILE" >&2
    exit 2
fi

# Extract the number embedded in the badge JSON ("message": "95.2%").
BADGE=$(grep -oE '"message"[[:space:]]*:[[:space:]]*"[0-9.]+%"' "$BADGE_JSON" | \
    grep -oE '[0-9.]+' | head -1)
if [ -z "$BADGE" ]; then
    echo "coverage-badge-check: could not parse message from $BADGE_JSON" >&2
    exit 2
fi

# awk floating-point comparison; abs(TOTAL - BADGE) > TOLERANCE_PP.
DRIFT=$(awk -v a="$TOTAL" -v b="$BADGE" 'BEGIN {d=a-b; if (d<0) d=-d; print d}')
OVER=$(awk -v d="$DRIFT" -v t="$TOLERANCE_PP" 'BEGIN {print (d > t) ? "1" : "0"}')

if [ "$OVER" = "1" ]; then
    cat >&2 <<EOF
coverage-badge-check: FAIL

  current coverage total: ${TOTAL}%
  badge JSON message:     ${BADGE}%
  drift:                  ${DRIFT} percentage points (tolerance: ${TOLERANCE_PP})

Update $BADGE_JSON in the same PR that changes coverage:

  {
    "schemaVersion": 1,
    "label": "coverage",
    "message": "${TOTAL}%",
    "color": "$(awk -v t="$TOTAL" 'BEGIN {
        if (t >= 95) print "green";
        else if (t >= 90) print "yellowgreen";
        else if (t >= 80) print "yellow";
        else if (t >= 70) print "orange";
        else print "red";
    }')"
  }

Rationale: the README badge points at this file via shields.io/endpoint.
An out-of-date badge misrepresents the project to enterprise buyers
reading the README — the same class of trust issue as a stale
compliance claim.
EOF
    exit 1
fi

printf 'coverage-badge-check: ok (total %s%%, badge %s%%, drift %spp <= %spp)\n' \
    "$TOTAL" "$BADGE" "$DRIFT" "$TOLERANCE_PP"
