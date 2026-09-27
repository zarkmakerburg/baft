# R3.1 Morphing — v0.2-Pro candidate

Status: experimental review candidate, not a production release or evidence of
filtering resistance. Stage D remains paused; recovery.enabled remains false.
R3.1 supersedes the historical R3 deterministic-shaping scope only.

## Technical analysis

The transmit path is Session bytes -> Noise AES-GCM -> probabilistic envelope ->
HTTP/2 request/response body -> outer TLS -> TCP. The receiver removes bounded
padding before Noise authentication/decryption. No plaintext offset, TWRL credit,
replay frontier, PADL lease or ECRL ownership transition is altered.

`internal/recordshape/morphing.go` samples a truncated normal or Laplace CDF using
crypto/rand. A bounded 48-step inverse CDF yields integer padding sizes without
power-of-two buckets or clamp-induced endpoint spikes. Padding contents use
crypto/rand too. One complete envelope is passed to the writer to avoid forcing
a separate H2 flush for a four-byte prefix. Stream ownership remains serialized
by the Noise connection lock; codec users must serialize writes to the same IO.

Wire format: `body_length:u32 | ciphertext_length:u32 | ciphertext | padding`.
No custom cryptography is introduced. The envelope/padding is outside Noise
AEAD; a TLS-terminating intermediary can alter padding or corrupt framing and
cause failure, but cannot forge an accepted ciphertext. Parser bounds and sticky
read/write errors limit failure handling. Partial writes poison the connection
instead of retrying after consuming a Noise nonce.

The Noise prologue includes `/probabilistic-records-v2` whenever shaping is on.
Both peers must upgrade and agree on on/off state. Historical R3 shaped peers
are intentionally incompatible and fail authentication. Distribution, ratio,
and jitter parameters can differ by direction: receive framing has a fixed
format-wide cap rather than interpreting the local send budget as a receive cap.

This removes a *specific fixed-bucket application-record signature*. It does not
establish statistical equivalence to HTTPS or defeat a classifier. A normal
additive padding law is not an empirical HTTPS traffic model. HTTP/2, TLS and TCP
can split/coalesce writes; the record lengths measured by unit tests are not
IP packet lengths. RFC 9113 sections 6.1/10.7 discuss DATA padding and traffic
analysis: https://www.rfc-editor.org/rfc/rfc9113.html . This implementation uses
an application envelope, not HTTP/2's PADDED flag.

## Authentication and active probes

The old no-Noise configuration continues to use mTLS. Explicit `noise` mode
uses server-verified TLS 1.3 plus mutually pinned Noise IK, with exactly one
configured peer key mapped to one allowlisted identity. HELLO is never trusted
as the authentication source. Routes and identity revocation remain enforced;
certificate serial/fingerprint revocation of the client is not applicable in
Noise mode. This is an opt-in authentication mode, not auto-fallback.

Ordinary HTTP requests, invalid IK messages, unknown keys, and revoked peers
receive the same HTML handler. Success headers and the second IK message are
withheld until message one authenticates. The client sends message one before
waiting for HTTP response headers. HTTP/1.1 serves only the website; carriers
still require HTTP/2. Default pending handshake limit: 64; read timeout: 5 s;
handshake write deadline: read timeout + 1 s; client handshake timeout: 10 s.
Malformed TLS handshakes cannot be answered with an HTTP page on a TLS port.
A default HTML page is included; operators may specify `cover_html_file` (1 MiB
maximum). It does not spoof Nginx/Apache headers or prove an indistinguishable
web-server fingerprint. A locally issued BAFT certificate can itself identify
the deployment; the operator must choose appropriate valid TLS material.

The runtime only accepts pre-pinned peers. R2 IKpsk0 helpers remain tested, but
automatic transactional enrollment and PSK erasure are not implemented here.

## Operational code and configuration

Add this section to each existing validated config, substituting the public key
of the *other* node. On the listener `allowed_peer_identities` has one identity.

```yaml
noise:
  key_file: /etc/baft/noise-key.json
  peer_public_key: REPLACE_WITH_PEER_43_CHARACTER_BASE64URL_PUBLIC_KEY
  # cover_html_file: /etc/baft/site/index.html
  record_shaping:
    enabled: true
    distribution: normal
    mean_bytes: 192
    stddev_bytes: 96
    max_padding_bytes: 512
    max_padding_ratio: 0.5
    jitter_min_us: 25
    jitter_max_us: 250
```

