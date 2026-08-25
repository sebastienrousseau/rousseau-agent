#!/usr/bin/env bash
#
# preflight.sh — verify this host can run the rousseau-agent container
# estate rootlessly, before you spend twenty minutes building images
# and discover a kernel or subuid problem at `systemctl --user start`.
#
# Checks are ordered so the first failure is the most likely cause.
# Exit status is the number of FAILed checks; WARNs do not fail the run.
#
#   make container-check      # or: bash docker/preflight.sh
#
set -uo pipefail

# $USER is not exported in every shell (notably non-login shells and
# some container entrypoints), and `set -u` turns that into a crash
# rather than a wrong answer. Derive it, preferring id(1).
USER_NAME="${USER:-$(id -un 2>/dev/null || echo "uid-$(id -u)")}"

pass=0 warn=0 fail=0

ok()   { printf '  \033[32mok\033[0m    %s\n' "$1"; pass=$((pass + 1)); }
warno(){ printf '  \033[33mwarn\033[0m  %s\n' "$1"; warn=$((warn + 1)); }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; fail=$((fail + 1)); }
note() { printf '        %s\n' "$1"; }

echo "rousseau-agent container preflight"
echo

# -- engine ------------------------------------------------------------
echo "engine"
if command -v podman >/dev/null 2>&1; then
    ok "podman present ($(podman --version 2>/dev/null | head -1))"
    ENGINE=podman
elif command -v docker >/dev/null 2>&1; then
    warno "podman absent, docker present"
    note "Images build fine under docker: make images ENGINE=docker"
    note "But Quadlet units, UserNS=keep-id and the pasta network stack"
    note "are podman features. The quadlet-* targets will not work."
    ENGINE=docker
else
    bad "neither podman nor docker found"
    ENGINE=""
fi
echo

# -- rootless prerequisites -------------------------------------------
echo "rootless"
if [ "$(id -u)" -eq 0 ]; then
    warno "running as root"
    note "These units are designed for rootless operation. Running as"
    note "root works but discards the isolation the Quadlets rely on."
else
    ok "running as uid $(id -u), not root"
fi

if [ -r /etc/subuid ] && grep -q "^${USER_NAME}:" /etc/subuid 2>/dev/null; then
    ok "subuid range allocated for ${USER_NAME}"
else
    bad "no /etc/subuid range for ${USER_NAME}"
    note "Rootless podman cannot map UIDs without one. Fix with:"
    note "  sudo usermod --add-subuids 100000-165535 ${USER_NAME}"
fi

if [ -r /etc/subgid ] && grep -q "^${USER_NAME}:" /etc/subgid 2>/dev/null; then
    ok "subgid range allocated for ${USER_NAME}"
else
    bad "no /etc/subgid range for ${USER_NAME}"
    note "  sudo usermod --add-subgids 100000-165535 ${USER_NAME}"
fi

max_userns=$(cat /proc/sys/user/max_user_namespaces 2>/dev/null || echo 0)
if [ "${max_userns:-0}" -gt 0 ]; then
    ok "unprivileged user namespaces enabled (max=${max_userns})"
else
    bad "user namespaces disabled (/proc/sys/user/max_user_namespaces=0)"
    note "Required by rootless podman AND by bubblewrap inside the"
    note "builder. Enable with:"
    note "  sudo sysctl -w user.max_user_namespaces=15000"
fi
echo

# -- networking --------------------------------------------------------
echo "networking"
if command -v pasta >/dev/null 2>&1 || command -v passt >/dev/null 2>&1; then
    ok "pasta/passt present (Network=pasta in the Quadlets)"
elif command -v slirp4netns >/dev/null 2>&1; then
    warno "slirp4netns present but pasta absent"
    note "The Quadlets specify Network=pasta. Either install passt or"
    note "change Network= in the unit files."
else
    bad "neither pasta nor slirp4netns found"
    note "Rootless podman has no usable network backend. Install passt."
fi
echo

# -- systemd / quadlet -------------------------------------------------
echo "systemd"
if command -v systemctl >/dev/null 2>&1; then
    if systemctl --user show-environment >/dev/null 2>&1; then
        ok "user systemd instance reachable"
    else
        bad "systemctl --user not usable in this session"
        note "Quadlet units are user units. If this is an SSH session,"
        note "enable lingering:  sudo loginctl enable-linger ${USER_NAME}"
    fi
    if [ -d /usr/lib/systemd/user-generators ] &&
       ls /usr/lib/systemd/user-generators/ 2>/dev/null | grep -q quadlet; then
        ok "podman Quadlet generator installed"
    else
        warno "Quadlet generator not found in /usr/lib/systemd/user-generators"
        note "Needs podman >= 4.4. Without it .container files are ignored."
    fi
else
    warno "systemctl absent — Quadlet units cannot be used here"
    note "You can still run the images directly with ${ENGINE:-podman} run."
fi
echo

# -- builder-specific --------------------------------------------------
echo "builder image prerequisites"
if [ -e /dev/fuse ]; then
    ok "/dev/fuse present (fuse-overlayfs for nested containers)"
else
    warno "/dev/fuse absent"
    note "Nested rootless containers inside agent-builder will not work."
fi

seccomp=/usr/share/containers/seccomp.json
if [ -f "$seccomp" ]; then
    ok "seccomp profile present at $seccomp"
else
    bad "seccomp profile missing: $seccomp"
    note "Both Quadlets reference it via SeccompProfile= and will fail"
    note "to start. Install the containers-common package."
fi
echo

# -- summary -----------------------------------------------------------
printf 'summary: %d ok, %d warn, %d failed\n' "$pass" "$warn" "$fail"
if [ "$fail" -gt 0 ]; then
    echo
    echo "Fix the FAILed items above before running make quadlet-install."
fi
exit "$fail"
