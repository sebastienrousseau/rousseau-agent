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

# Packages allowed below PKG_MIN, with a REASON per entry.
#
# Format: one entry per line, TAB-separated: <package>\t<reason>
# Everything after the first field until end-of-line is the reason.
#
# Two classes of exemption live here:
#
#   1. Uncoverable func main(): coverage is not merged from re-exec'd
#      subprocesses and os.Exit terminates the test binary. Reaching
#      main() would require a `var exit = os.Exit` indirection in
#      every file — worse for readers than the missing statement is
#      worth. The bodies of these packages ARE covered — main() is
#      one statement calling run(), and run() is tested directly.
#
#   2. Structural gaps under active work: packages where the coverage
#      floor is genuinely lower today because of an identified,
#      documented reason (live-service test surface, interactive
#      subprocess, go:embed loader with OS-error branches, etc). Each
#      such entry names the specific paths that are gapped and links
#      to the roadmap wave that closes it. These are NOT "silence the
#      warning" exemptions — they are contract commitments to grind
#      the gap in a specific future wave.
#
# An exemption at or above PKG_MIN is flagged as stale (see below).
# An unlisted package that drops below PKG_MIN fails the gate.
EXEMPTIONS=$(cat <<'EOF'
cmd/rousseau	capped by an uncoverable func main()
examples/embed-a2a	capped by an uncoverable func main()
examples/embed-a2a-federated	capped by an uncoverable func main()
examples/embed-agent	capped by an uncoverable func main()
examples/embed-cost	capped by an uncoverable func main()
examples/embed-identity	capped by an uncoverable func main()
examples/embed-integrations	capped by an uncoverable func main()
examples/embed-recall	capped by an uncoverable func main()
examples/embed-subagent	capped by an uncoverable func main()
internal/state/postgres	live-PG-only paths; sqlite mirror has full branch coverage; grinding requires expanded testcontainers matrix (Wave-2 storage-layer refactor)
internal/state/sqlite	FTS5 vector-blend + schema-migration corruption paths; grind planned as part of the storage-layer refactor (Wave-2)
internal/transport	whatsmeow/signal-cli/Discord-Gateway dial-failure branches need dedicated network fakes; ~5pp gap tracked for Wave-2 hardening
internal/cli	Cobra RunE closures that shell out to an interactive TUI (chat, whatsapp QR pairing) are not reachable via go test; ~3pp gap
internal/agent/opa	OPA policy invalid-rego surface produces WASM compile errors on a fail-safe path; broken-policy fixture harness pending (Wave-2)
internal/skills/bundle	packaged-resource go:embed loader; the ~6pp gap is OS-file-error branches that require injecting a broken filesystem
EOF
)

# Return "1" and print the reason when $1 is in the exempt list.
# Return "0" otherwise. Never prints on non-match.
exemption_reason() {
    # Match on the first tab-separated field; print the rest.
    printf '%s\n' "$EXEMPTIONS" | awk -F'\t' -v p="$1" '$1 == p { for (i=2; i<=NF; i++) printf "%s%s", $i, (i<NF?"\t":""); print ""; found=1; exit } END { exit !found }'
}

is_exempt() {
    exemption_reason "$1" >/dev/null 2>&1
}

# Enumerate exempt package paths (first field only).
exempt_paths() {
    printf '%s\n' "$EXEMPTIONS" | awk -F'\t' 'NF>=1 && $1 != "" { print $1 }'
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
        reason=$(exemption_reason "$pkg" || true)
        if [ -n "$reason" ]; then
            printf '  exempt  %-44s %5s%%  (%s)\n' "$pkg" "$pct" "$reason"
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
while read -r pkg; do
    [ -z "$pkg" ] && continue
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
done <<EOF
$(exempt_paths)
EOF
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
