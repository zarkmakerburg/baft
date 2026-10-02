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
# The offline release root public key (release/keys/root.pub). Empty until the
# owner's key ceremony; release installs are refused until it is pinned here.
BAFT_PINNED_ROOT_PUB=""
BAFT_ROOT_PUB="${BAFT_ROOT_PUB:-$BAFT_PINNED_ROOT_PUB}"
BAFT_ALLOW_DOWNGRADE="${BAFT_ALLOW_DOWNGRADE:-0}"
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
NONINTERACTIVE="${BAFT_NONINTERACTIVE:-0}"
RECORD_SHAPING=0
STEALTH_PRO=0
VERIFY_ONLY_DIR=""
CLEANUP=()
cleanup(){ if ((${#CLEANUP[@]})); then rm -rf -- "${CLEANUP[@]}"; fi; }
trap cleanup EXIT

log(){ printf '[baft-install] %s\n' "$*" >&2; }
die(){ log "ERROR: $*"; exit 1; }
need_root(){ [[ "${EUID}" -eq 0 ]] || die "run as root"; }

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
  sudo bash install.sh --role ex --public-address HOST_OR_IP [--reply-code BAFTREPLY1:...]
  sudo bash install.sh --role ir [--pairing-code BAFTPAIR1:...]
  sudo bash install.sh --role ex --stealth-pro  # existing pinned Noise config
  sudo bash install.sh --role ir --stealth-pro  # enable on BOTH peers

Options:
  --version vX.Y.Z    install this signed release (default: the latest)
  --allow-downgrade   accept a release older than the one installed
  --from-source       build from BAFT_REPO_URL at BAFT_REF (development only)

By default the installer downloads the signed release for this architecture,
verifies it against the pinned root key and the current revocation list, and
refuses a downgrade or a re-tagged version (state in BAFT_RELEASE_STATE).

Environment:
  BAFT_INSTALL_FROM (release|source) BAFT_VERSION BAFT_RELEASE_URL
  BAFT_GITHUB_REPO BAFT_REVOCATIONS_URL BAFT_ROOT_PUB BAFT_RELEASE_STATE
  BAFT_ALLOW_DOWNGRADE
  BAFT_REPO_URL BAFT_MIRROR_URL BAFT_REF BAFT_GO_VERSION (source installs)
  BAFT_PREFIX BAFT_CONFIG_DIR BAFT_STATE_DIR BAFT_PORT BAFT_SERVICE
  BAFT_TARGET (EX: fixed IP:port traffic exits to, default 127.0.0.1:2443)
  BAFT_ROUTE_LISTEN (IR: loopback address local clients use, default 127.0.0.1:1443)
  BAFT_METRICS_LISTEN BAFT_RUN_TESTS BAFT_PAIRING_CODE BAFT_REPLY_CODE
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
    --enable-record-shaping) die "use --stealth-pro with a pinned Noise configuration" ;;
    --stealth-pro) STEALTH_PRO=1; RECORD_SHAPING=1; shift ;;
    --version) BAFT_VERSION="${2:-}"; shift 2 ;;
    --allow-downgrade) BAFT_ALLOW_DOWNGRADE=1; shift ;;
    --from-source) BAFT_INSTALL_FROM=source; shift ;;
    --verify-release) VERIFY_ONLY_DIR="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

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

[[ "$ROLE" == "ex" || "$ROLE" == "ir" ]] || die "--role must be ex or ir"
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

export DEBIAN_FRONTEND=noninteractive
apt-get update
if [[ "$BAFT_INSTALL_FROM" == "release" ]]; then
  apt-get install -y --no-install-recommends ca-certificates curl openssl python3
else
  apt-get install -y --no-install-recommends ca-certificates curl git jq python3 build-essential
fi

