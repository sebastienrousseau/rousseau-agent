#!/usr/bin/env bash
#
# coverage-gate.sh — enforce a statement-coverage floor in CI.
#
#   scripts/coverage-gate.sh [coverprofile] [total-threshold] [package-threshold]
#
# Defaults: coverage.out, 95, 95.
#
# Two checks run:
#
#   1. Repo-wide total must be >= total-threshold.
#   2. Every package must be >= package-threshold, except those listed
#      in EXEMPT below.
#
# The exemption list is deliberately short, explicit, and carries a
# reason per entry. A blanket "exclude examples" rule would hide real
# regressions in code that is otherwise fully covered; naming each
# package keeps the exemption honest and reviewable.
#
set -uo pipefail

PROFILE="${1:-coverage.out}"
TOTAL_MIN="${2:-95}"
PKG_MIN="${3:-95}"

if [ ! -f "$PROFILE" ]; then
    echo "coverage-gate: no profile at $PROFILE (run 'make cover' first)" >&2
    exit 2
fi

# Packages allowed below PKG_MIN, with the reason each is exempt.
#
# Every entry here is capped by `func main()`, which `go test -cover`
# cannot reach: os.Exit terminates the test binary, and coverage is not
# merged from re-exec'd subprocesses. Reaching it would require a
# `var exit = os.Exit` indirection in each file. These are executable
# documentation, and that indirection would make them worse at that job
# than the missing statement is worth.
#
# The bodies of these packages ARE covered -- main() is one statement
# calling run(), and run() is tested directly.
EXEMPT_REASON="capped by an uncoverable func main()"
EXEMPT="
cmd/rousseau
examples/embed-a2a
examples/embed-agent
examples/embed-cost
examples/embed-identity
examples/embed-integrations
examples/embed-recall
examples/embed-subagent
"

is_exempt() {
    printf '%s\n' $EXEMPT | grep -qx -- "$1"
}

# -- repo-wide total ---------------------------------------------------
total=$(awk 'NR>1 { n=$2; c=$3; T+=n; if (c+0>0) C+=n }
             END { if (T>0) printf "%.2f", C*100/T; else print "0" }' "$PROFILE")

echo "coverage gate (total >= ${TOTAL_MIN}%, per-package >= ${PKG_MIN}%)"
echo
printf 'total: %s%%\n' "$total"

fail=0
awk -v min="$TOTAL_MIN" -v got="$total" 'BEGIN { exit !(got+0 >= min+0) }' || {
    echo "FAIL: total coverage ${total}% is below ${TOTAL_MIN}%" >&2
    fail=1
}

# -- per package -------------------------------------------------------
below=$(awk 'NR>1 {
    split($1, a, ":"); f = a[1];
    sub(/\/[^\/]+\.go$/, "", f);
    sub(/.*rousseau-agent\//, "", f);
    n = $2; c = $3;
    tot[f] += n; if (c+0 > 0) cov[f] += n;
}
END { for (k in tot) printf "%s %.1f\n", k, cov[k]*100/tot[k] }' "$PROFILE" \
    | sort)

echo
unexpected=0
while read -r pkg pct; do
    [ -z "$pkg" ] && continue
    if awk -v p="$pct" -v m="$PKG_MIN" 'BEGIN { exit !(p+0 < m+0) }'; then
        if is_exempt "$pkg"; then
            printf '  exempt  %-44s %5s%%  (%s)\n' "$pkg" "$pct" "$EXEMPT_REASON"
        else
            printf '  BELOW   %-44s %5s%%\n' "$pkg" "$pct"
            unexpected=$((unexpected + 1))
        fi
    fi
done <<EOF
$below
EOF

if [ "$unexpected" -gt 0 ]; then
    echo >&2
    echo "FAIL: $unexpected package(s) below ${PKG_MIN}% and not exempt" >&2
    echo "Either raise their coverage, or add them to EXEMPT in" >&2
    echo "scripts/coverage-gate.sh WITH A REASON." >&2
    fail=1
fi

# An exemption that is no longer needed is technical debt: it silently
# permits a future regression in a package that has since been fixed.
stale=0
for pkg in $EXEMPT; do
    pct=$(printf '%s\n' "$below" | awk -v p="$pkg" '$1 == p { print $2 }')
    # Absent from the profile means the package no longer exists;
    # present and at/above the floor means the exemption is spent.
    if [ -z "$pct" ]; then
        printf '  stale   %-44s not present in the profile\n' "$pkg"
        stale=$((stale + 1))
    elif awk -v p="$pct" -v m="$PKG_MIN" 'BEGIN { exit !(p+0 >= m+0) }'; then
        printf '  stale   %-44s %5s%% — at or above %s%%, exemption not needed\n' \
            "$pkg" "$pct" "$PKG_MIN"
        stale=$((stale + 1))
    fi
done
if [ "$stale" -gt 0 ]; then
    echo
    echo "note: $stale exemption(s) are no longer needed and should be removed."
fi

echo
if [ "$fail" -eq 0 ]; then
    echo "coverage gate: PASS"
else
    echo "coverage gate: FAIL"
fi
exit "$fail"
