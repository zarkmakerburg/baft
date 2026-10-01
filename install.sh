#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

BAFT_REPO_URL="${BAFT_REPO_URL:-https://github.com/zarkmakerburg/baft.git}"
BAFT_MIRROR_URL="${BAFT_MIRROR_URL:-}"
BAFT_REF="${BAFT_REF:-release-v1-goldapp}"
BAFT_GO_VERSION="${BAFT_GO_VERSION:-1.27.1}"
BAFT_PREFIX="${BAFT_PREFIX:-/opt/baft}"
BAFT_CONFIG_DIR="${BAFT_CONFIG_DIR:-/etc/baft}"
BAFT_STATE_DIR="${BAFT_STATE_DIR:-/var/lib/baft}"
BAFT_BIN="${BAFT_BIN:-/usr/local/bin/baft}"
BAFT_PAIR_BIN="${BAFT_PAIR_BIN:-/usr/local/bin/baft-pair}"
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

log(){ printf '[baft-install] %s\n' "$*" >&2; }
die(){ log "ERROR: $*"; exit 1; }
need_root(){ [[ "${EUID}" -eq 0 ]] || die "run as root"; }

usage() {
  cat <<EOF
Usage:
  sudo bash install.sh --role ex --public-address HOST_OR_IP [--reply-code BAFTREPLY1:...]
  sudo bash install.sh --role ir [--pairing-code BAFTPAIR1:...]
  sudo bash install.sh --role ex --stealth-pro  # existing pinned Noise config
  sudo bash install.sh --role ir --stealth-pro  # enable on BOTH peers

Environment:
  BAFT_REPO_URL BAFT_MIRROR_URL BAFT_REF BAFT_GO_VERSION
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
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ "$ROLE" == "ex" || "$ROLE" == "ir" ]] || die "--role must be ex or ir"
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
apt-get install -y --no-install-recommends   ca-certificates curl git jq python3 build-essential

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
install_go

if ! id "$BAFT_USER" >/dev/null 2>&1; then
  useradd --system --home-dir "$BAFT_STATE_DIR" --create-home --shell /usr/sbin/nologin "$BAFT_USER"
fi
install -d -m 0750 -o root -g "$BAFT_USER" "$BAFT_CONFIG_DIR"
install -d -m 0700 -o "$BAFT_USER" -g "$BAFT_USER" "$BAFT_STATE_DIR"
install -d -m 0755 -o root -g root "$BAFT_PREFIX"

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
if [[ "$STEALTH_PRO" == "1" ]]; then
  SHAPE_CONFIG_TMP="$(mktemp "$BAFT_CONFIG_DIR/.stealth-XXXXXX.yaml")"
  trap 'rm -f "${SHAPE_CONFIG_TMP:-}"' EXIT
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