install_go() {
  if command -v go >/dev/null 2>&1; then
    current="$(go version | awk '{print $3}' | sed 's/^go//')"
    if [[ "$current" == "$BAFT_GO_VERSION" ]]; then
      log "Go $current already installed"
      return
    fi
  fi
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
if [[ "$BAFT_INSTALL_FROM" == "source" ]]; then
  install_go
fi

if ! id "$BAFT_USER" >/dev/null 2>&1; then
  useradd --system --home-dir "$BAFT_STATE_DIR" --create-home --shell /usr/sbin/nologin "$BAFT_USER"
fi
install -d -m 0750 -o root -g "$BAFT_USER" "$BAFT_CONFIG_DIR"
install -d -m 0700 -o "$BAFT_USER" -g "$BAFT_USER" "$BAFT_STATE_DIR"
install -d -m 0755 -o root -g root "$BAFT_PREFIX"

REL_DIR=""
REL_REV=""
REL_ARTIFACTS=("baft-linux-$GOARCH" "baft-pair-linux-$GOARCH")
fetch_release() {
  local url="$BAFT_RELEASE_URL" f
  if [[ -z "$url" ]]; then
    if [[ -n "$BAFT_VERSION" ]]; then
      url="https://github.com/${BAFT_GITHUB_REPO}/releases/download/${BAFT_VERSION}"
    else
      url="https://github.com/${BAFT_GITHUB_REPO}/releases/latest/download"
    fi
  fi
  REL_DIR="$(mktemp -d)"
  REL_REV="$(mktemp)"
  CLEANUP+=("$REL_DIR" "$REL_REV")
  for f in manifest.json release-key.cert.json SHA256SUMS "${REL_ARTIFACTS[@]}"; do
    curl -fsSL --retry 3 --retry-all-errors -o "$REL_DIR/$f" "$url/$f" || die "cannot download $f from $url"
  done
  curl -fsSL --retry 3 --retry-all-errors -o "$REL_REV" "$BAFT_REVOCATIONS_URL" || die "cannot download the revocation list from $BAFT_REVOCATIONS_URL"
  local got
  got="$(verify_release "$REL_DIR" "$REL_REV" 0 "${REL_ARTIFACTS[@]}")" || die "the downloaded release did not verify; nothing was installed"
  log "verified signed release $got"
  install -m 0755 "$REL_DIR/baft-linux-$GOARCH" "$BAFT_BIN.new"
  install -m 0755 "$REL_DIR/baft-pair-linux-$GOARCH" "$BAFT_PAIR_BIN.new"
}

build_from_source() {
  SRC="$BAFT_PREFIX/src"
  rm -rf "$SRC.tmp"
  if ! git clone --filter=blob:none --no-checkout "$BAFT_REPO_URL" "$SRC.tmp"; then
    [[ -n "$BAFT_MIRROR_URL" ]] || die "primary repository unavailable and BAFT_MIRROR_URL is unset"
    log "trying configured mirror"
    git clone --filter=blob:none --no-checkout "$BAFT_MIRROR_URL" "$SRC.tmp"
  fi
  git -C "$SRC.tmp" checkout --detach "$BAFT_REF"
  rm -rf "$SRC"
  mv "$SRC.tmp" "$SRC"

  (
    cd "$SRC"
    go mod download
    if [[ "$BAFT_RUN_TESTS" == "1" ]]; then
      go test ./internal/recordshape ./internal/securityinternal ./internal/carrier/h2 ./internal/config ./cmd/baft ./cmd/baft-pair ./tests/integration -count=1
    fi
    go build -trimpath -ldflags="-s -w -X main.version=${BAFT_REF}" -o "$BAFT_BIN.new" ./cmd/baft
    go build -trimpath -ldflags="-s -w" -o "$BAFT_PAIR_BIN.new" ./cmd/baft-pair
  )
}

if [[ "$BAFT_INSTALL_FROM" == "release" ]]; then
  fetch_release
else
  build_from_source
fi
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
  "$BAFT_BIN.new" config stealth-pro "${SHAPE_ARGS[@]}" >"$SHAPE_CONFIG_TMP"
  "$BAFT_BIN.new" config validate --file "$SHAPE_CONFIG_TMP"
fi
install -m 0755 -o root -g root "$BAFT_BIN.new" "$BAFT_BIN"
install -m 0755 -o root -g root "$BAFT_PAIR_BIN.new" "$BAFT_PAIR_BIN"
rm -f "$BAFT_BIN.new" "$BAFT_PAIR_BIN.new"
if [[ "$BAFT_INSTALL_FROM" == "release" ]]; then
  # Record the release only once its binaries are in place.
  verify_release "$REL_DIR" "$REL_REV" 1 "${REL_ARTIFACTS[@]}" >/dev/null || die "could not record the installed release"
fi

if [[ "$STEALTH_PRO" == "1" ]]; then
  cp -p "$BAFT_CONFIG_DIR/baft.yaml" "$BAFT_CONFIG_DIR/baft.yaml.before-stealth-$(date +%s)"
  chown root:"$BAFT_USER" "$SHAPE_CONFIG_TMP"
  chmod 0640 "$SHAPE_CONFIG_TMP"
  mv "$SHAPE_CONFIG_TMP" "$BAFT_CONFIG_DIR/baft.yaml"
  log "Stealth Pro configured. Configure BOTH peers, then restart baft on each host."
  log "Existing certificates, Noise keys, pairing state and systemd unit retained."
  exit 0
fi

NOISE_KEY="$BAFT_CONFIG_DIR/noise-key.json"
"$BAFT_PAIR_BIN" keygen --file "$NOISE_KEY" >/dev/null
# The service reads this key as $BAFT_USER, and the runtime refuses a private
# key that group or other can access, so it must be owner-only for that user.
chown "$BAFT_USER:$BAFT_USER" "$NOISE_KEY"
chmod 0600 "$NOISE_KEY"

generate_outer_pki_ex() {
  local host="$1" pki="$BAFT_CONFIG_DIR/pki"
  install -d -m 0750 -o root -g "$BAFT_USER" "$pki"
  "$BAFT_PAIR_BIN" pki --dir "$pki" --host "$host"
  chmod 0600 "$pki/ca.key"
  # Same contract as the Noise key: the service user owns server.key. The CA
  # signing key stays root-only; the service never needs it.
  chown "$BAFT_USER:$BAFT_USER" "$pki/server.key"
  chmod 0600 "$pki/server.key"
  chmod 0644 "$pki/ca.pem" "$pki/server.pem"
}

CONFIG="$BAFT_CONFIG_DIR/baft.yaml"
CAPS=""
if (( BAFT_PORT < 1024 )); then
  CAPS=$'AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE'
fi

cat >"/etc/systemd/system/$BAFT_SERVICE.service" <<EOF
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
$CAPS
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
systemctl daemon-reload
systemctl enable "$BAFT_SERVICE.service"

# The config is root-owned and group-readable by the service, and must pass
# the same loader `baft run` uses before the service is (re)started.
install_config() {
  chown root:"$BAFT_USER" "$CONFIG"
  chmod 0640 "$CONFIG"
  "$BAFT_BIN" config validate --file "$CONFIG" >/dev/null
}

if [[ "$ROLE" == "ex" ]]; then
  [[ -n "$PUBLIC_ADDR" ]] || PUBLIC_ADDR="$(hostname -I | awk '{print $1}')"
  [[ -n "$PUBLIC_ADDR" ]] || die "cannot determine public address; use --public-address"
  generate_outer_pki_ex "$PUBLIC_ADDR"
  IDENTITY="urn:baft:node:ex-$(head -c 6 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  SHAPING_FLAG=()
  if [[ "$RECORD_SHAPING" == "1" ]]; then
    SHAPING_FLAG=(--record-shaping)
  fi
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
  printf '\nPAIRING CODE (secret, one-time, 15 minute lifetime):\n%s\n\n' "$PAIRING"
  if [[ -z "$REPLY_CODE" && "$NONINTERACTIVE" != "1" ]]; then
    read -r -p "Run the IR installer with this code, then paste its BAFTREPLY1 code here: " REPLY_CODE
  fi
  if [[ -z "$REPLY_CODE" ]]; then
    printf 'When the IR prints its reply code, finish pairing on this host with:\n  sudo %s --reply BAFTREPLY1:...\n  sudo chown root:%s %s && sudo chmod 0640 %s && sudo systemctl restart %s\n\n' \
      "${ACCEPT[*]}" "$BAFT_USER" "$CONFIG" "$CONFIG" "$BAFT_SERVICE" >&2
    log "installed; $BAFT_SERVICE.service starts once pairing is finished"
    exit 0
  fi
  "${ACCEPT[@]}" --reply "$REPLY_CODE" >/dev/null
  install_config
  systemctl restart "$BAFT_SERVICE.service"
  log "$BAFT_SERVICE.service started; listening on 0.0.0.0:${BAFT_PORT}, exiting to $BAFT_TARGET"
else
  if [[ -z "$PAIRING_CODE" ]]; then
    [[ "$NONINTERACTIVE" == "1" ]] && die "BAFT_PAIRING_CODE is required in non-interactive mode"
    read -r -s -p "Paste BAFT pairing code: " PAIRING_CODE
    printf '\n'
  fi
  REPLY="$("$BAFT_PAIR_BIN" ir-apply --code "$PAIRING_CODE" --key "$NOISE_KEY" --state-dir "$BAFT_STATE_DIR" \
    --config-out "$CONFIG" --route-listen "$BAFT_ROUTE_LISTEN" --metrics-listen "$BAFT_METRICS_LISTEN" \
    --unix-socket "$BAFT_STATE_DIR/admin.sock")"
  chown -R "$BAFT_USER:$BAFT_USER" "$BAFT_STATE_DIR"
  install_config
  printf '\nREPLY CODE for the EX (secret, expires with the pairing code):\n%s\n\n' "$REPLY"
  # Until the EX accepts this reply the dialer cannot connect and exits;
  # Restart=on-failure retries every 2s, so it connects once the EX is up.
  systemctl restart "$BAFT_SERVICE.service"
  log "$BAFT_SERVICE.service started; local clients connect to $BAFT_ROUTE_LISTEN"
fi
log "installed $BAFT_BIN and $BAFT_PAIR_BIN"
