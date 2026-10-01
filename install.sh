#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

BAFT_REPO_URL="${BAFT_REPO_URL:-https://github.com/zarkmakerburg/baft.git}"
BAFT_MIRROR_URL="${BAFT_MIRROR_URL:-}"
BAFT_REF="${BAFT_REF:-r3.1-probabilistic-morphing}"
BAFT_GO_VERSION="${BAFT_GO_VERSION:-1.27.1}"
BAFT_PREFIX="${BAFT_PREFIX:-/opt/baft}"
BAFT_CONFIG_DIR="${BAFT_CONFIG_DIR:-/etc/baft}"
BAFT_STATE_DIR="${BAFT_STATE_DIR:-/var/lib/baft}"
BAFT_BIN="${BAFT_BIN:-/usr/local/bin/baft}"
BAFT_PAIR_BIN="${BAFT_PAIR_BIN:-/usr/local/bin/baft-pair}"
BAFT_USER="${BAFT_USER:-baft}"
BAFT_PORT="${BAFT_PORT:-8443}"
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
  sudo bash install.sh --role ex --public-address HOST_OR_IP
  sudo bash install.sh --role ir [--pairing-code BAFTPAIR1:...]
  sudo bash install.sh --role ex --stealth-pro  # existing pinned Noise config
  sudo bash install.sh --role ir --stealth-pro  # enable on BOTH peers

Environment:
  BAFT_REPO_URL BAFT_MIRROR_URL BAFT_REF BAFT_GO_VERSION
  BAFT_PREFIX BAFT_CONFIG_DIR BAFT_STATE_DIR BAFT_PORT
  BAFT_SHAPE_DISTRIBUTION BAFT_SHAPE_MEAN BAFT_SHAPE_STDDEV
  BAFT_SHAPE_MAX_PADDING BAFT_SHAPE_MAX_RATIO
  BAFT_JITTER_MIN_US BAFT_JITTER_MAX_US
Stealth Pro requires an existing baft.yaml with noise.key_file and
noise.peer_public_key. It updates that configuration without re-pairing.
It is experimental; statistical similarity to HTTPS is not established.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --role) ROLE="${2:-}"; shift 2 ;;
    --pairing-code) PAIRING_CODE="${2:-}"; shift 2 ;;
    --public-address) PUBLIC_ADDR="${2:-}"; shift 2 ;;
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
apt-get install -y --no-install-recommends   ca-certificates curl git openssl jq python3 build-essential

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
  go test ./internal/recordshape ./internal/securityinternal ./internal/carrier/h2 ./internal/config ./cmd/baft ./tests/integration -count=1
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
chown root:"$BAFT_USER" "$NOISE_KEY"
chmod 0640 "$NOISE_KEY"

generate_outer_pki_ex() {
  local host="$1" pki="$BAFT_CONFIG_DIR/pki"
  install -d -m 0750 -o root -g "$BAFT_USER" "$pki"
  openssl genpkey -algorithm ED25519 -out "$pki/ca.key"
  openssl req -x509 -new -key "$pki/ca.key" -days 3650 -subj "/CN=BAFT Local CA" -out "$pki/ca.pem"
  openssl genpkey -algorithm ED25519 -out "$pki/server.key"

  if [[ "$host" =~ ^[0-9a-fA-F:.]+$ ]]; then
    SAN="IP:$host"
  else
    SAN="DNS:$host"
  fi
  openssl req -new -key "$pki/server.key" -subj "/CN=$host"     -addext "subjectAltName=$SAN" -out "$pki/server.csr"
  openssl x509 -req -in "$pki/server.csr" -CA "$pki/ca.pem" -CAkey "$pki/ca.key"     -CAcreateserial -days 825 -copy_extensions copyall -out "$pki/server.pem"
  rm -f "$pki/server.csr" "$pki/ca.srl"
  chmod 0600 "$pki/ca.key" "$pki/server.key"
  chmod 0644 "$pki/ca.pem" "$pki/server.pem"
}

if [[ "$ROLE" == "ex" ]]; then
  [[ -n "$PUBLIC_ADDR" ]] || PUBLIC_ADDR="$(hostname -I | awk '{print $1}')"
  [[ -n "$PUBLIC_ADDR" ]] || die "cannot determine public address; use --public-address"
  generate_outer_pki_ex "$PUBLIC_ADDR"
  IDENTITY="urn:baft:node:ex-$(openssl rand -hex 6)"
  SHAPING_FLAG=()
  if [[ "$RECORD_SHAPING" == "1" ]]; then
    SHAPING_FLAG=(--record-shaping)
  fi
  PAIRING="$("$BAFT_PAIR_BIN" ex-code     --key "$NOISE_KEY"     --address "${PUBLIC_ADDR}:${BAFT_PORT}"     --server-name "$PUBLIC_ADDR"     --identity "$IDENTITY"     --ca-file "$BAFT_CONFIG_DIR/pki/ca.pem"     --psk-out "$BAFT_STATE_DIR/pairing.psk"     --ttl 15m "${SHAPING_FLAG[@]}")"
  chown "$BAFT_USER:$BAFT_USER" "$BAFT_STATE_DIR/pairing.psk"
  printf '\nPAIRING CODE (secret, one-time, 15 minute lifetime):\n%s\n\n' "$PAIRING"
else
  if [[ -z "$PAIRING_CODE" ]]; then
    [[ "$NONINTERACTIVE" == "1" ]] && die "BAFT_PAIRING_CODE is required in non-interactive mode"
    read -r -s -p "Paste BAFT pairing code: " PAIRING_CODE
    printf '\n'
  fi
  "$BAFT_PAIR_BIN" ir-apply --code "$PAIRING_CODE" --key "$NOISE_KEY" --state-dir "$BAFT_STATE_DIR" >/dev/null
  chown -R "$BAFT_USER:$BAFT_USER" "$BAFT_STATE_DIR"
fi

cat >/etc/systemd/system/baft.service <<EOF
[Unit]
Description=BAFT transport service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$BAFT_USER
Group=$BAFT_USER
ExecStart=$BAFT_BIN run --file $BAFT_CONFIG_DIR/baft.yaml
ExecReload=/bin/kill -HUP \$MAINPID
Restart=on-failure
RestartSec=2s
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
systemctl enable baft.service
log "installed $BAFT_BIN and $BAFT_PAIR_BIN"
log "systemd unit enabled; service starts after /etc/baft/baft.yaml is provisioned"