Keys must be readable by the service owner only (0600). Use the existing
`baft-pair keygen --file PATH` helper on each machine and pin public keys through
an authenticated administrative channel. Never send the private key to the peer.
Existing TLS, Route, identity, and metrics settings remain required. The current
schema still requires local cert/key paths on the dialer, although Noise mode
does not send a client certificate.

Mean/sd refer to the underlying distribution. Truncation and ratio caps change
the observed moments. Validation requires mean <= 8*sd, mean <= max padding,
sd >= 1, padding <= 65536, ratio in [0,16], and jitter in [0,100000] microseconds.
Zero ratio disables only the ratio cap. Ratio limits padding/ciphertext bytes,
not TLS/TCP/header overhead. Receive body cap is 1 MiB, additionally bounded by
max ciphertext + 4 + 65536; padding is discarded without an allocation.
Send memory is one bounded envelope per serialized writer. TLS/H2 allocations
and envelope allocations are separate from TWRL application-buffer accounting.

After provisioning both peers, enable on an existing installation with one
command, from this checkout (EX example; use `--role ir` on IR):

```bash
sudo bash install.sh --role ex --stealth-pro
```

This upgrade builds/tests first, emits and validates a candidate config, backs
up the old config, then atomically replaces it. It preserves keys/certificates,
pairing state and the existing systemd unit. It does not automatically restart
one side while the other is unprepared. Restart both when both configs match.
The script defaults to the R3.1 branch; pin BAFT_REF to a reviewed commit for a
reproducible deployment. It is **not** a validated one-command fresh-VM install.
The old R2 installer does not provision complete routing/TLS/enrollment by itself.

Environment tuning: BAFT_SHAPE_DISTRIBUTION, BAFT_SHAPE_MEAN, BAFT_SHAPE_STDDEV,
BAFT_SHAPE_MAX_PADDING, BAFT_SHAPE_MAX_RATIO, BAFT_JITTER_MIN_US,
BAFT_JITTER_MAX_US. Set both jitter values to 0 for padding-only operation.
The pure `baft config stealth-pro --file existing.yaml` command emits validated
JSON (also valid YAML) and leaves its input unchanged. It refuses configs without
a pre-provisioned `noise` section. Distribution/padding/jitter flags are in help.

Rollback: restore the saved config and prior binary on both ends. Disabling
shaping on only one end intentionally fails the Noise handshake.

## Falsification and remaining gates

See `reports/HOOSHA-R3.1.md` and raw logs. Regression gates cover 1000 records
per law, deviation, empirical length/byte entropy, lag 1..16 autocorrelation,
discrete KS against the selected law, stuck/bucket/periodic negative controls,
malformed/truncated records, entropy-source failure, cancellation, concurrency,
ciphertext tampering, mode mismatch, HTTP probes, real runtime transfer,
half-close, hash, slow receivers, bounded memory and cleanup.

ECRL generated differential and atomicity tests compare the reference model and
existing engine. They do not turn Stage-D replay into a production capability.
No claim of global absence of bugs follows from these finite tests.

Local codec benchmark, 16400-byte ciphertext, io.Discard, 100 ms x 3:
- disabled: 2.31–2.41 us/record;
- padding only: 4.93–5.16 us/record;
- padding + default jitter: 1.06–1.10 ms/record (roughly 15 MB/s synthetic rate).

The timer oversleeps relative to requested microseconds. These are codec costs,
not public-network throughput. Default jitter therefore fails a claim of
"no throughput impact". No busy-spin timing workaround is used. Production
performance acceptance remains OPEN pending a defined budget, timer strategy,
real-path captures and the formal 60 s x 5 benchmark campaign.

Detectability acceptance also remains OPEN: collect consented HTTPS and BAFT
packet traces on representative paths, use held-out workloads, compare lengths,
PIT, joint/directional/burst distributions, and report classifier TPR at a fixed
FPR alongside useful throughput, overhead and p95/p99 latency. Synthetic entropy
alone is not acceptance evidence for resistance to filtering in Iran.
