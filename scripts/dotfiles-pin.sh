#!/usr/bin/env bash
# dotfiles-pin.sh — guard and maintain the dotfiles pin (security H-4).
#
# The release image (docker/Dockerfile) must carry no dotfiles. The
# dev image (docker/Dockerfile.builder) applies them from one commit,
# held in `ARG DOTFILES_REF=<40-hex>`.
#
# Usage:
#   scripts/dotfiles-pin.sh check            static checks, no network
#   scripts/dotfiles-pin.sh check --fetch    also fetch the pinned commit
#                                            twice and require identical
#                                            content (reproducible input)
#   scripts/dotfiles-pin.sh bump [<sha>]     rewrite DOTFILES_REF to <sha>
#                                            (default: the remote's main)
#                                            and print "old new"
#
# Env overrides (tests): RELEASE_DOCKERFILE, BUILDER_DOCKERFILE,
# DOTFILES_REPO.

set -euo pipefail

cd "$(dirname "$0")/.."

RELEASE_DOCKERFILE="${RELEASE_DOCKERFILE:-docker/Dockerfile}"
BUILDER_DOCKERFILE="${BUILDER_DOCKERFILE:-docker/Dockerfile.builder}"

die() {
  printf 'dotfiles-pin: %s\n' "$*" >&2
  exit 1
}

# arg_value FILE NAME prints the default of `ARG NAME=value` in FILE.
arg_value() {
  sed -n "s/^ARG $2=\\(.*\\)\$/\\1/p" "$1" | head -n1
}

is_commit_id() {
  [[ "$1" =~ ^[0-9a-f]{40}$ ]]
}

check_release_image() {
  # Instructions only: comments may explain why the step is gone.
  if grep -vE '^[[:space:]]*#' "$RELEASE_DOCKERFILE" | grep -nE 'chezmoi|dotfiles'; then
    die "$RELEASE_DOCKERFILE references chezmoi/dotfiles; the release image must not apply dotfiles"
  fi
}

check_builder_pin() {
  local ref
  ref="$(arg_value "$BUILDER_DOCKERFILE" DOTFILES_REF)"
  is_commit_id "$ref" || die "$BUILDER_DOCKERFILE: DOTFILES_REF '$ref' is not a 40-hex commit id"
  printf '%s\n' "$ref"
}

# content_digest REPO REF fetches REF into a fresh repository and
# prints "<tree id> <sha256 of git archive>".
content_digest() {
  local repo="$1" ref="$2" dir
  dir="$(mktemp -d)"
  git -C "$dir" init -q
  git -C "$dir" -c transfer.fsckObjects=true fetch -q --depth=1 "$repo" "$ref"
  [ "$(git -C "$dir" rev-parse FETCH_HEAD)" = "$ref" ] || die "fetched commit differs from $ref"
  printf '%s %s\n' \
    "$(git -C "$dir" rev-parse 'FETCH_HEAD^{tree}')" \
    "$(git -C "$dir" archive --format=tar FETCH_HEAD | sha256sum | cut -d' ' -f1)"
  rm -rf "$dir"
}

check_reproducible() {
  local ref="$1" repo a b
  repo="${DOTFILES_REPO:-$(arg_value "$BUILDER_DOCKERFILE" DOTFILES_REPO)}"
  a="$(content_digest "$repo" "$ref")"
  b="$(content_digest "$repo" "$ref")"
  printf 'fetch A: %s\nfetch B: %s\n' "$a" "$b"
  [ "$a" = "$b" ] || die "pinned dotfiles content differs between two fetches"
}

cmd_check() {
  local ref
  check_release_image
  ref="$(check_builder_pin)"
  if [ "${1:-}" = "--fetch" ]; then
    check_reproducible "$ref"
  fi
  printf 'dotfiles-pin: ok (release image has no dotfiles; builder pinned to %s)\n' "$ref"
}

cmd_bump() {
  local old new repo
  old="$(check_builder_pin)"
  repo="${DOTFILES_REPO:-$(arg_value "$BUILDER_DOCKERFILE" DOTFILES_REPO)}"
  new="${1:-$(git ls-remote "$repo" refs/heads/main | cut -f1)}"
  is_commit_id "$new" || die "resolved ref '$new' is not a 40-hex commit id"
  if [ "$new" != "$old" ]; then
    sed -i "s/^ARG DOTFILES_REF=$old\$/ARG DOTFILES_REF=$new/" "$BUILDER_DOCKERFILE"
    [ "$(arg_value "$BUILDER_DOCKERFILE" DOTFILES_REF)" = "$new" ] || die "failed to rewrite DOTFILES_REF"
  fi
  printf '%s %s\n' "$old" "$new"
}

case "${1:-}" in
  check) shift; cmd_check "$@" ;;
  bump) shift; cmd_bump "$@" ;;
  *) die "usage: $0 check [--fetch] | bump [<sha>]" ;;
esac
