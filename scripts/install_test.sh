#!/usr/bin/env bash
# install_test.sh — offline tests for scripts/install.sh (security M-16).
#
# Runs install.sh against a fake release: `curl` and `cosign` are stubs
# on $PATH that serve files from a fixture directory and record their
# arguments. Nothing touches the network.
#
# Usage: scripts/install_test.sh [path/to/install.sh]

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INSTALL_SH="${1:-$ROOT/scripts/install.sh}"
VERSION='v9.9.9'
WANT_IDENTITY="https://github.com/sebastienrousseau/rousseau-agent/.github/workflows/release.yml@refs/tags/${VERSION}"
WANT_ISSUER='https://token.actions.githubusercontent.com'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0

# ------------------------------------------------------------ fixture --

asset_name() {
  local os arch
  case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) os=unknown ;; esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) arch="$(uname -m)" ;;
  esac
  printf 'rousseau-agent_%s_%s_%s.tar.gz' "${VERSION#v}" "$os" "$arch"
}

ASSET="$(asset_name)"

# make_release DIR builds a release that passes the SHA-256 check.
make_release() {
  local dir="$1" build
  build="$(mktemp -d "$WORK/build.XXXXXX")"
  mkdir -p "$dir"
  printf '#!/bin/sh\necho rousseau-fake\n' > "$build/rousseau"
  chmod +x "$build/rousseau"
  tar -czf "$dir/$ASSET" -C "$build" rousseau
  (cd "$dir" && sha256sum "$ASSET" > checksums.txt)
  printf 'fake-signature\n' > "$dir/checksums.txt.sig"
  printf 'fake-certificate\n' > "$dir/checksums.txt.pem"
}

# make_stubs DIR [with-cosign] writes curl (and optionally cosign) stubs.
make_stubs() {
  local dir="$1" with_cosign="${2:-}"
  mkdir -p "$dir"
  cat > "$dir/curl" <<'STUB'
#!/usr/bin/env bash
# Minimal curl: serves `--output DST URL` from $FAKE_RELEASE by basename.
set -euo pipefail
dst='' url=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output | -o) dst="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
src="$FAKE_RELEASE/$(basename "$url")"
[ -f "$src" ] || { echo "curl: (22) 404 $url" >&2; exit 22; }
if [ -n "$dst" ]; then cp "$src" "$dst"; else cat "$src"; fi
STUB
  chmod +x "$dir/curl"
  if [ -n "$with_cosign" ]; then
    cat > "$dir/cosign" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$COSIGN_ARGS"
exit "${COSIGN_EXIT:-0}"
STUB
    chmod +x "$dir/cosign"
  fi
}

# run_install NAME [VAR=VALUE ...] runs install.sh in a fresh sandbox
# and leaves exit code, output and cosign args under $WORK/NAME.
run_install() {
  local name="$1" case_dir
  shift
  case_dir="$WORK/$name"
  mkdir -p "$case_dir/home" "$case_dir/bin"
  set +e
  env -i \
    HOME="$case_dir/home" \
    PATH="$STUB_PATH" \
    FAKE_RELEASE="$RELEASE" \
    COSIGN_ARGS="$case_dir/cosign.args" \
    ROUSSEAU_VERSION="$VERSION" \
    ROUSSEAU_INSTALL_DIR="$case_dir/bin" \
    "$@" \
    bash "$INSTALL_SH" > "$case_dir/out" 2>&1
  echo "$?" > "$case_dir/rc"
  set -e
}

# ------------------------------------------------------------ asserts --

ok() {
  pass=$((pass + 1))
  printf 'ok   %s\n' "$1"
}

not_ok() {
  fail=$((fail + 1))
  printf 'FAIL %s\n' "$1"
  [ -f "$WORK/$CASE/out" ] && sed 's/^/     | /' "$WORK/$CASE/out"
  return 0
}

# check DESC CMD... records CMD's success as a pass.
check() {
  local desc="$1"
  shift
  if "$@"; then ok "$CASE: $desc"; else not_ok "$CASE: $desc"; fi
}

rc_zero() { [ "$(cat "$WORK/$CASE/rc")" = 0 ]; }
rc_nonzero() { ! rc_zero; }

expect_rc_nonzero() { check 'exits non-zero' rc_nonzero; }
expect_rc_zero() { check 'exits zero' rc_zero; }
expect_output() { check "says '$1'" grep -qF -- "$1" "$WORK/$CASE/out"; }
expect_installed() { check 'binary installed' test -x "$WORK/$CASE/bin/rousseau"; }
expect_not_installed() { check 'nothing installed' test ! -e "$WORK/$CASE/bin/rousseau"; }

