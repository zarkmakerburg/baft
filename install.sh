#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

BAFT_REPO_URL="${BAFT_REPO_URL:-https://github.com/zarkmakerburg/baft.git}"
BAFT_MIRROR_URL="${BAFT_MIRROR_URL:-}"
BAFT_REF="${BAFT_REF:-main}"
# release (default): download signed release artifacts and verify them
# against the pinned root key; no Go, git or build tools on the server.
# source: clone BAFT_REPO_URL at BAFT_REF and build (development only).
BAFT_INSTALL_FROM="${BAFT_INSTALL_FROM:-release}"
BAFT_GITHUB_REPO="${BAFT_GITHUB_REPO:-zarkmakerburg/baft}"
BAFT_VERSION="${BAFT_VERSION:-}"
BAFT_RELEASE_URL="${BAFT_RELEASE_URL:-}"
BAFT_REVOCATIONS_URL="${BAFT_REVOCATIONS_URL:-https://raw.githubusercontent.com/${BAFT_GITHUB_REPO}/main/release/keys/revocations.json}"
# The offline release root public key (release/keys/root.pub). tests/docs
# keeps it equal to that file.
BAFT_PINNED_ROOT_PUB="NJq0LmZ503x67pdXSuNGmSOqaiNJDpYf9nDE8Bwa-JI"
BAFT_ROOT_PUB="${BAFT_ROOT_PUB:-$BAFT_PINNED_ROOT_PUB}"
BAFT_ALLOW_DOWNGRADE="${BAFT_ALLOW_DOWNGRADE:-0}"
# Offline install: a directory unpacked from baft-offline-<version>.tar.gz. No
# network is used; the release is still verified against the pinned root key.
BAFT_OFFLINE_DIR="${BAFT_OFFLINE_DIR:-}"
BAFT_GO_VERSION="${BAFT_GO_VERSION:-1.27.1}"
BAFT_PREFIX="${BAFT_PREFIX:-/opt/baft}"
BAFT_CONFIG_DIR="${BAFT_CONFIG_DIR:-/etc/baft}"
BAFT_STATE_DIR="${BAFT_STATE_DIR:-/var/lib/baft}"
BAFT_BIN="${BAFT_BIN:-/usr/local/bin/baft}"
BAFT_PAIR_BIN="${BAFT_PAIR_BIN:-/usr/local/bin/baft-pair}"
# Root-owned, outside the service user's state directory, so a compromised
# service cannot roll back the record of what was installed.
BAFT_RELEASE_STATE="${BAFT_RELEASE_STATE:-$BAFT_PREFIX/release-state.json}"
BAFT_USER="${BAFT_USER:-baft}"
BAFT_PORT="${BAFT_PORT:-8443}"
BAFT_SERVICE="${BAFT_SERVICE:-baft}"
BAFT_TARGET="${BAFT_TARGET:-127.0.0.1:2443}"
BAFT_ROUTE_LISTEN="${BAFT_ROUTE_LISTEN:-127.0.0.1:1443}"
BAFT_METRICS_LISTEN="${BAFT_METRICS_LISTEN:-127.0.0.1:9191}"
BAFT_RUN_TESTS="${BAFT_RUN_TESTS:-1}"
REPLY_CODE="${BAFT_REPLY_CODE:-}"
ROLE=""
PAIRING_CODE="${BAFT_PAIRING_CODE:-}"
PUBLIC_ADDR="${BAFT_PUBLIC_ADDR:-}"
# EX only: set up the IR server from here over SSH (user@host[:port]).
IR_SSH="${BAFT_IR_SSH:-}"
IR_SSH_KEY="${BAFT_IR_SSH_KEY:-}"
IR_SSH_FINGERPRINT="${BAFT_IR_SSH_FINGERPRINT:-}"
NONINTERACTIVE="${BAFT_NONINTERACTIVE:-0}"
RECORD_SHAPING=0
STEALTH_PRO=0
VERIFY_ONLY_DIR=""
AGENT_ONLY=0
BAFT_BCC_URL="${BAFT_BCC_URL:-}"
BAFT_NODE_ID="${BAFT_NODE_ID:-}"
BAFT_AGENT_BIN="${BAFT_AGENT_BIN:-/usr/local/bin/baft-agent}"
BAFT_AGENT_DIR="${BAFT_AGENT_DIR:-/etc/baft-agent}"
BAFT_AGENT_STATE_DIR="${BAFT_AGENT_STATE_DIR:-/var/lib/baft-agent}"
# systemd unit name and poll interval of the agent (tests run two on one host).
BAFT_AGENT_UNIT="${BAFT_AGENT_UNIT:-baft-agent}"
BAFT_AGENT_INTERVAL="${BAFT_AGENT_INTERVAL:-30s}"
CONFIG="$BAFT_CONFIG_DIR/baft.yaml"
NOISE_KEY="$BAFT_CONFIG_DIR/noise-key.json"
BAFT_SYSTEMD_DIR="${BAFT_SYSTEMD_DIR:-/etc/systemd/system}"
UNIT_FILE="$BAFT_SYSTEMD_DIR/$BAFT_SERVICE.service"
AGENT_UNIT_FILE="$BAFT_SYSTEMD_DIR/$BAFT_AGENT_UNIT.service"

