#!/usr/bin/env bash
#
# depcheck.sh — keep the provider and store layers free of the agent
# loop. internal/model holds the conversation types; a provider
# adapter or a store that imports internal/agent drags in SSO, audit
# egress, progress and reliability for no reason, and cannot be
# promoted to pkg/ without them.
#
#   scripts/depcheck.sh
#
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# Packages that must not depend (transitively) on internal/agent.
#
# Not guarded: internal/resilience, whose Recover middleware wraps
# transport.Handler and so legitimately follows internal/transport
# (which runs the loop). Its provider breaker alone is loop-free.
GUARDED=(
  ./internal/model
  ./internal/llm/anthropic
  ./internal/llm/openai
  ./internal/llm/bedrock
  ./internal/llm/vertex
  ./internal/pricing
  ./internal/state
  ./internal/state/history
  ./internal/state/sqlite
  ./internal/state/postgres
  ./internal/skills
  ./internal/mcp
)

bad=0
for pkg in "${GUARDED[@]}"; do
  if go list -deps "$pkg" | grep -qx 'github.com/sebastienrousseau/rousseau-agent/internal/agent'; then
    echo "depcheck: $pkg depends on internal/agent — import internal/model instead" >&2
    bad=1
  fi
done
if [ "$bad" -ne 0 ]; then
  exit 1
fi
echo "depcheck: OK (${#GUARDED[@]} packages independent of internal/agent)"