expect_cosign_arg_pair() {
  local flag="$1" value="$2" args="$WORK/$CASE/cosign.args"
  if [ -f "$args" ] && grep -qxF -- "$flag" "$args" &&
    [ "$(grep -xF -A1 -- "$flag" "$args" | tail -n1)" = "$value" ]; then
    ok "$CASE: cosign $flag $value"
  else
    not_ok "$CASE: cosign $flag $value"
  fi
}

expect_no_cosign_arg() {
  local args="$WORK/$CASE/cosign.args"
  if [ -f "$args" ] && grep -qxF -- "$1" "$args"; then
    not_ok "$CASE: cosign called without $1"
  else
    ok "$CASE: cosign called without $1"
  fi
}

# -------------------------------------------------------------- cases --

SYS_PATH='/usr/bin:/bin'
STUBS="$WORK/stubs"
STUBS_NO_COSIGN="$WORK/stubs-no-cosign"
make_stubs "$STUBS" with-cosign
make_stubs "$STUBS_NO_COSIGN"

# 1. Signature missing while cosign is installed: refuse, say why.
CASE=missing-sig
RELEASE="$WORK/rel-$CASE"
make_release "$RELEASE"
rm "$RELEASE/checksums.txt.sig"
STUB_PATH="$STUBS:$SYS_PATH" run_install "$CASE"
expect_rc_nonzero
expect_output 'release signature missing'
expect_not_installed

# 2. Certificate missing: same.
CASE=missing-pem
RELEASE="$WORK/rel-$CASE"
make_release "$RELEASE"
rm "$RELEASE/checksums.txt.pem"
STUB_PATH="$STUBS:$SYS_PATH" run_install "$CASE"
expect_rc_nonzero
expect_output 'release signature missing'
expect_not_installed

# 3. Happy path: cosign gets the exact release workflow identity.
CASE=verified
RELEASE="$WORK/rel-$CASE"
make_release "$RELEASE"
STUB_PATH="$STUBS:$SYS_PATH" run_install "$CASE"
expect_rc_zero
expect_installed
expect_cosign_arg_pair --certificate-identity "$WANT_IDENTITY"
expect_cosign_arg_pair --certificate-oidc-issuer "$WANT_ISSUER"
expect_no_cosign_arg --certificate-identity-regexp

# 4. cosign rejects the signature: refuse.
CASE=cosign-rejects
RELEASE="$WORK/rel-$CASE"
make_release "$RELEASE"
STUB_PATH="$STUBS:$SYS_PATH" run_install "$CASE" COSIGN_EXIT=1
expect_rc_nonzero
expect_not_installed

# 5. Explicit override still installs without a signature.
CASE=skip-cosign
RELEASE="$WORK/rel-$CASE"
make_release "$RELEASE"
rm "$RELEASE/checksums.txt.sig" "$RELEASE/checksums.txt.pem"
STUB_PATH="$STUBS:$SYS_PATH" run_install "$CASE" ROUSSEAU_SKIP_COSIGN=1
expect_rc_zero
expect_installed
expect_output 'skipped by request'

# 6. Tampered archive: SHA-256 check refuses.
CASE=bad-checksum
RELEASE="$WORK/rel-$CASE"
make_release "$RELEASE"
printf 'tampered' >> "$RELEASE/$ASSET"
STUB_PATH="$STUBS:$SYS_PATH" run_install "$CASE"
expect_rc_nonzero
expect_not_installed

# 7. A version that is not a release tag is refused before any download.
CASE=bad-version
RELEASE="$WORK/rel-$CASE"
make_release "$RELEASE"
STUB_PATH="$STUBS:$SYS_PATH" run_install "$CASE" ROUSSEAU_VERSION='v1.0.0/../../evil'
expect_rc_nonzero
expect_output 'not a release tag'
expect_not_installed

# 8. cosign not installed: SHA-256 only, with a warning (unchanged).
if [ -x /usr/bin/cosign ] || [ -x /bin/cosign ]; then
  printf 'skip no-cosign: a system cosign is on %s\n' "$SYS_PATH"
else
  CASE=no-cosign
  RELEASE="$WORK/rel-$CASE"
  make_release "$RELEASE"
  STUB_PATH="$STUBS_NO_COSIGN:$SYS_PATH" run_install "$CASE"
  expect_rc_zero
  expect_installed
  expect_output "cosign not on \$PATH"
fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