# BCC-only install. This is intentionally separate from the IR/EX transport
# role: installing the control plane must never create or mutate a tunnel.
BCC_ONLY=0
BAFT_BCC_BIN="${BAFT_BCC_BIN:-/usr/local/bin/baft-bcc}"
BAFT_BCC_CONFIG_DIR="${BAFT_BCC_CONFIG_DIR:-/etc/baft-bcc}"
BAFT_BCC_STATE_DIR="${BAFT_BCC_STATE_DIR:-/var/lib/baft-bcc}"
BAFT_BCC_STATE_FILE="${BAFT_BCC_STATE_FILE:-$BAFT_BCC_STATE_DIR/bcc-state.json}"
BAFT_BCC_ADMIN_TOKEN_FILE="${BAFT_BCC_ADMIN_TOKEN_FILE:-$BAFT_BCC_CONFIG_DIR/admin-token}"
BAFT_BCC_ACCESS_FILE="${BAFT_BCC_ACCESS_FILE:-$BAFT_BCC_CONFIG_DIR/access.json}"
BAFT_BCC_JOB_KEY_FILE="${BAFT_BCC_JOB_KEY_FILE:-$BAFT_BCC_CONFIG_DIR/job-key}"
BAFT_BCC_BACKUP_DIR="${BAFT_BCC_BACKUP_DIR:-$BAFT_BCC_STATE_DIR/backups}"
BAFT_BCC_SERVICE="${BAFT_BCC_SERVICE:-baft-bcc}"
BAFT_BCC_UNIT_FILE="$BAFT_SYSTEMD_DIR/$BAFT_BCC_SERVICE.service"
BAFT_BCC_LISTEN="${BAFT_BCC_LISTEN:-127.0.0.1:8080}"
BAFT_BCC_PUBLIC_URL="${BAFT_BCC_PUBLIC_URL:-}"
BAFT_BCC_TLS_CERT="${BAFT_BCC_TLS_CERT:-}"
BAFT_BCC_TLS_KEY="${BAFT_BCC_TLS_KEY:-}"
BAFT_BCC_ALLOW_INSECURE_HTTP="${BAFT_BCC_ALLOW_INSECURE_HTTP:-0}"
# Rerun controls. A rerun inspects first and changes only what the plan shows.
PLAN_ONLY=0
PLAN_JSON=0
ASSUME_YES="${BAFT_ASSUME_YES:-0}"
RE_PAIR=0
CLEANUP=()
APPLYING=0
cleanup(){
  local rc=$?
  trap - EXIT
  if [[ "$APPLYING" == "1" && "$rc" -ne 0 ]]; then rollback_apply || true; fi
  if ((${#CLEANUP[@]})); then rm -rf -- "${CLEANUP[@]}"; fi
  exit "$rc"
}
trap cleanup EXIT

log(){ printf '[baft-install] %s\n' "$*" >&2; }
die(){ log "ERROR: $*"; exit 1; }
need_root(){ [[ "${EUID}" -eq 0 ]] || die "run as root"; }

# P0-FIELD-A: fail before APPLY if the service identity cannot traverse the
# host hierarchy needed to start BAFT. This is deliberately diagnostic-only:
# the installer never chmods/chowns host base directories to make the check
# pass.
preflight_stat() {
  stat -Lc 'mode=%a uid=%u gid=%g' -- "$1" 2>/dev/null || printf 'mode=? uid=? gid=?'
}

preflight_fail() {
  local path="$1" need="$2"
  die "preflight: service user '$BAFT_USER' cannot $need '$path' ($(preflight_stat "$path")); repair host permissions manually; nothing was changed"
}

preflight_dir_access() {
  local path="$1" mode
  [[ -e "$path" ]] || return 0
  [[ -d "$path" ]] || preflight_fail "$path" "traverse non-directory path"

  if [[ "$F_USER" == "1" ]]; then
    runuser -u "$BAFT_USER" -- test -x "$path" 2>/dev/null ||
      preflight_fail "$path" "traverse"
    return 0
  fi

  # On a fresh install the service account does not exist yet. Its future
  # supplementary groups are not knowable, so an already-existing ancestor
  # must at least be traversable by an unprivileged "other" identity.
  mode="$(stat -Lc '%a' -- "$path" 2>/dev/null || true)"
  [[ "$mode" =~ ^[0-7]{3,4}$ ]] ||
    preflight_fail "$path" "inspect"
  (( (8#$mode & 1) != 0 )) ||
    preflight_fail "$path" "traverse"
}

preflight_chain() {
  local target="${1%/}" cur="/" part rest
  [[ "$target" == /* ]] || die "preflight: expected absolute path, got '$target'"
  preflight_dir_access "/"
  rest="${target#/}"
  while [[ -n "$rest" ]]; do
    part="${rest%%/*}"
    if [[ "$rest" == */* ]]; then rest="${rest#*/}"; else rest=""; fi
    [[ -n "$part" ]] || continue
    if [[ "$cur" == "/" ]]; then cur="/$part"; else cur="$cur/$part"; fi
    [[ -e "$cur" ]] || break
    preflight_dir_access "$cur"
  done
}

preflight_service_paths() {
  [[ "$BCC_ONLY" == "1" || "$AGENT_ONLY" == "1" ]] && return 0

  local p
  for p in / /etc /etc/systemd /etc/systemd/system /var /var/lib \
    "$BAFT_CONFIG_DIR" "$BAFT_STATE_DIR" "$BAFT_PREFIX" \
    "$(dirname "$BAFT_BIN")" "$(dirname "$BAFT_PAIR_BIN")" "$BAFT_SYSTEMD_DIR"; do
    preflight_chain "$p"
  done

  if [[ "$F_USER" == "1" ]]; then
    for p in "$CONFIG" "$NOISE_KEY"; do
      [[ -e "$p" ]] || continue
      runuser -u "$BAFT_USER" -- test -r "$p" 2>/dev/null ||
        preflight_fail "$p" "read"
    done
    if [[ -e "$BAFT_BIN" ]]; then
      runuser -u "$BAFT_USER" -- test -x "$BAFT_BIN" 2>/dev/null ||
        preflight_fail "$BAFT_BIN" "execute"
    fi
  fi
}

# show_logo prints the BAFT mark in 256-colour block art on a colour
# terminal, and a plain title otherwise (pipes, logs, NO_COLOR, TERM=dumb).
show_logo() {
  if [[ -t 2 && -z "${NO_COLOR:-}" && "${TERM:-dumb}" != "dumb" ]]; then
    local line
    while IFS= read -r line; do printf '  %b\n' "$line" >&2; done <<'LOGO'
\033[0m           \033[0;38;5;130m▀\033[0;38;5;136m▀\033[0m                   \033[0m
\033[0m                                \033[0m
\033[0m      \033[0;38;5;173m▄\033[38;5;94;48;5;94m▀\033[38;5;58;48;5;240m▀▀▀\033[38;5;58;48;5;58m▀\033[38;5;240;48;5;240m▀\033[38;5;238;48;5;238m▀\033[38;5;240;48;5;240m▀▀▀▀\033[38;5;59;48;5;59m▀\033[0;38;5;59m▄▄\033[0m           \033[0m
\033[0m      \033[38;5;172;48;5;136m▀\033[38;5;173;48;5;172m▀\033[38;5;95;48;5;179m▀\033[38;5;240;48;5;180m▀\033[38;5;238;48;5;238m▀\033[38;5;58;48;5;240m▀\033[38;5;240;48;5;240m▀\033[38;5;238;48;5;238m▀\033[38;5;240;48;5;238m▀▀▀\033[38;5;240;48;5;240m▀▀\033[38;5;59;48;5;240m▀▀\033[38;5;59;48;5;59m▀\033[38;5;101;48;5;59m▀\033[0;38;5;137m▄\033[0m        \033[0m
\033[0m     \033[0;38;5;130m▀\033[38;5;94;48;5;215m▀\033[38;5;94;48;5;137m▀\033[38;5;130;48;5;94m▀\033[38;5;180;48;5;130m▀\033[38;5;188;48;5;137m▀\033[38;5;95;48;5;222m▀\033[38;5;238;48;5;179m▀\033[0;38;5;238m▀▀▀\033[38;5;238;48;5;238m▀▀▀\033[38;5;240;48;5;238m▀\033[38;5;240;48;5;240m▀▀▀▀\033[38;5;137;48;5;240m▀\033[0;38;5;179m▄\033[0m      \033[0m
\033[0m      \033[0;38;5;136m▀\033[38;5;215;48;5;130m▀\033[38;5;172;48;5;94m▀\033[38;5;179;48;5;94m▀\033[38;5;180;48;5;173m▀\033[38;5;179;48;5;173m▀\033[38;5;222;48;5;173m▀\033[38;5;179;48;5;229m▀\033[0;38;5;215m▄\033[0m   \033[0;38;5;238m▀▀\033[38;5;238;48;5;238m▀\033[38;5;240;48;5;238m▀▀▀\033[38;5;238;48;5;238m▀\033[38;5;137;48;5;136m▀\033[38;5;221;48;5;215m▀\033[0m     \033[0m
\033[0m       \033[0;38;5;172m▀\033[38;5;179;48;5;179m▀\033[38;5;94;48;5;179m▀\033[38;5;94;48;5;136m▀\033[38;5;137;48;5;179m▀\033[38;5;58;48;5;223m▀\033[38;5;94;48;5;136m▀\033[38;5;136;48;5;137m▀\033[38;5;173;48;5;137m▀\033[0;38;5;130m▄\033[0m   \033[0;38;5;238m▀\033[38;5;238;48;5;238m▀▀▀▀\033[38;5;180;48;5;179m▀\033[38;5;179;48;5;179m▀\033[38;5;172;48;5;173m▀\033[0m    \033[0m
\033[0m          \033[0;38;5;130m▀\033[38;5;130;48;5;172m▀\033[38;5;130;48;5;130m▀\033[38;5;130;48;5;136m▀\033[38;5;94;48;5;94m▀\033[38;5;179;48;5;58m▀\033[38;5;130;48;5;130m▀\033[38;5;94;48;5;136m▀\033[0;38;5;130m▄\033[0m  \033[38;5;238;48;5;238m▀▀\033[38;5;238;48;5;101m▀\033[38;5;143;48;5;222m▀\033[38;5;136;48;5;94m▀\033[38;5;179;48;5;222m▀\033[38;5;179;48;5;179m▀\033[0m    \033[0m
\033[0m      \033[38;5;172;48;5;130m▀\033[38;5;136;48;5;94m▀\033[0m     \033[0;38;5;136m▀\033[38;5;172;48;5;240m▀\033[38;5;130;48;5;58m▀\033[38;5;94;48;5;94m▀▀\033[38;5;94;48;5;240m▀\033[0;38;5;240m▄\033[0m \033[38;5;240;48;5;186m▀\033[38;5;94;48;5;173m▀\033[38;5;222;48;5;223m▀\033[38;5;180;48;5;137m▀\033[38;5;137;48;5;94m▀\033[38;5;179;48;5;136m▀\033[0;38;5;130m▀\033[0m    \033[0m
\033[0m               \033[0;38;5;238m▀\033[38;5;238;48;5;240m▀\033[38;5;238;48;5;137m▀\033[38;5;58;48;5;173m▀\033[38;5;94;48;5;229m▀\033[38;5;229;48;5;180m▀\033[38;5;173;48;5;94m▀\033[38;5;179;48;5;130m▀\033[38;5;137;48;5;94m▀\033[38;5;240;48;5;137m▀\033[38;5;179;48;5;179m▀\033[0;38;5;172m▀\033[0m     \033[0m
\033[0m       \033[0;38;5;136m▄\033[0;38;5;172m▄\033[0;38;5;136m▄\033[0m   \033[0;38;5;185m▄\033[0;38;5;179m▄\033[38;5;179;48;5;229m▀\033[38;5;223;48;5;137m▀\033[38;5;173;48;5;130m▀\033[38;5;179;48;5;172m▀\033[38;5;179;48;5;58m▀\033[38;5;58;48;5;94m▀\033[38;5;94;48;5;215m▀\033[38;5;179;48;5;130m▀\033[38;5;130;48;5;130m▀\033[0;38;5;172m▀\033[0;38;5;130m▀\033[0m      \033[0m
\033[0m        \033[0;38;5;94m▀\033[0;38;5;179m▄▄\033[38;5;215;48;5;222m▀\033[38;5;222;48;5;130m▀\033[38;5;173;48;5;130m▀\033[38;5;221;48;5;130m▀\033[38;5;137;48;5;240m▀\033[38;5;240;48;5;130m▀\033[38;5;136;48;5;172m▀\033[38;5;94;48;5;94m▀▀\033[38;5;172;48;5;238m▀\033[38;5;94;48;5;238m▀\033[0;38;5;94m▀\033[0m  \033[38;5;179;48;5;94m▀\033[0m      \033[0m
\033[0m       \033[0;38;5;172m▄\033[38;5;179;48;5;179m▀\033[38;5;179;48;5;136m▀\033[38;5;179;48;5;130m▀\033[38;5;94;48;5;52m▀\033[38;5;240;48;5;130m▀\033[38;5;136;48;5;172m▀\033[38;5;94;48;5;130m▀\033[38;5;130;48;5;94m▀\033[38;5;172;48;5;238m▀\033[38;5;240;48;5;238m▀\033[38;5;238;48;5;238m▀\033[38;5;238;48;5;240m▀▀\033[38;5;240;48;5;240m▀\033[38;5;240;48;5;59m▀\033[38;5;59;48;5;59m▀\033[38;5;143;48;5;59m▀\033[0;38;5;137m▄\033[0m      \033[0m
\033[0m      \033[0;38;5;179m▄\033[38;5;215;48;5;136m▀\033[38;5;94;48;5;240m▀\033[38;5;94;48;5;173m▀\033[38;5;137;48;5;130m▀\033[38;5;130;48;5;172m▀\033[38;5;215;48;5;172m▀\033[0;38;5;136m▀\033[0;38;5;58m▀\033[0;38;5;238m▀▀\033[38;5;238;48;5;238m▀▀▀\033[38;5;240;48;5;238m▀\033[38;5;240;48;5;240m▀\033[38;5;240;48;5;238m▀\033[38;5;240;48;5;240m▀▀▀\033[38;5;179;48;5;58m▀\033[0;38;5;179m▄\033[0m    \033[0m
\033[0m     \033[0;38;5;173m▄\033[38;5;173;48;5;130m▀\033[38;5;94;48;5;137m▀\033[38;5;136;48;5;172m▀\033[38;5;215;48;5;172m▀\033[38;5;130;48;5;238m▀\033[0;38;5;130m▀\033[0m       \033[0;38;5;238m▀\033[38;5;238;48;5;238m▀▀▀▀▀\033[38;5;240;48;5;238m▀\033[38;5;238;48;5;238m▀\033[38;5;137;48;5;58m▀\033[38;5;215;48;5;222m▀\033[0m   \033[0m
\033[0m     \033[38;5;130;48;5;136m▀\033[38;5;137;48;5;173m▀\033[38;5;172;48;5;173m▀\033[38;5;94;48;5;238m▀\033[38;5;240;48;5;58m▀\033[0m  \033[0;38;5;179m▄\033[0m        \033[0;38;5;238m▀\033[38;5;238;48;5;238m▀▀▀▀▀\033[38;5;94;48;5;180m▀\033[38;5;222;48;5;179m▀\033[38;5;179;48;5;172m▀\033[0m  \033[0m
\033[0m     \033[38;5;173;48;5;179m▀\033[38;5;136;48;5;172m▀\033[38;5;94;48;5;238m▀\033[38;5;238;48;5;238m▀\033[38;5;58;48;5;58m▀\033[38;5;59;48;5;58m▀\033[0;38;5;94m▀\033[0;38;5;136m▀\033[0m          \033[38;5;238;48;5;238m▀▀\033[38;5;238;48;5;95m▀\033[38;5;94;48;5;173m▀\033[38;5;215;48;5;179m▀\033[38;5;173;48;5;136m▀\033[38;5;136;48;5;179m▀\033[0m  \033[0m
\033[0m      \033[38;5;172;48;5;172m▀\033[38;5;240;48;5;240m▀\033[38;5;238;48;5;238m▀\033[38;5;58;48;5;58m▀\033[38;5;58;48;5;59m▀\033[38;5;240;48;5;240m▀\033[0;38;5;59m▄\033[0m         \033[0;38;5;179m▄\033[38;5;238;48;5;137m▀\033[38;5;94;48;5;222m▀\033[38;5;222;48;5;137m▀\033[38;5;137;48;5;94m▀\033[38;5;130;48;5;130m▀\033[38;5;137;48;5;179m▀\033[38;5;179;48;5;130m▀\033[0m  \033[0m
\033[0m       \033[38;5;130;48;5;130m▀\033[38;5;238;48;5;240m▀\033[38;5;58;48;5;240m▀▀\033[38;5;59;48;5;238m▀\033[38;5;240;48;5;59m▀\033[38;5;59;48;5;240m▀\033[0;38;5;58m▄\033[0;38;5;130m▄\033[0;38;5;172m▄\033[0;38;5;179m▄▄▄\033[38;5;172;48;5;215m▀\033[38;5;221;48;5;94m▀\033[38;5;179;48;5;94m▀\033[38;5;179;48;5;130m▀\033[38;5;137;48;5;240m▀\033[38;5;240;48;5;137m▀\033[38;5;137;48;5;173m▀\033[38;5;137;48;5;130m▀\033[0;38;5;130m▀\033[0m   \033[0m
\033[0m        \033[0;38;5;94m▀\033[38;5;137;48;5;172m▀\033[38;5;95;48;5;94m▀\033[38;5;238;48;5;238m▀\033[38;5;240;48;5;238m▀\033[38;5;59;48;5;238m▀\033[38;5;240;48;5;240m▀\033[38;5;238;48;5;240m▀\033[38;5;58;48;5;238m▀▀\033[38;5;94;48;5;238m▀\033[38;5;136;48;5;238m▀\033[38;5;58;48;5;240m▀\033[38;5;94;48;5;58m▀\033[38;5;130;48;5;58m▀\033[38;5;94;48;5;240m▀\033[38;5;94;48;5;58m▀\033[38;5;136;48;5;240m▀\033[38;5;58;48;5;94m▀\033[0;38;5;130m▀\033[0m    \033[0m
\033[0m          \033[0;38;5;94m▀\033[0;38;5;240m▀\033[38;5;238;48;5;58m▀\033[38;5;238;48;5;238m▀▀▀\033[38;5;240;48;5;238m▀▀\033[38;5;238;48;5;238m▀▀▀▀\033[38;5;238;48;5;240m▀\033[38;5;238;48;5;58m▀\033[38;5;238;48;5;136m▀\033[0;38;5;94m▀\033[0m      \033[0m
\033[0m              \033[0;38;5;58m▀\033[0;38;5;240m▀\033[0;38;5;238m▀▀▀\033[0;38;5;240m▀▀\033[0;38;5;58m▀\033[0m          \033[0m
LOGO
    printf '\n  \033[1;38;5;179mB  A  F  T\033[0m  \033[38;5;245mtransport installer\033[0m\n\n' >&2
  else
    printf 'BAFT transport installer\n' >&2
  fi
}

usage() {
  cat <<EOF
Usage:
  sudo bash install.sh            # asks: outside Iran (EX) or inside Iran (IR)
  sudo bash install.sh --role ex --public-address HOST_OR_IP [--reply-code BAFTREPLY1:...]
  sudo bash install.sh --role ex --ir-ssh root@IR_HOST[:22] [--ir-ssh-key FILE] [--ir-ssh-fingerprint SHA256:...]
      # also installs and pairs the IR over SSH; nobody logs in to the IR
  sudo bash install.sh --role ir [--pairing-code BAFTPAIR1:...]
  sudo bash install.sh --role ex --stealth-pro  # existing pinned Noise config
  sudo bash install.sh --role ir --stealth-pro  # enable on BOTH peers
  sudo bash install.sh --agent-only --bcc-url https://bcc.example.com --node-id ex-1
      # server enrollment: binaries + baft-agent only, no tunnel yet; the agent
      # token comes from BAFT_AGENT_TOKEN_FILE (or BAFT_AGENT_TOKEN) and the
      # pinned BCC job key from BAFT_BCC_JOB_KEY
  sudo bash install.sh --bcc-only
      # installs the BAFT Command Center service only; no IR/EX tunnel is created
      # default listen is loopback 127.0.0.1:8080 and web credentials are shown once

Options:
  --version vX.Y.Z    install this signed release (default: the latest)
  --allow-downgrade   accept a release older than the one installed
  --from-source       build from BAFT_REPO_URL at BAFT_REF (development only)
  --plan              inspect this server and show what a run would do, then stop.
                      Changes nothing, needs no root and no network (with --json: machine-readable).
  --yes               apply a plan that changes an existing install without asking
                      (also BAFT_ASSUME_YES=1). A fresh install never asks.
  --ir-ssh USER@HOST[:PORT]
                      EX only: install the IR over SSH from this server and pair
                      it, so nobody logs in to the IR. Auth with --ir-ssh-key FILE
                      or a password (asked, or BAFT_IR_SSH_PASSWORD). The IR host
                      key is shown for confirmation, or pinned with
                      --ir-ssh-fingerprint SHA256:... (required without a
                      terminal). A non-root user needs passwordless sudo.
  --re-pair           start a NEW pairing on an installed node (the current config is
                      backed up first); a rerun never re-pairs on its own
  --bcc-only          install only BAFT Command Center (no tunnel/data-plane changes)
  --bcc-listen ADDR   BCC listen address (default 127.0.0.1:8080)
  --bcc-public-url URL
                      operator-facing base URL used in the final access handoff;
                      it does not make a loopback listener public
  --bcc-tls-cert FILE TLS certificate for a non-loopback/HTTPS BCC listener
  --bcc-tls-key FILE  TLS private key (must be supplied with --bcc-tls-cert)
  --bcc-allow-insecure-http
                      explicitly permit plain HTTP on a non-loopback BCC listener
  --offline DIR       install from an unpacked baft-offline-<version>.tar.gz: no
                      network, no apt; python3 and openssl must already be installed.
                      The release is verified exactly as for an online install.

By default the installer downloads the signed release for this architecture,
verifies it against the pinned root key and the current revocation list, and
refuses a downgrade or a re-tagged version (state in BAFT_RELEASE_STATE).

Rerunning is safe. The installer inspects the server first (FRESH_INSTALL,
PARTIAL_INSTALL, ALREADY_INSTALLED, CURRENT, UPGRADE_AVAILABLE, REPAIR_REQUIRED,
BROKEN_INSTALL), shows a plan, and applies only that plan, with a rollback if
it fails. It never rotates a key or certificate, overwrites a valid config,
re-pairs, removes a tunnel, clears state, resets the release state or touches an
ownership marker on its own; a healthy, current install is left exactly as it is.

Environment:
  BAFT_INSTALL_FROM (release|source) BAFT_VERSION BAFT_RELEASE_URL
  BAFT_GITHUB_REPO BAFT_REVOCATIONS_URL BAFT_ROOT_PUB BAFT_RELEASE_STATE
  BAFT_ALLOW_DOWNGRADE BAFT_OFFLINE_DIR
  BAFT_REPO_URL BAFT_MIRROR_URL BAFT_REF BAFT_GO_VERSION (source installs)
  BAFT_PREFIX BAFT_CONFIG_DIR BAFT_STATE_DIR BAFT_PORT BAFT_SERVICE
  BAFT_TARGET (EX: fixed IP:port traffic exits to, default 127.0.0.1:2443)
  BAFT_ROUTE_LISTEN (IR: loopback address local clients use, default 127.0.0.1:1443)
  BAFT_METRICS_LISTEN BAFT_RUN_TESTS BAFT_PAIRING_CODE BAFT_REPLY_CODE
  BAFT_BCC_BIN BAFT_BCC_CONFIG_DIR BAFT_BCC_STATE_DIR BAFT_BCC_STATE_FILE
  BAFT_BCC_ADMIN_TOKEN_FILE BAFT_BCC_ACCESS_FILE BAFT_BCC_JOB_KEY_FILE
  BAFT_BCC_BACKUP_DIR BAFT_BCC_SERVICE BAFT_BCC_LISTEN BAFT_BCC_PUBLIC_URL
  BAFT_BCC_TLS_CERT BAFT_BCC_TLS_KEY BAFT_BCC_ALLOW_INSECURE_HTTP
  BAFT_SHAPE_DISTRIBUTION BAFT_SHAPE_MEAN BAFT_SHAPE_STDDEV
  BAFT_SHAPE_MAX_PADDING BAFT_SHAPE_MAX_RATIO
  BAFT_JITTER_MIN_US BAFT_JITTER_MAX_US
Stealth Pro requires an existing baft.yaml with noise.key_file and
noise.peer_public_key. It updates that configuration without re-pairing.
It is experimental; statistical similarity to HTTPS is not established.

Pairing: the EX prints a one-time BAFTPAIR1 code. Run the IR installer with
it; the IR writes its config, starts, and prints a BAFTREPLY1 code. Give that
to the EX (the installer waits for it, or run the printed baft-pair ex-accept
command later); the EX then writes its config and starts.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --role) ROLE="${2:-}"; shift 2 ;;
    --pairing-code) PAIRING_CODE="${2:-}"; shift 2 ;;
    --public-address) PUBLIC_ADDR="${2:-}"; shift 2 ;;
    --reply-code) REPLY_CODE="${2:-}"; shift 2 ;;
    --ir-ssh) IR_SSH="${2:-}"; shift 2 ;;
    --ir-ssh-key) IR_SSH_KEY="${2:-}"; shift 2 ;;
    --ir-ssh-fingerprint) IR_SSH_FINGERPRINT="${2:-}"; shift 2 ;;
    --enable-record-shaping) die "use --stealth-pro with a pinned Noise configuration" ;;
    --stealth-pro) STEALTH_PRO=1; RECORD_SHAPING=1; shift ;;
    --version) BAFT_VERSION="${2:-}"; shift 2 ;;
    --allow-downgrade) BAFT_ALLOW_DOWNGRADE=1; shift ;;
    --from-source) BAFT_INSTALL_FROM=source; shift ;;
    --offline) BAFT_OFFLINE_DIR="${2:-}"; shift 2 ;;
    --verify-release) VERIFY_ONLY_DIR="${2:-}"; shift 2 ;;
    --plan|--dry-run) PLAN_ONLY=1; shift ;;
    --json) PLAN_JSON=1; shift ;;
    --yes|-y) ASSUME_YES=1; shift ;;
    --re-pair) RE_PAIR=1; shift ;;
    --agent-only) AGENT_ONLY=1; shift ;;
    --bcc-only) BCC_ONLY=1; shift ;;
    --bcc-listen) BAFT_BCC_LISTEN="${2:-}"; shift 2 ;;
    --bcc-public-url) BAFT_BCC_PUBLIC_URL="${2:-}"; shift 2 ;;
    --bcc-tls-cert) BAFT_BCC_TLS_CERT="${2:-}"; shift 2 ;;
    --bcc-tls-key) BAFT_BCC_TLS_KEY="${2:-}"; shift 2 ;;
    --bcc-allow-insecure-http) BAFT_BCC_ALLOW_INSECURE_HTTP=1; shift ;;
    --bcc-url) BAFT_BCC_URL="${2:-}"; shift 2 ;;
    --node-id) BAFT_NODE_ID="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

# Where questions are read from. Run from a file (`bash install.sh`), stdin
# answers them as before: a terminal, or a pipe/FIFO that scripts feed the
# reply code through. Run piped (`curl ... | sudo bash`), stdin is this script
# itself, so questions go to the controlling terminal instead. With
# BAFT_NONINTERACTIVE=1, or piped without a terminal, nothing is asked and
# missing answers are errors.
ASK_FD=""
ASK_TTY=0
if [[ "$NONINTERACTIVE" != "1" ]]; then
  if [[ -n "${BASH_SOURCE[0]:-}" ]]; then
    ASK_FD=0
    [[ -t 0 ]] && ASK_TTY=1
  elif ( : </dev/tty ) 2>/dev/null; then
    exec 3</dev/tty
    ASK_FD=3
    ASK_TTY=1
  fi
fi
# ask VAR PROMPT [read flags...]: fails when there is nothing to ask or no answer.
ask() {
  local __var=$1 __prompt=$2
  shift 2
  [[ -n "$ASK_FD" ]] || return 1
  IFS= read -r "$@" -p "$__prompt" "$__var" <&"$ASK_FD"
}

# ---------------------------------------------------------------------------
# Inspect, plan, apply (rerunnable installer).
#
#   INSPECT -> BUILD PLAN -> SHOW PLAN -> APPLY -> VERIFY -> ROLLBACK IF FAILED
#
# RERUN != REINSTALL. Inspecting changes nothing. The plan lists every item as
# keep, create, update, replace, start or restart, and only the items that are
# not "keep" are touched. Long-lived secrets (the Noise key, the outer PKI, the
# agent token and job key), a valid config, the release state, tunnels and
# ownership markers are never regenerated, overwritten or removed by a rerun.
# ---------------------------------------------------------------------------
sha_of() { sha256sum "$1" 2>/dev/null | awk '{print $1}'; }
same_file() { [[ -f "$1" && -f "$2" ]] && cmp -s -- "$1" "$2"; }
svc_active() { [[ "$(systemctl is-active "$1" 2>/dev/null || true)" == "active" ]]; }
svc_enabled() { [[ "$(systemctl is-enabled "$1" 2>/dev/null || true)" == "enabled" ]]; }
json_escape() { printf '%s' "$1" | tr -d '\n\r\t' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }

PLAN_ITEM=(); PLAN_ACT=(); PLAN_DETAIL=(); PLAN_CHANGES=0
plan_add() {
  PLAN_ITEM+=("$1"); PLAN_ACT+=("$2"); PLAN_DETAIL+=("$3")
  case "$2" in keep|skip) ;; *) PLAN_CHANGES=$((PLAN_CHANGES + 1)) ;; esac
}

STATE=""; STATE_WHY=""; INSTALLED_REL=""
F_BIN=0; F_PAIR=0; F_AGENT_BIN=0; F_RELSTATE=0; F_UNIT=0; F_UNIT_MANAGED=0; F_CFG=0; F_CFG_VALID=0
F_KEY=0; F_KEY_OK=0; F_PKI=0; F_PENDING=0; F_USER=0; F_ACTIVE=0; F_ENABLED=0; F_DIRS=0
F_AGENT_UNIT=0; F_AGENT_ACTIVE=0; F_AGENT_ENABLED=0; F_TOKEN=0; F_JOBKEY=0
F_BCC_BIN=0; F_BCC_UNIT=0; F_BCC_UNIT_MANAGED=0; F_BCC_ACTIVE=0; F_BCC_ENABLED=0
F_BCC_ADMIN=0; F_BCC_ACCESS=0; F_BCC_JOBKEY=0; F_BCC_STATE=0; F_BCC_DIRS=0

INSPECTED=0; CFG_INVALID_WHY=""
inspect_install() {
  INSPECTED=1
  # A binary that does not run counts as missing: it cannot vouch for the config.
  if [[ -x "$BAFT_BIN" ]] && "$BAFT_BIN" version >/dev/null 2>&1; then F_BIN=1; fi
  [[ -x "$BAFT_PAIR_BIN" ]] && F_PAIR=1
  [[ -x "$BAFT_AGENT_BIN" ]] && F_AGENT_BIN=1
  if [[ -f "$BAFT_RELEASE_STATE" ]]; then
    F_RELSTATE=1
    INSTALLED_REL="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$BAFT_RELEASE_STATE" | head -n1)"
  fi
  [[ -f "$UNIT_FILE" ]] && F_UNIT=1
  if [[ "$F_UNIT" == 1 ]] && grep -q '^# baft-managed: true' "$UNIT_FILE" 2>/dev/null; then F_UNIT_MANAGED=1; fi
  [[ -f "$BAFT_CONFIG_DIR/baft.managed.json" ]] && F_UNIT_MANAGED=1
  [[ -f "$CONFIG" ]] && F_CFG=1
  if [[ "$F_CFG" == 1 && "$F_BIN" == 1 ]] && "$BAFT_BIN" config validate --file "$CONFIG" >/dev/null 2>&1; then F_CFG_VALID=1; fi
  [[ -f "$NOISE_KEY" ]] && F_KEY=1
  if [[ "$F_KEY" == 1 ]] && [[ "$(stat -c '%a %U' "$NOISE_KEY" 2>/dev/null)" == "600 $BAFT_USER" ]]; then F_KEY_OK=1; fi
  [[ -f "$BAFT_CONFIG_DIR/pki/ca.pem" && -f "$BAFT_CONFIG_DIR/pki/server.pem" && -f "$BAFT_CONFIG_DIR/pki/server.key" ]] && F_PKI=1
  [[ -f "$BAFT_STATE_DIR/pairing.ex.json" ]] && F_PENDING=1
  id -u "$BAFT_USER" >/dev/null 2>&1 && F_USER=1
  [[ -d "$BAFT_CONFIG_DIR" && -d "$BAFT_STATE_DIR" && -d "$BAFT_PREFIX" ]] && F_DIRS=1
  if svc_active "$BAFT_SERVICE.service"; then F_ACTIVE=1; fi
  if svc_enabled "$BAFT_SERVICE.service"; then F_ENABLED=1; fi
  [[ -f "$AGENT_UNIT_FILE" ]] && F_AGENT_UNIT=1
  if svc_active "$BAFT_AGENT_UNIT.service"; then F_AGENT_ACTIVE=1; fi
  if svc_enabled "$BAFT_AGENT_UNIT.service"; then F_AGENT_ENABLED=1; fi
  [[ -f "$BAFT_AGENT_DIR/token" ]] && F_TOKEN=1
  [[ -f "$BAFT_AGENT_DIR/bcc-job.pub" ]] && F_JOBKEY=1

  [[ -x "$BAFT_BCC_BIN" ]] && F_BCC_BIN=1
  [[ -f "$BAFT_BCC_UNIT_FILE" ]] && F_BCC_UNIT=1
  if [[ "$F_BCC_UNIT" == 1 ]] &&
     grep -q '^# baft-managed: true' "$BAFT_BCC_UNIT_FILE" 2>/dev/null &&
     grep -q '^# baft-component: bcc' "$BAFT_BCC_UNIT_FILE" 2>/dev/null; then F_BCC_UNIT_MANAGED=1; fi
  [[ -s "$BAFT_BCC_ADMIN_TOKEN_FILE" ]] && F_BCC_ADMIN=1
  [[ -s "$BAFT_BCC_ACCESS_FILE" ]] && F_BCC_ACCESS=1
  [[ -s "$BAFT_BCC_JOB_KEY_FILE" ]] && F_BCC_JOBKEY=1
  [[ -f "$BAFT_BCC_STATE_FILE" ]] && F_BCC_STATE=1
  [[ -d "$BAFT_BCC_CONFIG_DIR" && -d "$BAFT_BCC_STATE_DIR" ]] && F_BCC_DIRS=1
  if svc_active "$BAFT_BCC_SERVICE.service"; then F_BCC_ACTIVE=1; fi
  if svc_enabled "$BAFT_BCC_SERVICE.service"; then F_BCC_ENABLED=1; fi
  return 0
}

classify_state() {
  if [[ -n "$CFG_INVALID_WHY" ]]; then STATE="BROKEN_INSTALL"; STATE_WHY="$CFG_INVALID_WHY"; return; fi
  if [[ "$BCC_ONLY" == "1" ]]; then
    if [[ "$F_BCC_UNIT" == 1 && "$F_BCC_UNIT_MANAGED" == 0 ]]; then
      STATE="BROKEN_INSTALL"; STATE_WHY="$BAFT_BCC_UNIT_FILE exists but is not BAFT-managed"; return
    fi
    if [[ "$F_BCC_BIN$F_BCC_UNIT$F_BCC_ADMIN$F_BCC_ACCESS$F_BCC_JOBKEY$F_BCC_STATE" == "000000" ]]; then
      STATE="FRESH_INSTALL"; STATE_WHY="no BAFT Command Center install exists here"; return
    fi
    if [[ "$F_BCC_BIN" == 1 && "$F_BCC_UNIT" == 1 && "$F_BCC_ADMIN" == 1 && "$F_BCC_ACCESS" == 1 && "$F_BCC_JOBKEY" == 1 && "$F_BCC_STATE" == 1 ]]; then
      if [[ "$F_BCC_ACTIVE" == 1 && "$F_BCC_ENABLED" == 1 ]]; then
        STATE="ALREADY_INSTALLED"; STATE_WHY="BAFT Command Center is installed and running"
      else
        STATE="REPAIR_REQUIRED"; STATE_WHY="BAFT Command Center is installed but its service is not running or enabled"
      fi
      return
    fi
    STATE="PARTIAL_INSTALL"; STATE_WHY="some BAFT Command Center files are missing"; return
  fi
  if [[ "$AGENT_ONLY" == "1" ]]; then
    # The binaries are shared by every instance on a host, so only this
    # instance's own files decide whether it is fresh.
    if [[ "$F_AGENT_UNIT$F_TOKEN$F_JOBKEY" == "000" ]]; then
      STATE="FRESH_INSTALL"; STATE_WHY="nothing of BAFT is installed here"; return
    fi
    if [[ "$F_BIN" == 1 && "$F_PAIR" == 1 && "$F_AGENT_BIN" == 1 && "$F_AGENT_UNIT" == 1 && "$F_TOKEN" == 1 && "$F_JOBKEY" == 1 ]]; then
      if [[ "$F_AGENT_ACTIVE" == 1 ]]; then STATE="ALREADY_INSTALLED"; STATE_WHY="the agent is installed and running"
      else STATE="REPAIR_REQUIRED"; STATE_WHY="the agent is installed but not running"; fi
      return
    fi
    STATE="PARTIAL_INSTALL"; STATE_WHY="some agent files are missing"; return
  fi
  if [[ "$F_UNIT$F_CFG$F_KEY" == "000" ]]; then
    STATE="FRESH_INSTALL"; STATE_WHY="nothing of BAFT is installed here"; return
  fi
  if [[ "$F_CFG" == 1 && "$F_BIN" == 1 && "$F_CFG_VALID" == 0 ]]; then
    STATE="BROKEN_INSTALL"; STATE_WHY="$CONFIG exists but does not validate"; return
  fi
  if [[ "$F_CFG" == 1 && "$F_KEY" == 0 ]]; then
    STATE="BROKEN_INSTALL"; STATE_WHY="$CONFIG exists but the Noise key $NOISE_KEY is missing (a new key would need a new pairing)"; return
  fi
  if [[ "$F_CFG" == 1 && "$ROLE" == "ex" && "$F_PKI" == 0 ]]; then
    STATE="BROKEN_INSTALL"; STATE_WHY="$CONFIG exists but the certificate files in $BAFT_CONFIG_DIR/pki are missing"; return
  fi
  if [[ "$F_BIN" == 1 && "$F_PAIR" == 1 && "$F_UNIT" == 1 && "$F_CFG_VALID" == 1 && "$F_KEY" == 1 ]]; then
    if [[ "$F_ACTIVE" == 0 || "$F_ENABLED" == 0 || "$F_KEY_OK" == 0 || "$F_USER" == 0 || "$F_DIRS" == 0 ]]; then
      STATE="REPAIR_REQUIRED"; STATE_WHY="installed and paired, but the service is not running or enabled, or a permission is off"
    else
      STATE="ALREADY_INSTALLED"; STATE_WHY="installed, paired and running"
    fi
    return
  fi
  STATE="PARTIAL_INSTALL"; STATE_WHY="some of the install is missing (an interrupted install, or a node still waiting for pairing)"
}

# plan_binaries compares the staged (verified) binaries with the installed ones.
# STAGED=1 once a release was downloaded and verified; before that (--plan) the
# comparison can only use the requested version.
STAGED=0; TARGET_REL=""
STAGE_BIN=""; STAGE_PAIR=""; STAGE_AGENT=""; STAGE_BCC=""
DO_BIN=0
plan_binary() { # item installed-path staged-path flag
  local item="$1" path="$2" staged="$3" present="$4"
  if [[ "$STAGED" == 1 ]]; then
    if same_file "$staged" "$path"; then plan_add "$item" keep "identical to the verified release ${TARGET_REL}"
    elif [[ "$present" == 1 ]]; then plan_add "$item" update "replace with the verified release ${TARGET_REL} (the old one is kept for rollback)"; DO_BIN=1
    else plan_add "$item" create "install from the verified release ${TARGET_REL}"; DO_BIN=1; fi
  else
    if [[ "$present" == 1 ]]; then
      if [[ -n "$BAFT_VERSION" && -n "$INSTALLED_REL" && "$BAFT_VERSION" != "$INSTALLED_REL" ]]; then
        plan_add "$item" update "installed $INSTALLED_REL, requested $BAFT_VERSION (verified when downloaded)"
      else plan_add "$item" keep "installed${INSTALLED_REL:+ ($INSTALLED_REL)}; compared with the release when it is downloaded"; fi
    else plan_add "$item" create "not installed"; fi
  fi
}

DO_RELSTATE=0
plan_relstate() {
  [[ "$BAFT_INSTALL_FROM" == "release" ]] || return 0
  if [[ "$F_RELSTATE" == 0 ]]; then plan_add "release state" create "record the verified release"; DO_RELSTATE=1
  elif [[ "$STAGED" == 1 && -n "$TARGET_REL" && "$TARGET_REL" != "$INSTALLED_REL" ]]; then
    plan_add "release state" update "move forward to ${TARGET_REL} after the install verifies (never backwards, never reset)"; DO_RELSTATE=1
  else plan_add "release state" keep "recorded${INSTALLED_REL:+: $INSTALLED_REL}; only moves forward, never reset"; fi
}
DO_UNIT=0; DO_PKI=0; DO_KEYGEN=0; DO_PAIRING=0; DO_RESTART=0; DO_START=0; DO_ENABLE=0; DO_FIXPERM=0
DO_AGENT_UNIT=0; DO_AGENT_ENABLE=0; DO_TOKEN=0; DO_JOBKEY=0; DO_ROOTPIN=0
DO_BCC_UNIT=0; DO_BCC_ENABLE=0; DO_BCC_ADMIN=0; DO_BCC_ACCESS=0; DO_BCC_JOBKEY=0
REFUSED=""

build_role_plan() {
  plan_binary "baft binary" "$BAFT_BIN" "$STAGE_BIN" "$F_BIN"
  plan_binary "baft-pair binary" "$BAFT_PAIR_BIN" "$STAGE_PAIR" "$F_PAIR"
  plan_relstate
  if [[ "$STATE" == "BROKEN_INSTALL" ]]; then
    plan_add "install" refuse "$STATE_WHY"
    REFUSED="$STATE_WHY"
    return
  fi
  # Noise key: created only when absent, never regenerated.
  if [[ "$F_KEY" == 0 ]]; then plan_add "noise key" create "generate $NOISE_KEY (owner-only)"; DO_KEYGEN=1
  elif [[ "$F_KEY_OK" == 0 ]]; then plan_add "noise key" update "fix owner and mode only (never regenerated)"; DO_FIXPERM=1
  else plan_add "noise key" keep "exists; a rerun never regenerates it"; fi
  # Outer PKI (EX): created only when absent.
  if [[ "$ROLE" == "ex" ]]; then
    if [[ "$F_PKI" == 1 ]]; then plan_add "outer PKI" keep "CA and server certificate exist; never regenerated"
    else plan_add "outer PKI" create "generate the CA and server certificate for ${PUBLIC_ADDR:-this host}"; DO_PKI=1; fi
  fi
  # Config and pairing.
  # An existing config is never overwritten or re-paired on its own. Without a
  # usable installed binary it is validated with the verified release binary
  # once that is staged (a config that then fails refuses the install).
  local cfg_kept=0
  if [[ "$F_CFG_VALID" == 1 ]]; then cfg_kept=1
  elif [[ "$F_CFG" == 1 && "$STAGED" == 0 ]]; then cfg_kept=1; fi
  if [[ "$cfg_kept" == 1 && "$RE_PAIR" == 0 ]]; then
    if [[ "$F_CFG_VALID" == 1 ]]; then plan_add "config" keep "valid; it is not overwritten"
    else plan_add "config" keep "exists; validated with the verified release binary once it is downloaded, never overwritten"; fi
    plan_add "pairing" skip "already paired; --re-pair starts a new pairing"
  elif [[ "$cfg_kept" == 1 && "$RE_PAIR" == 1 ]]; then
    plan_add "config" replace "new pairing requested (--re-pair); the current config is backed up first"
    plan_add "pairing" create "issue a new one-time pairing code; the tunnel is interrupted until pairing completes"
    DO_PAIRING=1
  else
    plan_add "config" create "written when pairing completes"
    if [[ "$ROLE" == "ex" ]]; then plan_add "pairing" create "issue a one-time pairing code${REPLY_CODE:+ and finish with the given reply code}"
    else plan_add "pairing" create "pair with the EX using the pairing code"; fi
    DO_PAIRING=1
  fi
  # systemd unit: created when missing; an existing one is never overwritten.
  if [[ "$F_UNIT" == 0 ]]; then plan_add "systemd unit" create "$UNIT_FILE"; DO_UNIT=1
  elif [[ "$F_UNIT_MANAGED" == 1 ]]; then plan_add "systemd unit" keep "managed by the BCC tunnel builder (ownership marker); not touched"
  else plan_add "systemd unit" keep "exists; not overwritten"; fi
  [[ "$F_ENABLED" == 0 && "$F_UNIT" == 1 ]] && { plan_add "service enable" update "enable $BAFT_SERVICE.service"; DO_ENABLE=1; }
  # Service.
  if [[ "$DO_PAIRING" == 1 ]]; then plan_add "service" restart "started when pairing completes"
  elif [[ "$DO_BIN" == 1 || "$DO_UNIT" == 1 ]]; then plan_add "service" restart "to run the new binary or unit"; DO_RESTART=1
  elif [[ "$F_ACTIVE" == 0 && "$F_CFG_VALID" == 1 ]]; then plan_add "service" start "installed and paired but not running"; DO_START=1
  else plan_add "service" keep "left running; no restart needed"; fi
  if [[ "$STATE" != "FRESH_INSTALL" && "$PLAN_CHANGES" == 0 ]]; then
    plan_add "credentials" keep "none created, rotated or removed"
  fi
}

build_agent_plan() {
  plan_binary "baft binary" "$BAFT_BIN" "$STAGE_BIN" "$F_BIN"
  plan_binary "baft-pair binary" "$BAFT_PAIR_BIN" "$STAGE_PAIR" "$F_PAIR"
  plan_binary "baft-agent binary" "$BAFT_AGENT_BIN" "$STAGE_AGENT" "$F_AGENT_BIN"
  plan_relstate
  # The given token / job key / root pin are compared with what is installed.
  if [[ "$F_TOKEN" == 0 ]]; then plan_add "agent token" create "$BAFT_AGENT_DIR/token"; DO_TOKEN=1
  elif same_file "$AGENT_TOKEN_TMP" "$BAFT_AGENT_DIR/token"; then plan_add "agent token" keep "same token as installed"
  else plan_add "agent token" replace "a DIFFERENT token was given; the installed one is replaced"; DO_TOKEN=1; fi
  if [[ "$F_JOBKEY" == 0 ]]; then plan_add "BCC job key" create "$BAFT_AGENT_DIR/bcc-job.pub"; DO_JOBKEY=1
  elif [[ "$(cat "$BAFT_AGENT_DIR/bcc-job.pub" 2>/dev/null)" == "$BAFT_BCC_JOB_KEY" ]]; then plan_add "BCC job key" keep "same pinned key as installed"
  else plan_add "BCC job key" replace "a DIFFERENT job key was given; the pinned one is replaced"; DO_JOBKEY=1; fi
  if [[ -n "$BAFT_ROOT_PUB" ]]; then
    if [[ "$(cat "$BAFT_AGENT_DIR/release-root.pub" 2>/dev/null)" == "$BAFT_ROOT_PUB" ]]; then plan_add "release root pin" keep "same as installed"
    elif [[ -f "$BAFT_AGENT_DIR/release-root.pub" ]]; then plan_add "release root pin" replace "a DIFFERENT release root key was given"; DO_ROOTPIN=1
    else plan_add "release root pin" create "$BAFT_AGENT_DIR/release-root.pub"; DO_ROOTPIN=1; fi
  fi
  render_agent_unit >"$AGENT_UNIT_TMP"
  if [[ "$F_AGENT_UNIT" == 0 ]]; then plan_add "agent unit" create "$AGENT_UNIT_FILE"; DO_AGENT_UNIT=1
  elif same_file "$AGENT_UNIT_TMP" "$AGENT_UNIT_FILE"; then plan_add "agent unit" keep "identical"
  else plan_add "agent unit" update "arguments differ from the installed unit (BCC URL, node id, paths or interval)"; DO_AGENT_UNIT=1; fi
  if [[ "$F_AGENT_UNIT" == 1 && "$F_AGENT_ENABLED" == 0 ]]; then plan_add "agent service enable" update "enable $BAFT_AGENT_UNIT.service"; DO_AGENT_ENABLE=1; fi
  if [[ "$DO_BIN" == 1 || "$DO_TOKEN" == 1 || "$DO_JOBKEY" == 1 || "$DO_ROOTPIN" == 1 || "$DO_AGENT_UNIT" == 1 ]]; then
    plan_add "agent service" restart "to pick up the change"; DO_RESTART=1
  elif [[ "$F_AGENT_ACTIVE" == 0 ]]; then plan_add "agent service" start "installed but not running"; DO_START=1
  else plan_add "agent service" keep "left running; no restart needed"; fi
}

build_bcc_plan() {
  plan_binary "BCC binary" "$BAFT_BCC_BIN" "$STAGE_BCC" "$F_BCC_BIN"
  plan_relstate
  if [[ "$STATE" == "BROKEN_INSTALL" ]]; then
    plan_add "install" refuse "$STATE_WHY"
    REFUSED="$STATE_WHY"
    return
  fi

  if [[ "$F_BCC_ADMIN" == 0 ]]; then plan_add "BCC admin token" create "$BAFT_BCC_ADMIN_TOKEN_FILE (root-only; never printed)"; DO_BCC_ADMIN=1
  else plan_add "BCC admin token" keep "exists; rerun never rotates it"; fi

  if [[ "$F_BCC_ACCESS" == 0 ]]; then plan_add "BCC access" create "$BAFT_BCC_ACCESS_FILE; username/password/path shown once"; DO_BCC_ACCESS=1
  else plan_add "BCC access" keep "exists; rerun never rotates it"; fi

  if [[ "$F_BCC_JOBKEY" == 0 ]]; then plan_add "BCC job key" create "$BAFT_BCC_JOB_KEY_FILE (owner-only)"; DO_BCC_JOBKEY=1
  else plan_add "BCC job key" keep "exists; rerun never rotates it"; fi

  if [[ "$F_BCC_UNIT" == 0 ]]; then
    plan_add "BCC systemd unit" create "$BAFT_BCC_UNIT_FILE"; DO_BCC_UNIT=1
  elif [[ "$F_BCC_UNIT_MANAGED" == 0 ]]; then
    plan_add "BCC systemd unit" refuse "$BAFT_BCC_UNIT_FILE exists but is not BAFT-managed"
    REFUSED="$BAFT_BCC_UNIT_FILE exists but is not BAFT-managed"
    return
  elif cmp -s "$BAFT_BCC_UNIT_FILE" <(render_bcc_unit); then
    plan_add "BCC systemd unit" keep "identical"
  else
    plan_add "BCC systemd unit" update "managed unit arguments differ"; DO_BCC_UNIT=1
  fi

  if [[ "$F_BCC_ENABLED" == 0 ]]; then plan_add "BCC service enable" update "enable $BAFT_BCC_SERVICE.service"; DO_BCC_ENABLE=1; fi

  if [[ "$F_BCC_ACTIVE" == 0 ]]; then
    plan_add "BCC service" start "start the Command Center after its files are ready"; DO_START=1
  elif [[ "$DO_BIN" == 1 || "$DO_BCC_UNIT" == 1 || "$DO_BCC_ADMIN" == 1 || "$DO_BCC_JOBKEY" == 1 ]]; then
    plan_add "BCC service" restart "to pick up the verified binary, managed unit or startup credential"; DO_RESTART=1
  else
    plan_add "BCC service" keep "left running; credentials are not rotated"
  fi
}

show_plan() {
  if [[ "$PLAN_JSON" == "1" ]]; then
    local i sep=""
    printf '{"state":"%s","why":"%s","changes":%d,"steps":[' "$STATE" "$(json_escape "$STATE_WHY")" "$PLAN_CHANGES"
    for i in "${!PLAN_ITEM[@]}"; do
      printf '%s{"item":"%s","action":"%s","detail":"%s"}' "$sep" "$(json_escape "${PLAN_ITEM[$i]}")" "${PLAN_ACT[$i]}" "$(json_escape "${PLAN_DETAIL[$i]}")"
      sep=","
    done
    printf ']}\n'
    return
  fi
  local i
  log "install state: $STATE ($STATE_WHY)"
  log "plan:"
  for i in "${!PLAN_ITEM[@]}"; do
    printf '[baft-install]   %-20s %-8s %s\n' "${PLAN_ITEM[$i]}" "${PLAN_ACT[$i]}" "${PLAN_DETAIL[$i]}" >&2
  done
  if [[ "$PLAN_CHANGES" == 0 ]]; then log "nothing to change"; fi
}

# A plan that changes an existing install needs a yes; a fresh install never asks.
confirm_plan() {
  [[ -n "$REFUSED" ]] && return 0
  [[ "$STATE" == "FRESH_INSTALL" || "$PLAN_CHANGES" == 0 ]] && return 0
  [[ "$ASSUME_YES" == "1" ]] && return 0
  if [[ "$ASK_TTY" == 1 ]]; then
    local a=""
    ask a "[baft-install] Apply this plan? [y/N] " || true
    [[ "$a" == "y" || "$a" == "Y" || "$a" == "yes" ]] || die "not applied; nothing was changed"
    return 0
  fi
  log "this plan changes an existing install; re-run with --yes to apply it (or --plan to review it)"
  exit 4
}

# ---- apply with rollback ----
BACKUP_DIR=""; BACKUP_MAP=(); CREATED=(); BACKED_UP=(); WAS_ACTIVE=0; WAS_AGENT_ACTIVE=0; WAS_BCC_ACTIVE=0; TOUCHED_SERVICE=0
backup_name() { printf '%s' "$1" | tr '/' '_'; }
backup_file() {
  local f="$1"
  if [[ ! -e "$f" ]]; then CREATED+=("$f"); return 0; fi
  local seen
  for seen in "${BACKED_UP[@]}"; do
    [[ "$seen" == "$f" ]] && return 0
  done
  if [[ -z "$BACKUP_DIR" ]]; then
    BACKUP_DIR="$BAFT_PREFIX/backups/rerun-$(date +%Y%m%dT%H%M%S)-$$"
    install -d -m 0700 -o root -g root "$BAFT_PREFIX/backups" "$BACKUP_DIR"
  fi
  cp -p -- "$f" "$BACKUP_DIR/$(backup_name "$f")"
  # The manifest is the proof of what this run wrote here: an uninstall
  # removes only files it lists with a matching digest.
  ( cd "$BACKUP_DIR" && sha256sum -- "$(backup_name "$f")" >>MANIFEST.sha256 )
  BACKUP_MAP+=("$f")
  BACKED_UP+=("$f")
}
prune_backups() {
  local d n=0
  [[ -d "$BAFT_PREFIX/backups" ]] || return 0
  # Keep the newest three runs.
  while IFS= read -r d; do
    n=$((n + 1))
    if (( n > 3 )); then rm -rf -- "$d"; fi
  done < <(ls -1dt "$BAFT_PREFIX"/backups/rerun-* 2>/dev/null)
}
ENABLED_BY_RUN=0; WAS_ENABLED=0; WAS_AGENT_ENABLED=0; WAS_BCC_ENABLED=0
fail_point() { # test hook: BAFT_TEST_FAIL_AT=<point> makes the run fail there
  if [[ "${BAFT_TEST_FAIL_AT:-}" == "$1" ]]; then die "injected failure at $1 (test hook)"; fi
  return 0
}
# Rollback puts back the files AND the service state: a unit this run enabled
# is disabled again, a service that was not running before is stopped, and a
# service that was running is restarted on the old files. All of that happens
# in an order that works while the unit file still exists.
rollback_apply() {
  APPLYING=0
  log "apply failed: rolling back"
  local f unit was_active
  if [[ "$BCC_ONLY" == "1" ]]; then unit="$BAFT_BCC_SERVICE.service"; was_active="$WAS_BCC_ACTIVE"
  elif [[ "$AGENT_ONLY" == "1" ]]; then unit="$BAFT_AGENT_UNIT.service"; was_active="$WAS_AGENT_ACTIVE"
  else unit="$BAFT_SERVICE.service"; was_active="$WAS_ACTIVE"; fi
  if [[ "$TOUCHED_SERVICE" == 1 ]]; then systemctl stop "$unit" || true; fi
  if [[ "$ENABLED_BY_RUN" == 1 ]]; then systemctl disable "$unit" || true; fi
  for f in "${BACKUP_MAP[@]}"; do
    local tmp
    tmp="$(dirname "$f")/.baft-rollback-$(basename "$f").$$"
    if cp -p -- "$BACKUP_DIR/$(backup_name "$f")" "$tmp" && mv -f -- "$tmp" "$f"; then
      :
    else
      rm -f -- "$tmp"
      log "could not restore $f (copy kept in $BACKUP_DIR)"
    fi
  done
  for f in "${CREATED[@]}"; do rm -f -- "$f"; done
  systemctl daemon-reload || true
  # A service this run never stopped or restarted is left alone.
  if [[ "$TOUCHED_SERVICE" == 1 && "$was_active" == 1 ]]; then systemctl restart "$unit" || true; fi
  log "rolled back to the previous binaries, files and service state; the release state was not recorded"
}

# verify_release DIR REVOCATIONS UPDATE(0|1) ARTIFACT... checks a downloaded
# release with the rules of internal/release (cmd/baft-release verify):
# root-signed certificate and revocation list (mandatory, unexpired, never an
# older sequence), release-key-signed manifest, SHA256SUMS, artifact hashes,
# and no downgrade or re-tag against BAFT_RELEASE_STATE. It runs before any
# downloaded code, so it uses only python3 and the openssl CLI.
verify_release() {
  local dir="$1" rev="$2" update="$3"; shift 3
  python3 - "$dir" "$BAFT_ROOT_PUB" "$rev" "$BAFT_RELEASE_STATE" "$BAFT_ALLOW_DOWNGRADE" "$update" "$@" <<'PY'
# Verifies a downloaded BAFT release against the pinned root key, following
# internal/release (VerifyDir) rules, with the same trust-state file format.
# Ed25519 checks use the openssl CLI (OpenSSL 3), so no Go toolchain or BAFT
# binary is trusted before verification. Usage:
#   verify DIR ROOT_PUB REVOCATIONS STATE ALLOW_DOWNGRADE(0|1) UPDATE(0|1) ARTIFACT...
import base64, datetime, hashlib, json, os, re, subprocess, sys, tempfile

MAX_VALIDITY = datetime.timedelta(days=400)
META = {"manifest.json", "release-key.cert.json", "SHA256SUMS"}
VERSION_RE = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$")
COMMIT_RE = re.compile(r"^[0-9a-f]{40}$")
KEYID_RE = re.compile(r"^[0-9a-f]{32}$")
HEX64_RE = re.compile(r"^[0-9a-f]{64}$")
SPKI_PREFIX = bytes.fromhex("302a300506032b6570032100")


class Reject(Exception):
    pass


def b64url(s):
    s = s.strip()
    return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))


def b64std(s):
    return base64.b64decode(s, validate=True)


def key_id(pub):
    return hashlib.sha256(pub).hexdigest()[:32]


def strict(obj, keys, optional=(), what="document"):
    if not isinstance(obj, dict):
        raise Reject(f"{what}: not a JSON object")
    extra = set(obj) - set(keys) - set(optional)
    missing = set(keys) - set(obj)
    if extra or missing:
        raise Reject(f"{what}: unexpected fields {sorted(extra)} / missing {sorted(missing)}")
    return obj


def load_json(data, what):
    try:
        return json.loads(data)
    except (ValueError, UnicodeDecodeError) as e:
        raise Reject(f"{what}: {e}")


def ts(s, what):
    if not isinstance(s, str):
        raise Reject(f"{what}: not a timestamp")
    try:
        t = datetime.datetime.fromisoformat(s.replace("Z", "+00:00"))
    except ValueError:
        raise Reject(f"{what}: malformed timestamp {s!r}")
    if t.tzinfo is None:
        raise Reject(f"{what}: timestamp without zone")
    return t


def ed25519_ok(pub, msg, sig):
    with tempfile.TemporaryDirectory() as d:
        paths = {n: os.path.join(d, n) for n in ("pub.der", "msg", "sig")}
        for name, data in (("pub.der", SPKI_PREFIX + pub), ("msg", msg), ("sig", sig)):
            with open(paths[name], "wb") as f:
                f.write(data)
        r = subprocess.run(
            ["openssl", "pkeyutl", "-verify", "-pubin", "-keyform", "DER", "-inkey", paths["pub.der"],
             "-rawin", "-in", paths["msg"], "-sigfile", paths["sig"]],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return r.returncode == 0


def open_envelope(raw, payload_type, pub, what):
    env = strict(load_json(raw, what), ("payloadType", "payload", "signatures"), what=what)
    if env["payloadType"] != payload_type:
        raise Reject(f"{what}: payload type {env['payloadType']!r}")
    sigs = env["signatures"]
    if not isinstance(sigs, list) or len(sigs) != 1:
        raise Reject(f"{what}: want exactly one signature")
    s = strict(sigs[0], ("keyid", "sig"), what=what)
    if s["keyid"] != key_id(pub):
        raise Reject(f"{what}: signed by key {s['keyid']}, want {key_id(pub)}")
    try:
        payload, sig = b64std(env["payload"]), b64std(s["sig"])
    except (ValueError, TypeError):
        raise Reject(f"{what}: bad base64")
    t = payload_type.encode()
    pae = b"DSSEv1 %d %s %d %s" % (len(t), t, len(payload), payload)
    if not ed25519_ok(pub, pae, sig):
        raise Reject(f"{what}: signature does not verify")
    return load_json(payload, what)


def parse_version(v):
    if not isinstance(v, str) or not VERSION_RE.match(v):
        raise Reject(f"version {v!r} is not vMAJOR.MINOR.PATCH[-pre]")
    core, _, pre = v[1:].partition("-")
    return [int(x) for x in core.split(".")], (pre.split(".") if pre else [])


def compare_versions(a, b):
    (ca, pa), (cb, pb) = parse_version(a), parse_version(b)
    if ca != cb:
        return -1 if ca < cb else 1
    if not pa or not pb:
        return (len(pa) == 0) - (len(pb) == 0)
    for x, y in zip(pa, pb):
        if x == y:
            continue
        xn, yn = x.isdigit(), y.isdigit()
        if xn and yn:
            return -1 if int(x) < int(y) else 1
        if xn != yn:
            return -1 if xn else 1
        return -1 if x < y else 1
    return (len(pa) > len(pb)) - (len(pa) < len(pb))


def sha256_file(path):
    h, n = hashlib.sha256(), 0
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
            n += len(chunk)
    return n, h.hexdigest()


def read_state(path):
    if not os.path.exists(path):
        return {"schema_version": 1, "revocation_sequence": 0}
    with open(path, "rb") as f:
        st = strict(load_json(f.read(), "trust state"), ("schema_version", "revocation_sequence", "updated_at"),
                    optional=("version", "commit"), what="trust state")
    if st["schema_version"] != 1:
        raise Reject("trust state: unsupported schema")
    if st.get("version"):
        parse_version(st["version"])
    return st


def write_state(path, st):
    d = os.path.dirname(path) or "."
    fd, tmp = tempfile.mkstemp(dir=d, prefix=".trust-state-")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(st, f, indent=2)
            f.write("\n")
            f.flush()
            os.fsync(f.fileno())
        os.chmod(tmp, 0o644)
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def window_ok(nb, na):
    return na > nb and na - nb <= MAX_VALIDITY


def verify(dir_, root_b64, rev_path, state_path, allow_downgrade, update, wanted):
    now = datetime.datetime.now(datetime.timezone.utc)
    root = b64url(root_b64)
    if len(root) != 32:
        raise Reject("pinned root key is not an Ed25519 public key")
    state = read_state(state_path)

    with open(rev_path, "rb") as f:
        rev = strict(open_envelope(f.read(), "application/vnd.baft.release-revocations+json", root, "revocation list"),
                     ("schema_version", "sequence", "issued_at", "expires_at", "revoked_key_ids"), what="revocation list")
    if rev["schema_version"] != 2 or type(rev["sequence"]) is not int or rev["sequence"] < 1:
        raise Reject("revocation list: unsupported schema or sequence")
    issued, expires = ts(rev["issued_at"], "revocation list"), ts(rev["expires_at"], "revocation list")
    if not window_ok(issued, expires):
        raise Reject("revocation list: invalid validity window")
    revoked = rev["revoked_key_ids"] or []
    if not isinstance(revoked, list) or not all(isinstance(i, str) and KEYID_RE.match(i) for i in revoked):
        raise Reject("revocation list: malformed key id")
    if now >= expires:
        raise Reject(f"revocation list: expired at {rev['expires_at']}; the root holder must re-sign it")
    if rev["sequence"] < state["revocation_sequence"]:
        raise Reject(f"revocation list: sequence {rev['sequence']} is older than {state['revocation_sequence']} already accepted")

    with open(os.path.join(dir_, "release-key.cert.json"), "rb") as f:
        cert = strict(open_envelope(f.read(), "application/vnd.baft.release-key-cert+json", root, "release key certificate"),
                      ("schema_version", "purpose", "key_id", "public_key", "not_before", "not_after"),
                      what="release key certificate")
    if cert["schema_version"] != 1 or cert["purpose"] != "baft-release":
        raise Reject("release key certificate: unsupported schema or purpose")
    rel_pub = b64url(cert["public_key"])
    if len(rel_pub) != 32 or key_id(rel_pub) != cert["key_id"]:
        raise Reject("release key certificate: key_id does not match public_key")
    nb, na = ts(cert["not_before"], "certificate"), ts(cert["not_after"], "certificate")
    if not window_ok(nb, na):
        raise Reject("release key certificate: invalid validity window")
    if cert["key_id"] in revoked:
        raise Reject(f"release key {cert['key_id']} is revoked")

    with open(os.path.join(dir_, "manifest.json"), "rb") as f:
        m = strict(open_envelope(f.read(), "application/vnd.baft.release-manifest+json", rel_pub, "manifest"),
                   ("schema_version", "product", "version", "commit", "created_at", "signing_key_id",
                    "sha256sums_sha256", "artifacts", "provenance"), what="manifest")
    if m["schema_version"] != 1 or m["product"] != "baft":
        raise Reject("manifest: unsupported schema or product")
    parse_version(m["version"])
    if not isinstance(m["commit"], str) or not COMMIT_RE.match(m["commit"]):
        raise Reject("manifest: malformed commit")
    if m["signing_key_id"] != cert["key_id"]:
        raise Reject("manifest: signing_key_id does not match certificate")
    created = ts(m["created_at"], "manifest")
    if created < nb or created > na:
        raise Reject("manifest: created_at is outside the release key's validity")

    with open(os.path.join(dir_, "SHA256SUMS"), "rb") as f:
        sums = f.read()
    if hashlib.sha256(sums).hexdigest() != m["sha256sums_sha256"]:
        raise Reject("SHA256SUMS does not match the manifest")
    listed = {}
    for line in sums.decode().splitlines():
        h, sep, name = line.partition("  ")
        if not sep or not HEX64_RE.match(h) or not name or name in listed:
            raise Reject(f"malformed SHA256SUMS line {line!r}")
        listed[name] = h
    arts = {}
    for a in m["artifacts"] or []:
        a = strict(a, ("name", "os", "arch", "size", "sha256"), what="manifest artifact")
        if a["name"] in arts or listed.get(a["name"]) != a["sha256"]:
            raise Reject(f"{a['name']}: SHA256SUMS and manifest disagree")
        arts[a["name"]] = a
    if len(arts) != len(listed):
        raise Reject("SHA256SUMS and manifest list different artifacts")

    # The installer downloads only what it installs: every file present must
    # be a signed artifact, and every wanted artifact must be present.
    for name in os.listdir(dir_):
        if name in META:
            continue
        path = os.path.join(dir_, name)
        a = arts.get(name)
        if a is None:
            raise Reject(f"{name}: not part of the signed release")
        if not os.path.isfile(path) or os.path.islink(path):
            raise Reject(f"{name}: not a regular file")
        if sha256_file(path) != (a["size"], a["sha256"]):
            raise Reject(f"{name}: hash or size does not match the signed manifest")
    for name in wanted:
        if name not in arts or not os.path.isfile(os.path.join(dir_, name)):
            raise Reject(f"{name}: not in the signed release")

    if state.get("version"):
        c = compare_versions(m["version"], state["version"])
        if c == 0 and m["commit"] != state.get("commit"):
            raise Reject(f"release {m['version']} is commit {m['commit']}, but {state['version']} was already accepted as commit {state.get('commit')}")
        if c < 0 and not allow_downgrade:
            raise Reject(f"release {m['version']} is older than the accepted {state['version']} (downgrade refused)")

    if update:
        write_state(state_path, {
            "schema_version": 1, "version": m["version"], "commit": m["commit"],
            "revocation_sequence": max(rev["sequence"], state["revocation_sequence"]),
            "updated_at": now.replace(microsecond=0).strftime("%Y-%m-%dT%H:%M:%SZ"),
        })
    print(m["version"], m["commit"])


if __name__ == "__main__":
    try:
        a = sys.argv[1:]
        verify(a[0], a[1], a[2], a[3], a[4] == "1", a[5] == "1", a[6:])
    except Reject as e:
        print(f"release verification failed: {e}", file=sys.stderr)
        sys.exit(1)
    except (OSError, KeyError, TypeError, IndexError) as e:
        print(f"release verification failed: {type(e).__name__}: {e}", file=sys.stderr)
        sys.exit(1)
PY
}

# --verify-release DIR: run only the verifier (used by tests). Artifacts to
# require come from BAFT_VERIFY_ARTIFACTS; BAFT_UPDATE_STATE=1 records them.
if [[ -n "$VERIFY_ONLY_DIR" ]]; then
  IFS=' ' read -r -a _wanted <<<"${BAFT_VERIFY_ARTIFACTS:-}"
  verify_release "$VERIFY_ONLY_DIR" "${BAFT_REVOCATIONS_FILE:?BAFT_REVOCATIONS_FILE is required}" "${BAFT_UPDATE_STATE:-0}" "${_wanted[@]}"
  exit
fi

bcc_listen_host() {
  local host
  if [[ "$BAFT_BCC_LISTEN" == \[*\]:* ]]; then
    host="${BAFT_BCC_LISTEN#\[}"
    host="${host%%\]:*}"
  else
    host="${BAFT_BCC_LISTEN%:*}"
  fi
  printf '%s' "$host"
}

bcc_listen_is_loopback() {
  local host
  host="$(bcc_listen_host)"
  case "$host" in
    localhost|127.*|::1) return 0 ;;
  esac
  return 1
}

validate_bcc_settings() {
  [[ "$BAFT_BCC_LISTEN" =~ ^(\[[0-9A-Fa-f:]+\]|[A-Za-z0-9._-]+):([0-9]{1,5})$ ]] || die "--bcc-listen must be HOST:PORT"
  local port="${BASH_REMATCH[2]}" host
  host="$(bcc_listen_host)"
  (( port >= 1 && port <= 65535 )) || die "--bcc-listen port must be 1..65535"
  if [[ "$host" != "localhost" ]]; then
    if [[ "$host" == *:* ]]; then
      [[ "$host" =~ ^[0-9A-Fa-f:]+$ ]] || die "--bcc-listen host must be localhost or an IP literal"
      command -v python3 >/dev/null 2>&1 || die "python3 is required to validate an IPv6 --bcc-listen"
      python3 - "$host" <<'PY' >/dev/null 2>&1 || die "--bcc-listen host must be localhost or an IP literal"
import ipaddress, sys
ipaddress.ip_address(sys.argv[1])
PY
    else
      local a b c d extra
      IFS=. read -r a b c d extra <<<"$host"
      [[ -z "$extra" && "$a" =~ ^[0-9]+$ && "$b" =~ ^[0-9]+$ && "$c" =~ ^[0-9]+$ && "$d" =~ ^[0-9]+$ ]] ||
        die "--bcc-listen host must be localhost or an IP literal"
      (( 10#$a <= 255 && 10#$b <= 255 && 10#$c <= 255 && 10#$d <= 255 )) ||
        die "--bcc-listen host must be localhost or an IP literal"
    fi
  fi
  if [[ -n "$BAFT_BCC_TLS_CERT" || -n "$BAFT_BCC_TLS_KEY" ]]; then
    [[ -n "$BAFT_BCC_TLS_CERT" && -n "$BAFT_BCC_TLS_KEY" ]] || die "--bcc-tls-cert and --bcc-tls-key must be supplied together"
  fi
  if ! bcc_listen_is_loopback && [[ -z "$BAFT_BCC_TLS_CERT" && "$BAFT_BCC_ALLOW_INSECURE_HTTP" != "1" ]]; then
    die "non-loopback --bcc-listen requires TLS cert/key (or explicit --bcc-allow-insecure-http)"
  fi
  if [[ -n "$BAFT_BCC_PUBLIC_URL" ]]; then
    [[ "$BAFT_BCC_PUBLIC_URL" =~ ^https://[^[:space:]/]+/?$ || ( "$BAFT_BCC_ALLOW_INSECURE_HTTP" == "1" && "$BAFT_BCC_PUBLIC_URL" =~ ^http://[^[:space:]/]+/?$ ) ]] ||
      die "--bcc-public-url must be https://host[:port] (http only with --bcc-allow-insecure-http)"
  fi
  local v
  for v in "$BAFT_BCC_BIN" "$BAFT_BCC_CONFIG_DIR" "$BAFT_BCC_STATE_DIR" "$BAFT_BCC_STATE_FILE"     "$BAFT_BCC_ADMIN_TOKEN_FILE" "$BAFT_BCC_ACCESS_FILE" "$BAFT_BCC_JOB_KEY_FILE" "$BAFT_BCC_BACKUP_DIR" "$BAFT_BCC_UNIT_FILE"; do
    [[ "$v" != *[[:space:]]* ]] || die "BCC install paths must not contain whitespace"
  done
}

if [[ "$BCC_ONLY" == "1" ]]; then
  [[ "$AGENT_ONLY" == "0" ]] || die "--bcc-only cannot be combined with --agent-only"
  [[ -z "$ROLE" ]] || die "--bcc-only does not take --role"
  [[ "$RE_PAIR" == "0" && "$STEALTH_PRO" == "0" && -z "$IR_SSH" ]] || die "--bcc-only cannot be combined with tunnel/pairing options"
  validate_bcc_settings
elif [[ "$AGENT_ONLY" == "1" ]]; then
  [[ -z "$ROLE" ]] || die "--agent-only does not take --role; the tunnel is set up later from BCC"
  [[ "$BAFT_BCC_URL" == https://* || ( "$BAFT_BCC_URL" == http://* && "${BAFT_AGENT_ALLOW_HTTP:-0}" == "1" ) ]] || die "--bcc-url must be https://"
  [[ "$BAFT_NODE_ID" =~ ^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$ ]] || die "--node-id is missing or malformed"
  [[ -n "${BAFT_BCC_JOB_KEY:-}" ]] || die "BAFT_BCC_JOB_KEY (baft-bcc jobkey show) is required"
  [[ -n "${BAFT_AGENT_TOKEN_FILE:-}" || -n "${BAFT_AGENT_TOKEN:-}" ]] || die "BAFT_AGENT_TOKEN_FILE or BAFT_AGENT_TOKEN is required"
else
  # One install command for both servers: without --role, ask where this
  # server is and continue with that role's steps.
  if [[ -z "$ROLE" && -n "$ASK_FD" ]]; then
    printf '\nWhere is this server?  /  این سرور کجاست؟\n' >&2
    printf '  1) Outside Iran (EX, exit server) - install this one first  /  خارج از ایران\n' >&2
    printf '  2) Inside Iran (IR) - needs the pairing code printed by EX  /  داخل ایران\n' >&2
    while [[ -z "$ROLE" ]]; do
      _choice=""
      ask _choice "Choose 1 or 2: " || die "no answer; pass --role ex or --role ir"
      case "$_choice" in
        1|ex|EX) ROLE=ex ;;
        2|ir|IR) ROLE=ir ;;
        *) printf 'Please type 1 or 2.\n' >&2 ;;
      esac
    done
    if [[ "$ROLE" == "ex" && -z "$PUBLIC_ADDR" && "$PLAN_ONLY" != "1" ]]; then
      _detected="$(hostname -I 2>/dev/null | awk '{print $1}')"
      ask PUBLIC_ADDR "Public IP or hostname IR will connect to [${_detected:-none}]: " || true
      PUBLIC_ADDR="${PUBLIC_ADDR:-$_detected}"
    fi
  fi
  [[ -n "$ROLE" ]] || die "--role ex or --role ir is required when there is no terminal to ask"
  [[ "$ROLE" == "ex" || "$ROLE" == "ir" ]] || die "--role must be ex or ir"
  [[ -z "$IR_SSH" || "$ROLE" == "ex" ]] || die "--ir-ssh is for the EX installer"
fi

# ---- EX: install and pair the IR over SSH ----
# The IR is installed by this same script, sent over SSH with the pairing code
# in its environment (never on a command line), non-interactively; its reply
# code is read back from its output. The IR host key is pinned before any
# credential is sent: confirmed by the operator or given as a fingerprint.
IR_SSH_ENV_PASS="BAFT_INSTALL_FROM BAFT_VERSION BAFT_RELEASE_URL BAFT_GITHUB_REPO BAFT_REVOCATIONS_URL BAFT_ROOT_PUB BAFT_ALLOW_DOWNGRADE BAFT_REPO_URL BAFT_REF BAFT_MIRROR_URL BAFT_GO_VERSION BAFT_RUN_TESTS"
setup_ir_over_ssh() { # PAIRING_CODE -> prints the BAFTREPLY1 code on stdout
  local code=$1 user host port target
  target="$IR_SSH"
  [[ "$target" =~ ^(([A-Za-z0-9._-]+)@)?([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:([0-9]{1,5}))?$ ]] || die "--ir-ssh must be user@host[:port]"
  user="${BASH_REMATCH[2]:-root}"; host="${BASH_REMATCH[3]}"; port="${BASH_REMATCH[5]:-22}"
  host="${host#[}"; host="${host%]}"
  for c in ssh ssh-keyscan ssh-keygen; do
    command -v "$c" >/dev/null 2>&1 && continue
    log "installing openssh-client for the IR setup"
    apt-get update -qq >/dev/null && apt-get install -y -qq --no-install-recommends openssh-client >/dev/null || die "cannot install openssh-client"
    break
  done
  local dir; dir="$(mktemp -d)"; CLEANUP+=("$dir")
  log "reading the IR host key from $host:$port"
  ssh-keyscan -T 15 -p "$port" "$host" >"$dir/scan" 2>/dev/null || true
  [[ -s "$dir/scan" ]] || die "cannot reach SSH on $host:$port from this server (firewall?); set up the IR by hand and paste its reply code instead"
  local line fp ok=0 shown=""
  : >"$dir/known_hosts"
  while IFS= read -r line; do
    [[ -z "$line" || "$line" == \#* ]] && continue
    fp="$(printf '%s\n' "$line" | ssh-keygen -lf - -E sha256 2>/dev/null | awk '{print $2" "$NF}')" || continue
    [[ -n "$fp" ]] || continue
    shown+="    $fp"$'\n'
    if [[ -n "$IR_SSH_FINGERPRINT" ]]; then
      [[ "${fp%% *}" == "$IR_SSH_FINGERPRINT" ]] && { printf '%s\n' "$line" >>"$dir/known_hosts"; ok=1; }
    else
      printf '%s\n' "$line" >>"$dir/known_hosts"
    fi
  done <"$dir/scan"
  if [[ -n "$IR_SSH_FINGERPRINT" ]]; then
    [[ "$ok" == 1 ]] || die "the IR host key does not match --ir-ssh-fingerprint; it presented:"$'\n'"$shown"
  else
    printf '\nThe IR server presented these SSH host keys:\n%s' "$shown" >&2
    printf 'Compare them with the server (provider console: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub).\n' >&2
    local yes=""
    ask yes "Do they match? [y/N] " || die "the IR host key must be confirmed; pass --ir-ssh-fingerprint SHA256:... when there is no terminal"
    [[ "$yes" == "y" || "$yes" == "Y" || "$yes" == "yes" ]] || die "IR host key not confirmed; nothing was sent to it"
  fi

  local -a opts=(-p "$port" -o StrictHostKeyChecking=yes -o UserKnownHostsFile="$dir/known_hosts"
    -o GlobalKnownHostsFile=/dev/null -o ConnectTimeout=20 -o ServerAliveInterval=30 -o LogLevel=ERROR)
  local -a envs=()
  if [[ -n "$IR_SSH_KEY" ]]; then
    [[ -r "$IR_SSH_KEY" ]] || die "--ir-ssh-key $IR_SSH_KEY is not readable"
    opts+=(-i "$IR_SSH_KEY" -o IdentitiesOnly=yes -o BatchMode=yes)
  else
    local pass="${BAFT_IR_SSH_PASSWORD:-}"
    if [[ -z "$pass" ]]; then
      ask pass "SSH password for $user@$host: " -s || die "an IR SSH password or --ir-ssh-key is required"
      printf '\n' >&2
    fi
    [[ -n "$pass" ]] || die "no IR SSH password entered"
    # The password reaches ssh through SSH_ASKPASS from the environment: never
    # an argument, never a file.
    printf '#!/bin/sh\nprintf "%%s\\n" "$BAFT_IR_SSH_ASKPASS_SECRET"\n' >"$dir/askpass"
    chmod 0700 "$dir/askpass"
    envs=(SSH_ASKPASS="$dir/askpass" SSH_ASKPASS_REQUIRE=force DISPLAY=baft BAFT_IR_SSH_ASKPASS_SECRET="$pass")
    opts+=(-o PreferredAuthentications=password,keyboard-interactive -o PubkeyAuthentication=no -o NumberOfPasswordPrompts=1)
  fi

  local self="${BASH_SOURCE[0]:-}"
  if [[ -n "$self" && -r "$self" ]]; then
    cp -- "$self" "$dir/install.sh"
  else
    curl -fsSL --retry 3 "${BAFT_INSTALL_URL:-https://raw.githubusercontent.com/${BAFT_GITHUB_REPO}/${BAFT_REF}/install.sh}" -o "$dir/install.sh" || die "cannot download install.sh to send to the IR"
  fi
  grep -q '^BAFT_IR_INSTALL_SH_END$' "$dir/install.sh" && die "install.sh contains the transfer delimiter"
  local remote="bash -s"
  [[ "$user" == "root" ]] || remote="sudo -n bash -s"
  {
    printf 'set -e\numask 077\nf="$(mktemp)"\ntrap '"'"'rm -f "$f"'"'"' EXIT\n'
    printf "cat >\"\$f\" <<'BAFT_IR_INSTALL_SH_END'\n"
    cat "$dir/install.sh"
    printf 'BAFT_IR_INSTALL_SH_END\n'
    printf 'export BAFT_NONINTERACTIVE=1 BAFT_PAIRING_CODE=%q\n' "$code"
    local v
    local -a pass_vars=() ir_env=()
    IFS=' ' read -r -a pass_vars <<<"$IR_SSH_ENV_PASS"
    IFS=' ' read -r -a ir_env <<<"${BAFT_IR_ENV:-}"
    for v in "${pass_vars[@]}"; do
      [[ -n "${!v:-}" ]] && printf 'export %s=%q\n' "$v" "${!v}"
    done
    # Extra NAME=VALUE settings for the IR only (BAFT_* names).
    for v in "${ir_env[@]}"; do
      [[ "$v" =~ ^BAFT_[A-Z0-9_]+=.*$ ]] || die "BAFT_IR_ENV entries must be BAFT_NAME=value"
      printf 'export %s=%q\n' "${v%%=*}" "${v#*=}"
    done
    printf 'bash "$f" --role ir </dev/null\n'
  } >"$dir/payload"
  log "installing the IR on $host over SSH (this takes a few minutes)"
  local rc=0
  env "${envs[@]}" ssh "${opts[@]}" "$user@$host" "$remote" <"$dir/payload" >"$dir/ir.out" 2>"$dir/ir.err" || rc=$?
  sed -E 's/BAFT(PAIR|REPLY)1:[^[:space:]]*/BAFT\11:[hidden]/g; s/^/  [IR] /' "$dir/ir.err" | tail -n 25 >&2
  local reply
  reply="$(grep -o 'BAFTREPLY1:[^[:space:]]*' "$dir/ir.out" | tail -n 1 || true)"
  if [[ "$rc" != 0 || -z "$reply" ]]; then
    log "the IR setup over SSH did not finish (exit $rc)"
    return 1
  fi
  printf '%s\n' "$reply"
}

# ---- renderers (pure: they print, they never touch the system) ----
render_service_unit() {
  local caps=""
  if (( BAFT_PORT < 1024 )); then
    caps=$'AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE'
  fi
  cat <<EOF
[Unit]
Description=BAFT transport service
After=network-online.target
Wants=network-online.target
# The IR dialer exits while its EX is unreachable; keep retrying forever.
StartLimitIntervalSec=0

[Service]
Type=simple
User=$BAFT_USER
Group=$BAFT_USER
ExecStart=$BAFT_BIN run --file $CONFIG
ExecReload=/bin/kill -HUP \$MAINPID
Restart=on-failure
RestartSec=2s
$caps
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
ReadWritePaths=$BAFT_STATE_DIR
UMask=0027

[Install]
WantedBy=multi-user.target
EOF
}

render_bcc_unit() {
  local tls_args="" insecure="" caps="" allow_line="" port host
  port="${BAFT_BCC_LISTEN##*:}"
  if [[ "$BAFT_BCC_LISTEN" == \[*\]:* ]]; then
    host="${BAFT_BCC_LISTEN#\[}"
    host="${host%%\]:*}"
  else
    host="${BAFT_BCC_LISTEN%:*}"
  fi
  case "$host" in
    localhost|127.*|::1) ;;
    *) allow_line="Environment=BAFT_BCC_ALLOWED_LISTEN_IPS=$host" ;;
  esac
  if [[ -n "$BAFT_BCC_TLS_CERT" ]]; then
    tls_args=" --tls-cert $BAFT_BCC_TLS_CERT --tls-key $BAFT_BCC_TLS_KEY"
  fi
  if [[ "$BAFT_BCC_ALLOW_INSECURE_HTTP" == "1" ]]; then insecure=" --allow-insecure-http"; fi
  if (( port < 1024 )); then
    caps=$'AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE'
  else
    caps='CapabilityBoundingSet='
  fi
  cat <<UNIT
# baft-managed: true
# baft-component: bcc
[Unit]
Description=BAFT Command Center
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
$allow_line
ExecStart=$BAFT_BCC_BIN --listen $BAFT_BCC_LISTEN --state-file $BAFT_BCC_STATE_FILE --admin-token-file $BAFT_BCC_ADMIN_TOKEN_FILE --access-file $BAFT_BCC_ACCESS_FILE --job-key-file $BAFT_BCC_JOB_KEY_FILE --backup-dir $BAFT_BCC_BACKUP_DIR$tls_args$insecure
Restart=on-failure
RestartSec=5s
$caps
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
ReadWritePaths=$BAFT_BCC_STATE_DIR
UMask=0077

[Install]
WantedBy=multi-user.target
UNIT
}

render_agent_unit() {
  local root_flag="" http_flag="" bindir
  if [[ -n "$BAFT_ROOT_PUB" ]]; then root_flag="--release-root $BAFT_AGENT_DIR/release-root.pub"; fi
  if [[ "$BAFT_BCC_URL" == http://* ]]; then http_flag="--allow-insecure-http"; fi
  # Where the agent fetches releases for update jobs (defaults: GitHub).
  if [[ -n "${BAFT_AGENT_RELEASE_BASE_URL:-}" ]]; then http_flag+=" --release-base-url $BAFT_AGENT_RELEASE_BASE_URL"; fi
  if [[ -n "${BAFT_AGENT_REVOCATIONS_URL:-}" ]]; then http_flag+=" --revocations-url $BAFT_AGENT_REVOCATIONS_URL"; fi
  bindir="$(dirname "$BAFT_BIN")"
  cat <<UNIT
[Unit]
Description=BAFT agent (signed BCC jobs)
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
ExecStart=$BAFT_AGENT_BIN --bcc-url $BAFT_BCC_URL --node-id $BAFT_NODE_ID --token-file $BAFT_AGENT_DIR/token --bcc-job-key $BAFT_AGENT_DIR/bcc-job.pub $root_flag --state-dir $BAFT_AGENT_STATE_DIR --release-state $BAFT_RELEASE_STATE --bin-dir $bindir --service $BAFT_SERVICE --config-dir $BAFT_CONFIG_DIR --baft-state-dir $BAFT_STATE_DIR --service-user $BAFT_USER --metrics-listen $BAFT_METRICS_LISTEN --interval $BAFT_AGENT_INTERVAL $http_flag
Restart=always
RestartSec=10s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
# Tunnel jobs write the BAFT config, keys and unit.
ReadWritePaths=$bindir $BAFT_PREFIX $BAFT_AGENT_STATE_DIR $BAFT_CONFIG_DIR $BAFT_STATE_DIR $BAFT_SYSTEMD_DIR
UMask=0077

[Install]
WantedBy=multi-user.target
UNIT
}

# The agent inputs are written to private temp files so the plan can compare
# them with what is installed (no secret is printed).
AGENT_TOKEN_TMP=""; AGENT_UNIT_TMP=""
prepare_agent_inputs() {
  AGENT_TOKEN_TMP="$(mktemp)"; AGENT_UNIT_TMP="$(mktemp)"
  CLEANUP+=("$AGENT_TOKEN_TMP" "$AGENT_UNIT_TMP")
  if [[ -n "${BAFT_AGENT_TOKEN_FILE:-}" ]]; then
    [[ -r "$BAFT_AGENT_TOKEN_FILE" ]] || die "cannot read BAFT_AGENT_TOKEN_FILE"
    cat -- "$BAFT_AGENT_TOKEN_FILE" >"$AGENT_TOKEN_TMP"
  else
    printf '%s\n' "$BAFT_AGENT_TOKEN" >"$AGENT_TOKEN_TMP"
  fi
}

plan_base() {
  if [[ "$F_USER" == 0 ]]; then plan_add "service user" create "system user $BAFT_USER"; else plan_add "service user" keep "$BAFT_USER exists"; fi
  if [[ "$F_DIRS" == 0 ]]; then plan_add "directories" create "missing ones of $BAFT_PREFIX $BAFT_CONFIG_DIR $BAFT_STATE_DIR (existing ones are not touched)"
  else plan_add "directories" keep "exist; owner and mode are not changed"; fi
}

plan_bcc_base() {
  if [[ "$F_BCC_DIRS" == 0 ]]; then
    plan_add "BCC directories" create "$BAFT_BCC_CONFIG_DIR and $BAFT_BCC_STATE_DIR"
  else
    plan_add "BCC directories" keep "exist; credentials are not regenerated"
  fi
}

plan_everything() {
  [[ "$INSPECTED" == 1 ]] || inspect_install
  classify_state
  [[ "$AGENT_ONLY" == "1" ]] && prepare_agent_inputs
  if [[ "$BCC_ONLY" == "1" ]]; then
    plan_bcc_base
    build_bcc_plan
  else
    plan_base
    if [[ "$AGENT_ONLY" == "1" ]]; then build_agent_plan; else build_role_plan; fi
  fi
  refine_state
}

refine_state() {
  [[ "$STATE" == "ALREADY_INSTALLED" ]] || return 0
  if [[ "$STAGED" == 1 ]]; then
    if [[ "$DO_BIN" == 1 ]]; then STATE="UPGRADE_AVAILABLE"; STATE_WHY="installed and running; the verified release ${TARGET_REL} differs from the installed binaries"
    else STATE="CURRENT"; STATE_WHY="installed, running and identical to the verified release ${TARGET_REL}"; fi
  elif [[ -n "$BAFT_VERSION" && -n "$INSTALLED_REL" && "$BAFT_VERSION" != "$INSTALLED_REL" ]]; then
    STATE="UPGRADE_AVAILABLE"; STATE_WHY="installed $INSTALLED_REL; $BAFT_VERSION was requested"
  fi
}

# --plan: inspect and show what a run would do. No root, no network, no apt,
# nothing is written, nothing is downloaded.
if [[ "$PLAN_ONLY" == "1" ]]; then
  plan_everything
  show_plan
  exit 0
fi

show_logo
[[ "$BAFT_INSTALL_FROM" == "release" || "$BAFT_INSTALL_FROM" == "source" ]] || die "BAFT_INSTALL_FROM must be release or source"
if [[ "$BAFT_INSTALL_FROM" == "release" && -z "$BAFT_ROOT_PUB" ]]; then
  die "no release root key is pinned in this installer yet; until the first signed release, use --from-source"
fi
need_root
if [[ "$STEALTH_PRO" == "1" ]]; then
  [[ -r "$BAFT_CONFIG_DIR/baft.yaml" ]] || die "Stealth Pro requires an existing pinned Noise baft.yaml; see docs/en/21-stealth-pro.md"
fi
[[ -r /etc/os-release ]] || die "unsupported OS"
. /etc/os-release
case "${ID:-}" in
  ubuntu|debian) ;;
  *) die "v0.1 supports Debian/Ubuntu only (got ${ID:-unknown})" ;;
esac

case "$(dpkg --print-architecture)" in
  amd64) GOARCH="amd64" ;;
  arm64) GOARCH="arm64" ;;
  *) die "supported architectures: amd64, arm64" ;;
esac

# INSPECT: look before touching anything.
inspect_install
classify_state
log "inspecting: $STATE ($STATE_WHY)"

# A broken install is refused before anything on the host is touched.
if [[ "$STATE" == "BROKEN_INSTALL" ]]; then
  plan_everything
  show_plan
  log "refusing to touch a broken install: $STATE_WHY"
  log "nothing was changed; repair it by hand (see docs/en/33-rerunnable-installer.md) or restore from a backup"
  exit 3
fi

reset_plan() {
  PLAN_ITEM=(); PLAN_ACT=(); PLAN_DETAIL=(); PLAN_CHANGES=0; REFUSED=""
  DO_BIN=0; DO_UNIT=0; DO_PKI=0; DO_KEYGEN=0; DO_PAIRING=0; DO_RESTART=0; DO_START=0; DO_ENABLE=0
  DO_FIXPERM=0; DO_AGENT_UNIT=0; DO_AGENT_ENABLE=0; DO_TOKEN=0; DO_JOBKEY=0; DO_ROOTPIN=0; DO_RELSTATE=0
  DO_BCC_UNIT=0; DO_BCC_ENABLE=0; DO_BCC_ADMIN=0; DO_BCC_ACCESS=0; DO_BCC_JOBKEY=0
}

# Prerequisites (apt packages, the Go toolchain for --from-source) are the only
# host changes made before the final plan. On an install that already exists
# they are listed in a preliminary plan and need the same confirmation.
export DEBIAN_FRONTEND=noninteractive
NEED_APT=0; NEED_GO=0
if [[ -n "$BAFT_OFFLINE_DIR" ]]; then
  [[ "$BAFT_INSTALL_FROM" == "release" ]] || die "--offline installs a signed release; it cannot be combined with --from-source"
  [[ -d "$BAFT_OFFLINE_DIR" ]] || die "offline directory $BAFT_OFFLINE_DIR does not exist"
  for c in python3 openssl sha256sum; do
    command -v "$c" >/dev/null 2>&1 || die "offline install needs $c, which is not installed (install it from your OS media or mirror first)"
  done
  log "offline install from $BAFT_OFFLINE_DIR (no network, no apt)"
elif [[ "$BAFT_INSTALL_FROM" == "release" ]]; then
  for c in curl openssl python3; do command -v "$c" >/dev/null 2>&1 || NEED_APT=1; done
  [[ -s /etc/ssl/certs/ca-certificates.crt ]] || NEED_APT=1
else
  for c in curl git jq python3 cc make; do command -v "$c" >/dev/null 2>&1 || NEED_APT=1; done
  if [[ "$BCC_ONLY" == "1" ]]; then command -v openssl >/dev/null 2>&1 || NEED_APT=1; fi
  [[ -s /etc/ssl/certs/ca-certificates.crt ]] || NEED_APT=1
  if command -v go >/dev/null 2>&1 && [[ "$(go version | awk '{print $3}' | sed 's/^go//')" == "$BAFT_GO_VERSION" ]]; then NEED_GO=0; else NEED_GO=1; fi
fi

install_go() {
  [[ "$NEED_GO" == 1 ]] || { log "Go $BAFT_GO_VERSION already installed"; return; }
  local manifest file sha tmp
  manifest="$(curl -fsSL 'https://go.dev/dl/?mode=json&include=all')" || die "cannot read Go release manifest"
  file="go${BAFT_GO_VERSION}.linux-${GOARCH}.tar.gz"
  sha="$(jq -r --arg f "$file" '.[] | .files[] | select(.filename==$f) | .sha256' <<<"$manifest" | head -n1)"
  [[ "$sha" =~ ^[0-9a-f]{64}$ ]] || die "checksum not found for $file"
  tmp="$(mktemp)"
  curl -fL --retry 3 --retry-all-errors "https://go.dev/dl/$file" -o "$tmp"
  echo "$sha  $tmp" | sha256sum -c -
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$tmp"
  rm -f "$tmp"
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
}

if [[ "$NEED_APT" == 1 || "$NEED_GO" == 1 ]]; then
  if [[ "$STATE" != "FRESH_INSTALL" ]]; then
    plan_everything
    [[ "$NEED_APT" == 1 ]] && plan_add "packages" create "install the missing tools with apt (ca-certificates, curl, openssl, python3$([[ "$BAFT_INSTALL_FROM" == source ]] && printf ', git, jq, build-essential'))"
    [[ "$NEED_GO" == 1 ]] && plan_add "go toolchain" create "install Go $BAFT_GO_VERSION into /usr/local/go"
    show_plan
    confirm_plan
    reset_plan
  fi
  if [[ "$NEED_APT" == 1 ]]; then
    apt-get update
    if [[ "$BAFT_INSTALL_FROM" == "release" ]]; then
      apt-get install -y --no-install-recommends ca-certificates curl openssl python3
    else
      if [[ "$BCC_ONLY" == "1" ]]; then
        apt-get install -y --no-install-recommends ca-certificates curl git jq python3 openssl build-essential
      else
        apt-get install -y --no-install-recommends ca-certificates curl git jq python3 build-essential
      fi
    fi
  fi
  if [[ "$NEED_GO" == 1 ]]; then install_go; fi
else
  [[ -n "$BAFT_OFFLINE_DIR" ]] || log "the required tools are already installed; apt is not run"
fi

# Staging only: downloads, verification and builds happen in a private temp
# directory. Nothing is written to a final path before the plan is confirmed.
STAGE="$(mktemp -d)"
CLEANUP+=("$STAGE")
STAGE_BIN="$STAGE/baft"; STAGE_PAIR="$STAGE/baft-pair"; STAGE_AGENT="$STAGE/baft-agent"; STAGE_BCC="$STAGE/baft-bcc"
REL_DIR=""
REL_REV=""
if [[ "$BCC_ONLY" == "1" ]]; then
  REL_ARTIFACTS=("baft-bcc-linux-$GOARCH")
else
  REL_ARTIFACTS=("baft-linux-$GOARCH" "baft-pair-linux-$GOARCH")
  if [[ "$AGENT_ONLY" == "1" ]]; then REL_ARTIFACTS+=("baft-agent-linux-$GOARCH"); fi
fi
fetch_release() {
  local url="$BAFT_RELEASE_URL" f
  REL_DIR="$(mktemp -d)"
  REL_REV="$(mktemp)"
  CLEANUP+=("$REL_DIR" "$REL_REV")
  if [[ -n "$BAFT_OFFLINE_DIR" ]]; then
    for f in manifest.json release-key.cert.json SHA256SUMS "${REL_ARTIFACTS[@]}"; do
      [[ -f "$BAFT_OFFLINE_DIR/release/$f" ]] || die "release/$f is missing from the offline bundle $BAFT_OFFLINE_DIR"
      cp "$BAFT_OFFLINE_DIR/release/$f" "$REL_DIR/$f"
    done
    [[ -f "$BAFT_OFFLINE_DIR/revocations.json" ]] || die "revocations.json is missing from the offline bundle"
    cp "$BAFT_OFFLINE_DIR/revocations.json" "$REL_REV"
  else
    if [[ -z "$url" ]]; then
      if [[ -n "$BAFT_VERSION" ]]; then
        url="https://github.com/${BAFT_GITHUB_REPO}/releases/download/${BAFT_VERSION}"
      else
        url="https://github.com/${BAFT_GITHUB_REPO}/releases/latest/download"
      fi
    fi
    for f in manifest.json release-key.cert.json SHA256SUMS "${REL_ARTIFACTS[@]}"; do
      curl -fsSL --retry 3 --retry-all-errors -o "$REL_DIR/$f" "$url/$f" || die "cannot download $f from $url"
    done
    curl -fsSL --retry 3 --retry-all-errors -o "$REL_REV" "$BAFT_REVOCATIONS_URL" || die "cannot download the revocation list from $BAFT_REVOCATIONS_URL"
  fi
  local got
  got="$(verify_release "$REL_DIR" "$REL_REV" 0 "${REL_ARTIFACTS[@]}")" || die "the downloaded release did not verify; nothing was installed"
  TARGET_REL="${got%% *}"
  log "verified signed release $got"
  if [[ "$BCC_ONLY" == "1" ]]; then
    install -m 0755 "$REL_DIR/baft-bcc-linux-$GOARCH" "$STAGE_BCC"
  else
    install -m 0755 "$REL_DIR/baft-linux-$GOARCH" "$STAGE_BIN"
    install -m 0755 "$REL_DIR/baft-pair-linux-$GOARCH" "$STAGE_PAIR"
    if [[ "$AGENT_ONLY" == "1" ]]; then install -m 0755 "$REL_DIR/baft-agent-linux-$GOARCH" "$STAGE_AGENT"; fi
  fi
}

build_from_source() {
  SRC="$STAGE/src"
  rm -rf "$SRC.tmp"
  if ! git clone --filter=blob:none --no-checkout "$BAFT_REPO_URL" "$SRC.tmp"; then
    [[ -n "$BAFT_MIRROR_URL" ]] || die "primary repository unavailable and BAFT_MIRROR_URL is unset"
    log "trying configured mirror"
    git clone --filter=blob:none --no-checkout "$BAFT_MIRROR_URL" "$SRC.tmp"
  fi
  git -C "$SRC.tmp" checkout --detach "$BAFT_REF"
  mv "$SRC.tmp" "$SRC"

  (
    cd "$SRC"
    go mod download
    if [[ "$BCC_ONLY" == "1" ]]; then
      if [[ "$BAFT_RUN_TESTS" == "1" ]]; then go test ./internal/bcc ./cmd/baft-bcc -count=1; fi
      go build -trimpath -ldflags="-s -w" -o "$STAGE_BCC" ./cmd/baft-bcc
    else
      if [[ "$BAFT_RUN_TESTS" == "1" ]]; then
        go test ./internal/recordshape ./internal/securityinternal ./internal/carrier/h2 ./internal/config ./cmd/baft ./cmd/baft-pair ./tests/integration -count=1
      fi
      go build -trimpath -ldflags="-s -w -X main.version=${BAFT_REF}" -o "$STAGE_BIN" ./cmd/baft
      go build -trimpath -ldflags="-s -w" -o "$STAGE_PAIR" ./cmd/baft-pair
      if [[ "$AGENT_ONLY" == "1" ]]; then go build -trimpath -ldflags="-s -w" -o "$STAGE_AGENT" ./cmd/baft-agent; fi
    fi
  )
  TARGET_REL="$BAFT_REF"
}

if [[ "$BAFT_INSTALL_FROM" == "release" ]]; then
  fetch_release
else
  build_from_source
fi
STAGED=1

# An existing config that the installed binary could not validate (the binary
# is missing or does not run) is validated with the verified staged binary.
# It is kept if it validates; if not the install is refused. It is never paired
# over.
if [[ "$BCC_ONLY" != "1" && "$AGENT_ONLY" != "1" && "$STEALTH_PRO" != "1" && "$F_CFG" == 1 && "$F_CFG_VALID" == 0 ]]; then
  if "$STAGE_BIN" config validate --file "$CONFIG" >/dev/null 2>&1; then
    F_CFG_VALID=1
    log "the existing config validates with the verified release; it is kept"
  else
    CFG_INVALID_WHY="$CONFIG exists but does not validate with the verified release binary"
  fi
fi
if [[ -n "$CFG_INVALID_WHY" ]]; then
  plan_everything
  show_plan
  log "refusing to touch a broken install: $STATE_WHY"
  log "nothing was changed; repair it by hand (see docs/en/33-rerunnable-installer.md) or restore from a backup"
  exit 3
fi

# Stealth Pro is an explicit config change on an installed host: the binaries
# are replaced, the config is backed up and swapped, nothing else is touched.
if [[ "$STEALTH_PRO" == "1" ]]; then
  SHAPE_CONFIG_TMP="$(mktemp "$BAFT_CONFIG_DIR/.stealth-XXXXXX.yaml")"
  CLEANUP+=("$SHAPE_CONFIG_TMP")
  SHAPE_ARGS=(
    --file "$BAFT_CONFIG_DIR/baft.yaml"
    --distribution "${BAFT_SHAPE_DISTRIBUTION:-normal}"
    --mean "${BAFT_SHAPE_MEAN:-192}"
    --stddev "${BAFT_SHAPE_STDDEV:-96}"
    --max-padding "${BAFT_SHAPE_MAX_PADDING:-512}"
    --max-ratio "${BAFT_SHAPE_MAX_RATIO:-0.5}"
    --jitter-min-us "${BAFT_JITTER_MIN_US:-25}"
    --jitter-max-us "${BAFT_JITTER_MAX_US:-250}"
  )
  "$STAGE_BIN" config stealth-pro "${SHAPE_ARGS[@]}" >"$SHAPE_CONFIG_TMP"
  "$STAGE_BIN" config validate --file "$SHAPE_CONFIG_TMP"
  install -m 0755 -o root -g root "$STAGE_BIN" "$BAFT_BIN"
  install -m 0755 -o root -g root "$STAGE_PAIR" "$BAFT_PAIR_BIN"
  if [[ "$BAFT_INSTALL_FROM" == "release" ]]; then
    verify_release "$REL_DIR" "$REL_REV" 1 "${REL_ARTIFACTS[@]}" >/dev/null || die "could not record the installed release"
  fi
  cp -p "$BAFT_CONFIG_DIR/baft.yaml" "$BAFT_CONFIG_DIR/baft.yaml.before-stealth-$(date +%s)"
  chown root:"$BAFT_USER" "$SHAPE_CONFIG_TMP"
  chmod 0640 "$SHAPE_CONFIG_TMP"
  mv "$SHAPE_CONFIG_TMP" "$BAFT_CONFIG_DIR/baft.yaml"
  log "Stealth Pro configured. Configure BOTH peers, then restart baft on each host."
  log "Existing certificates, Noise keys, pairing state and systemd unit retained."
  exit 0
fi

# BUILD PLAN -> SHOW PLAN -> confirm.
plan_everything
preflight_service_paths
show_plan
if [[ -n "$REFUSED" ]]; then
  log "refusing to touch a broken install: $REFUSED"
  log "nothing was changed; repair it by hand (see docs/en/33-rerunnable-installer.md) or restore from a backup"
  exit 3
fi
confirm_plan

# APPLY. From here a failure rolls back the files and restarts the service
# that was running before.
APPLYING=1
WAS_ACTIVE="$F_ACTIVE"; WAS_AGENT_ACTIVE="$F_AGENT_ACTIVE"; WAS_BCC_ACTIVE="$F_BCC_ACTIVE"; WAS_ENABLED="$F_ENABLED"; WAS_AGENT_ENABLED="$F_AGENT_ENABLED"; WAS_BCC_ENABLED="$F_BCC_ENABLED"

ensure_base() {
  if [[ "$BCC_ONLY" == "1" ]]; then
    [[ -d "$BAFT_BCC_CONFIG_DIR" ]] || install -d -m 0700 -o root -g root "$BAFT_BCC_CONFIG_DIR"
    [[ -d "$BAFT_BCC_STATE_DIR" ]] || install -d -m 0700 -o root -g root "$BAFT_BCC_STATE_DIR"
    [[ -d "$BAFT_PREFIX" ]] || install -d -m 0755 -o root -g root "$BAFT_PREFIX"
    [[ -d "$(dirname "$BAFT_BCC_BIN")" ]] || install -d -m 0755 -o root -g root "$(dirname "$BAFT_BCC_BIN")"
    if [[ "$BAFT_INSTALL_FROM" == "source" && -d "$STAGE/src" ]]; then
      rm -rf "$BAFT_PREFIX/src"
      mv "$STAGE/src" "$BAFT_PREFIX/src"
    fi
    return 0
  fi
  if [[ "$F_USER" == 0 ]]; then
    useradd --system --home-dir "$BAFT_STATE_DIR" --create-home --shell /usr/sbin/nologin "$BAFT_USER"
  fi
  [[ -d "$BAFT_CONFIG_DIR" ]] || install -d -m 0750 -o root -g "$BAFT_USER" "$BAFT_CONFIG_DIR"
  [[ -d "$BAFT_STATE_DIR" ]] || install -d -m 0700 -o "$BAFT_USER" -g "$BAFT_USER" "$BAFT_STATE_DIR"
  [[ -d "$BAFT_PREFIX" ]] || install -d -m 0755 -o root -g root "$BAFT_PREFIX"
  [[ -d "$(dirname "$BAFT_BIN")" ]] || install -d -m 0755 -o root -g root "$(dirname "$BAFT_BIN")"
  if [[ "$BAFT_INSTALL_FROM" == "source" && -d "$STAGE/src" ]]; then
    rm -rf "$BAFT_PREFIX/src"
    mv "$STAGE/src" "$BAFT_PREFIX/src"
  fi
}

# install_changed NEW PATH: replaces PATH only when the bytes differ.
install_changed() {
  local new="$1" path="$2"
  [[ -f "$new" ]] || return 0
  if same_file "$new" "$path"; then return 0; fi
  backup_file "$path"
  install -m 0755 -o root -g root "$new" "$path"
}

install_binaries() {
  if [[ "$BCC_ONLY" == "1" ]]; then
    install_changed "$STAGE_BCC" "$BAFT_BCC_BIN"
    "$BAFT_BCC_BIN" --help >/dev/null 2>&1 || die "verify: the installed $BAFT_BCC_BIN does not run"
  else
    install_changed "$STAGE_BIN" "$BAFT_BIN"
    install_changed "$STAGE_PAIR" "$BAFT_PAIR_BIN"
    if [[ "$AGENT_ONLY" == "1" ]]; then install_changed "$STAGE_AGENT" "$BAFT_AGENT_BIN"; fi
    # A new binary that does not run is caught before any service is touched.
    "$BAFT_BIN" version >/dev/null 2>&1 || die "verify: the installed $BAFT_BIN does not run"
  fi
  fail_point post-binaries
}

# wait_active UNIT SECONDS: active, and still the same process a few seconds
# later (a unit whose binary exits at once is briefly "active" before it dies).
wait_active() {
  local i pid
  for ((i = 0; i < $2; i++)); do
    if svc_active "$1"; then
      pid="$(systemctl show -p MainPID --value "$1" 2>/dev/null || true)"
      sleep 3
      if svc_active "$1" && [[ "$(systemctl show -p MainPID --value "$1" 2>/dev/null || true)" == "$pid" && "$pid" != "0" ]]; then return 0; fi
    fi
    sleep 1
  done
  return 1
}

# VERIFY, then record the release (only after everything verified), then keep
# the rollback copies of the last three runs and stop rolling back.
finalize_apply() {
  if [[ "$BCC_ONLY" == "1" ]]; then
    "$BAFT_BCC_BIN" --help >/dev/null 2>&1 || die "verify: $BAFT_BCC_BIN does not run"
  else
    "$BAFT_BIN" version >/dev/null 2>&1 || die "verify: $BAFT_BIN does not run"
    if [[ "$AGENT_ONLY" != "1" && -f "$CONFIG" ]]; then
      "$BAFT_BIN" config validate --file "$CONFIG" >/dev/null 2>&1 || die "verify: $CONFIG does not validate"
    fi
  fi
  # Enablement is the last service change before the commit point; rollback
  # undoes it (ENABLED_BY_RUN) if anything after it fails.
  if [[ "$BCC_ONLY" == "1" && ( "$DO_BCC_UNIT" == 1 || "$DO_BCC_ENABLE" == 1 ) && "$WAS_BCC_ENABLED" == 0 ]]; then
    ENABLED_BY_RUN=1; systemctl enable "$BAFT_BCC_SERVICE.service"
  elif [[ "$AGENT_ONLY" == "1" && ( "$DO_AGENT_UNIT" == 1 || "$DO_AGENT_ENABLE" == 1 ) && "$WAS_AGENT_ENABLED" == 0 ]]; then
    ENABLED_BY_RUN=1; systemctl enable "$BAFT_AGENT_UNIT.service"
  elif [[ "$BCC_ONLY" != "1" && "$AGENT_ONLY" != "1" && ( "$DO_UNIT" == 1 || "$DO_ENABLE" == 1 ) && "$WAS_ENABLED" == 0 ]]; then
    ENABLED_BY_RUN=1; systemctl enable "$BAFT_SERVICE.service"
  fi
  fail_point pre-commit
  if [[ "$BAFT_INSTALL_FROM" == "release" && "$DO_RELSTATE" == 1 ]]; then
    backup_file "$BAFT_RELEASE_STATE"
    verify_release "$REL_DIR" "$REL_REV" 1 "${REL_ARTIFACTS[@]}" >/dev/null || die "could not record the installed release"
  fi
  APPLYING=0
  prune_backups
}

# Server enrollment: the agent runs as root (it restarts services and swaps
# binaries) with the rest of the system read-only. It holds the agent token,
# the pinned BCC job key and the pinned release root; no tunnel config yet.
install_agent() {
  [[ -d "$BAFT_AGENT_DIR" ]] || install -d -m 0700 -o root -g root "$BAFT_AGENT_DIR"
  [[ -d "$BAFT_AGENT_STATE_DIR" ]] || install -d -m 0700 -o root -g root "$BAFT_AGENT_STATE_DIR"
  if [[ "$DO_TOKEN" == 1 ]]; then
    backup_file "$BAFT_AGENT_DIR/token"
    install -m 0600 -o root -g root "$AGENT_TOKEN_TMP" "$BAFT_AGENT_DIR/token"
  fi
  if [[ "$DO_JOBKEY" == 1 ]]; then
    backup_file "$BAFT_AGENT_DIR/bcc-job.pub"
    printf '%s\n' "$BAFT_BCC_JOB_KEY" >"$BAFT_AGENT_DIR/bcc-job.pub"
  fi
  if [[ "$DO_ROOTPIN" == 1 ]]; then
    backup_file "$BAFT_AGENT_DIR/release-root.pub"
    printf '%s\n' "$BAFT_ROOT_PUB" >"$BAFT_AGENT_DIR/release-root.pub"
  fi
  if [[ "$DO_AGENT_UNIT" == 1 ]]; then
    backup_file "$AGENT_UNIT_FILE"
    install -m 0644 -o root -g root "$AGENT_UNIT_TMP" "$AGENT_UNIT_FILE"
    systemctl daemon-reload
  fi
  if [[ "$DO_RESTART" == 1 || "$DO_START" == 1 ]]; then TOUCHED_SERVICE=1; fi
  if [[ "$DO_RESTART" == 1 ]]; then systemctl restart "$BAFT_AGENT_UNIT.service"
  elif [[ "$DO_START" == 1 ]]; then systemctl start "$BAFT_AGENT_UNIT.service"; fi
  if [[ "$DO_RESTART" == 1 || "$DO_START" == 1 ]]; then
    wait_active "$BAFT_AGENT_UNIT.service" 20 || die "verify: $BAFT_AGENT_UNIT.service is not running"
  fi
  finalize_apply
  if [[ "$PLAN_CHANGES" == 0 ]]; then log "baft-agent is already enrolled as $BAFT_NODE_ID; nothing was changed"
  else log "baft-agent enrolled as $BAFT_NODE_ID with $BAFT_BCC_URL; add tunnels from BCC"; fi
}

bcc_access_base() {
  if [[ -n "$BAFT_BCC_PUBLIC_URL" ]]; then
    printf '%s' "${BAFT_BCC_PUBLIC_URL%/}"
    return
  fi
  if [[ -n "$BAFT_BCC_TLS_CERT" ]]; then printf 'https://%s' "$BAFT_BCC_LISTEN"
  else printf 'http://%s' "$BAFT_BCC_LISTEN"; fi
}

install_bcc() {
  local access_once="" access_show="" access_path="" base="" tmp token f

  if [[ -n "$BAFT_BCC_TLS_CERT" ]]; then
    [[ -r "$BAFT_BCC_TLS_CERT" ]] || die "cannot read --bcc-tls-cert $BAFT_BCC_TLS_CERT"
    [[ -r "$BAFT_BCC_TLS_KEY" ]] || die "cannot read --bcc-tls-key $BAFT_BCC_TLS_KEY"
  fi

  if [[ "$DO_BCC_ADMIN" == "1" ]]; then
    backup_file "$BAFT_BCC_ADMIN_TOKEN_FILE"
    tmp="$(mktemp)"; CLEANUP+=("$tmp")
    openssl rand -hex 32 >"$tmp"
    install -m 0600 -o root -g root "$tmp" "$BAFT_BCC_ADMIN_TOKEN_FILE"
  fi
  [[ -s "$BAFT_BCC_ADMIN_TOKEN_FILE" ]] || die "verify: BCC admin token is missing"

  if [[ "$DO_BCC_ACCESS" == "1" ]]; then
    backup_file "$BAFT_BCC_ACCESS_FILE"
    access_once="$("$BAFT_BCC_BIN" access init --access-file "$BAFT_BCC_ACCESS_FILE")" ||
      die "could not initialize BCC web access"
    chown root:root "$BAFT_BCC_ACCESS_FILE"
    chmod 0600 "$BAFT_BCC_ACCESS_FILE"
  fi
  "$BAFT_BCC_BIN" access show --access-file "$BAFT_BCC_ACCESS_FILE" >/dev/null ||
    die "verify: BCC access file is invalid"

  if [[ "$DO_BCC_JOBKEY" == "1" ]]; then
    backup_file "$BAFT_BCC_JOB_KEY_FILE"
    "$BAFT_BCC_BIN" jobkey show --job-key-file "$BAFT_BCC_JOB_KEY_FILE" >/dev/null ||
      die "could not initialize BCC job-signing key"
    chown root:root "$BAFT_BCC_JOB_KEY_FILE"
    chmod 0600 "$BAFT_BCC_JOB_KEY_FILE"
  else
    "$BAFT_BCC_BIN" jobkey show --job-key-file "$BAFT_BCC_JOB_KEY_FILE" >/dev/null ||
      die "verify: BCC job-signing key is invalid"
  fi

  for f in "$BAFT_BCC_ADMIN_TOKEN_FILE" "$BAFT_BCC_ACCESS_FILE" "$BAFT_BCC_JOB_KEY_FILE"; do
    [[ "$(stat -c '%a %U' "$f" 2>/dev/null)" == "600 root" ]] ||
      die "verify: $f must be root-owned mode 0600"
  done

  if [[ "$DO_BCC_UNIT" == "1" ]]; then
    backup_file "$BAFT_BCC_UNIT_FILE"
    tmp="$(mktemp)"; CLEANUP+=("$tmp")
    render_bcc_unit >"$tmp"
    install -m 0644 -o root -g root "$tmp" "$BAFT_BCC_UNIT_FILE"
    systemctl daemon-reload
  fi

  if [[ "$DO_RESTART" == "1" || "$DO_START" == "1" ]]; then
    # Fresh-start files are part of rollback. Existing live state is never
    # copied while BCC is running.
    for f in "$BAFT_BCC_STATE_FILE" "$BAFT_BCC_STATE_FILE.audit.jsonl"       "$BAFT_BCC_STATE_FILE.audit-anchor-outbox.json" "$BAFT_BCC_STATE_FILE.lock"       "$BAFT_BCC_STATE_FILE-wal" "$BAFT_BCC_STATE_FILE-shm"; do
      [[ -e "$f" ]] || backup_file "$f"
    done
    TOUCHED_SERVICE=1
  fi
  if [[ "$DO_RESTART" == "1" ]]; then systemctl restart "$BAFT_BCC_SERVICE.service"
  elif [[ "$DO_START" == "1" ]]; then systemctl start "$BAFT_BCC_SERVICE.service"; fi
  if [[ "$DO_RESTART" == "1" || "$DO_START" == "1" ]]; then
    wait_active "$BAFT_BCC_SERVICE.service" 20 || die "verify: $BAFT_BCC_SERVICE.service is not running"
  fi
  [[ -f "$BAFT_BCC_STATE_FILE" ]] || die "verify: BCC state file was not created"

  finalize_apply

  access_show="$("$BAFT_BCC_BIN" access show --access-file "$BAFT_BCC_ACCESS_FILE")"
  access_path="$(printf '%s\n' "$access_show" | sed -n 's/^[[:space:]]*path:[[:space:]]*//p' | head -n1)"
  base="$(bcc_access_base)"
  printf '\nBAFT Command Center\n'
  printf '  URL:      %s%s\n' "$base" "$access_path"
  if [[ -n "$access_once" ]]; then
    printf '\n%s\n' "$access_once"
  else
    printf '%s\n' "$access_show"
    printf '  password: not stored; rotate locally with: %s access regenerate --access-file %s\n' "$BAFT_BCC_BIN" "$BAFT_BCC_ACCESS_FILE"
  fi
  printf '  job key:  run locally: %s jobkey show --job-key-file %s\n\n' "$BAFT_BCC_BIN" "$BAFT_BCC_JOB_KEY_FILE"
}

ensure_base
install_binaries
if [[ "$BCC_ONLY" == "1" ]]; then
  install_bcc
  exit 0
fi
if [[ "$AGENT_ONLY" == "1" ]]; then
  install_agent
  exit 0
fi

generate_outer_pki_ex() {
  local host="$1" pki="$BAFT_CONFIG_DIR/pki" f
  for f in ca.pem ca.key server.pem server.key; do backup_file "$pki/$f"; done
  install -d -m 0750 -o root -g "$BAFT_USER" "$pki"
  "$BAFT_PAIR_BIN" pki --dir "$pki" --host "$host"
  chmod 0600 "$pki/ca.key"
  # Same contract as the Noise key: the service user owns server.key. The CA
  # signing key stays root-only; the service never needs it.
  chown "$BAFT_USER:$BAFT_USER" "$pki/server.key"
  chmod 0600 "$pki/server.key"
  chmod 0644 "$pki/ca.pem" "$pki/server.pem"
}

# The service reads this key as $BAFT_USER, and the runtime refuses a private
# key that group or other can access, so it must be owner-only for that user.
if [[ "$DO_KEYGEN" == 1 ]]; then
  backup_file "$NOISE_KEY"
  "$BAFT_PAIR_BIN" keygen --file "$NOISE_KEY" >/dev/null
fi
if [[ "$DO_KEYGEN" == 1 || "$DO_FIXPERM" == 1 ]]; then
  chown "$BAFT_USER:$BAFT_USER" "$NOISE_KEY"
  chmod 0600 "$NOISE_KEY"
fi

if [[ "$DO_UNIT" == 1 ]]; then
  backup_file "$UNIT_FILE"
  render_service_unit >"$UNIT_FILE"
  chmod 0644 "$UNIT_FILE"
  systemctl daemon-reload
fi

# The config is root-owned and group-readable by the service, and must pass
# the same loader `baft run` uses before the service is (re)started.
install_config() {
  chown root:"$BAFT_USER" "$CONFIG"
  chmod 0640 "$CONFIG"
  "$BAFT_BIN" config validate --file "$CONFIG" >/dev/null
}

if [[ "$DO_PAIRING" == 1 ]]; then
  if [[ "$RE_PAIR" == 1 && -f "$CONFIG" ]]; then
    backup_file "$CONFIG"
    cp -p -- "$CONFIG" "$CONFIG.before-repair-$(date +%Y%m%dT%H%M%S)"
    # The EX listens on the service port while pairing.
    if [[ "$ROLE" == "ex" && "$F_ACTIVE" == 1 ]]; then TOUCHED_SERVICE=1; systemctl stop "$BAFT_SERVICE.service"; fi
  else
    backup_file "$CONFIG"
  fi
  if [[ "$ROLE" == "ex" ]]; then
    [[ -n "$PUBLIC_ADDR" ]] || PUBLIC_ADDR="$(hostname -I | awk '{print $1}')"
    [[ -n "$PUBLIC_ADDR" ]] || die "cannot determine public address; use --public-address"
    if [[ "$DO_PKI" == 1 ]]; then generate_outer_pki_ex "$PUBLIC_ADDR"; fi
    IDENTITY="urn:baft:node:ex-$(head -c 6 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    SHAPING_FLAG=()
    if [[ "$RECORD_SHAPING" == "1" ]]; then
      SHAPING_FLAG=(--record-shaping)
    fi
    backup_file "$BAFT_STATE_DIR/pairing.psk"; backup_file "$BAFT_STATE_DIR/pairing.ex.json"
    PAIRING="$("$BAFT_PAIR_BIN" ex-code --key "$NOISE_KEY" --address "${PUBLIC_ADDR}:${BAFT_PORT}" \
      --server-name "$PUBLIC_ADDR" --identity "$IDENTITY" --ca-file "$BAFT_CONFIG_DIR/pki/ca.pem" \
      --psk-out "$BAFT_STATE_DIR/pairing.psk" --pending-out "$BAFT_STATE_DIR/pairing.ex.json" \
      --ttl 15m "${SHAPING_FLAG[@]}")"
    ACCEPT=("$BAFT_PAIR_BIN" ex-accept
      --pending "$BAFT_STATE_DIR/pairing.ex.json" --psk-file "$BAFT_STATE_DIR/pairing.psk"
      --key "$NOISE_KEY" --listen "0.0.0.0:${BAFT_PORT}"
      --ca-file "$BAFT_CONFIG_DIR/pki/ca.pem" --cert-file "$BAFT_CONFIG_DIR/pki/server.pem"
      --cert-key-file "$BAFT_CONFIG_DIR/pki/server.key" --target "$BAFT_TARGET"
      --metrics-listen "$BAFT_METRICS_LISTEN" --unix-socket "$BAFT_STATE_DIR/admin.sock"
      --config-out "$CONFIG")
    if [[ -z "$REPLY_CODE" && -z "$IR_SSH" && "$ASK_TTY" == 1 ]]; then
      printf '\nHow should the Iran (IR) server be set up?\n' >&2
      printf '  1) From here over SSH: give its address and login, nobody needs to log in to it\n' >&2
      printf '  2) By hand: run the installer there and paste its reply code here\n' >&2
      _how=""
      ask _how "Choose 1 or 2 [1]: " || true
      if [[ "${_how:-1}" == 1 ]]; then
        while [[ -z "$IR_SSH" ]]; do ask IR_SSH "IR server SSH (user@host or user@host:port, e.g. root@1.2.3.4): " || break; done
        _key=""
        ask _key "Path to an SSH private key (leave empty to use a password): " || true
        IR_SSH_KEY="${_key:-$IR_SSH_KEY}"
      fi
    fi
    if [[ -z "$REPLY_CODE" && -n "$IR_SSH" ]]; then
      REPLY_CODE="$(setup_ir_over_ssh "$PAIRING")" || REPLY_CODE=""
      [[ -n "$REPLY_CODE" ]] || log "falling back to manual pairing with the code below"
    fi
    if [[ -z "$REPLY_CODE" ]]; then
      printf '\nPAIRING CODE (secret, one-time, 15 minute lifetime):\n%s\n\n' "$PAIRING"
      ask REPLY_CODE "Run the installer on the IR server with this code, then paste its BAFTREPLY1 code here: " || true
    fi
    if [[ -z "$REPLY_CODE" ]]; then
      printf 'When the IR prints its reply code, finish pairing on this host with:\n  sudo %s --reply BAFTREPLY1:...\n  sudo chown root:%s %s && sudo chmod 0640 %s && sudo systemctl restart %s\n\n' \
        "${ACCEPT[*]}" "$BAFT_USER" "$CONFIG" "$CONFIG" "$BAFT_SERVICE" >&2
      finalize_apply
      log "installed; $BAFT_SERVICE.service starts once pairing is finished"
      exit 0
    fi
    "${ACCEPT[@]}" --reply "$REPLY_CODE" >/dev/null
    install_config
    TOUCHED_SERVICE=1
    systemctl restart "$BAFT_SERVICE.service"
    finalize_apply
    log "$BAFT_SERVICE.service started; listening on 0.0.0.0:${BAFT_PORT}, exiting to $BAFT_TARGET"
  else
    if [[ -z "$PAIRING_CODE" ]]; then
      ask PAIRING_CODE "Paste the BAFTPAIR1 pairing code printed by EX: " -s || die "a pairing code is required: pass --pairing-code (or BAFT_PAIRING_CODE) when there is no terminal"
      printf '\n' >&2
      [[ -n "$PAIRING_CODE" ]] || die "no pairing code entered"
    fi
    REPLY="$("$BAFT_PAIR_BIN" ir-apply --code "$PAIRING_CODE" --key "$NOISE_KEY" --state-dir "$BAFT_STATE_DIR" \
      --config-out "$CONFIG" --route-listen "$BAFT_ROUTE_LISTEN" --metrics-listen "$BAFT_METRICS_LISTEN" \
      --unix-socket "$BAFT_STATE_DIR/admin.sock")"
    chown -R "$BAFT_USER:$BAFT_USER" "$BAFT_STATE_DIR"
    install_config
    printf '\nREPLY CODE for the EX (secret, expires with the pairing code):\n%s\n\n' "$REPLY"
    # Until the EX accepts this reply the dialer cannot connect and exits;
    # Restart=on-failure retries every 2s, so it connects once the EX is up.
    TOUCHED_SERVICE=1
    systemctl restart "$BAFT_SERVICE.service"
    finalize_apply
    log "$BAFT_SERVICE.service started; local clients connect to $BAFT_ROUTE_LISTEN"
  fi
else
  # Already paired: only what the plan listed is done.
  if [[ "$DO_RESTART" == 1 || "$DO_START" == 1 ]]; then TOUCHED_SERVICE=1; fi
  if [[ "$DO_RESTART" == 1 ]]; then systemctl restart "$BAFT_SERVICE.service"
  elif [[ "$DO_START" == 1 ]]; then systemctl start "$BAFT_SERVICE.service"; fi
  if [[ "$DO_RESTART" == 1 || "$DO_START" == 1 ]]; then
    wait_active "$BAFT_SERVICE.service" 20 || die "verify: $BAFT_SERVICE.service is not running"
  fi
  finalize_apply
  if [[ "$PLAN_CHANGES" == 0 ]]; then log "already installed and current; nothing was changed"
  else log "$BAFT_SERVICE.service is running"; fi
fi
log "baft is installed: $BAFT_BIN and $BAFT_PAIR_BIN"
