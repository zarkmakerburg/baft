<div dir="rtl" align="right" lang="fa">

# R1/R2 — Reachability و SecurityInternal

> وضعیت: R2 v0.1 آزمایشی  
> Stage D: متوقف در Reconcile/Epoch Commit  
> شواهد Noise: GitHub Actions run `36352142629`

## هدف

در v0.1 مسیر امنیت داخلی از outer TLS جدا شده است تا termination شدن TLS توسط یک intermediary لزوماً به معنی مشاهده‌ی payload اصلی BAFT نباشد.

<div dir="ltr" align="left">

```text
BAFT application bytes
        |
        v
SecurityInternal (Noise IK / IKpsk0 enrollment)
        |
        v
HTTP/2 stream
        |
        v
outer TLS
        |
        v
intermediary / origin
```

</div>

## SecurityInternal

پیاده‌سازی از `github.com/flynn/noise v1.1.0` استفاده می‌کند:

- Pattern: `IK`
- DH: X25519
- Cipher: AES-256-GCM
- Hash: SHA-256
- transport records: طول محدود + AEAD record
- nonce توسط `CipherState` مدیریت می‌شود.

رمزنگاری یا KDF سفارشی در BAFT تعریف نشده است.

## Pairing

Pairing code با prefix زیر ساخته می‌شود:

<div dir="ltr" align="left">

```text
BAFTPAIR1:<base64url descriptor>
```

</div>

descriptor شامل dial address، server name، هویت EX، public key استاتیک Noise سمت EX، CA outer TLS، expiry و PSK یک‌بارمصرف است.

Private key هیچ‌یک از طرفین در descriptor قرار نمی‌گیرد.

در enrollment اولیه، `IKpsk0` استفاده می‌شود. responder پس از handshake، static public key احراز‌شده‌ی initiator را به‌دست می‌آورد؛ runtime production باید آن را atomically pin کند و PSK یک‌بارمصرف را حذف کند. اتصال‌های بعدی از IK معمولی با static keyهای pin‌شده استفاده می‌کنند.

## نتیجه integration

در run `36352142629`:

<div dir="ltr" align="left">

```text
TestIKPSK0PairThenPinnedIK
PASS

TestNoiseIKOverHTTP2TerminatingIntermediary
noise_h2_intermediary_ok proto=HTTP/2.0 request_capture=133 response_capture=88
PASS

race detector
PASS

bash -n install.sh
PASS

go build ./cmd/baft-pair
PASS
```

</div>

تست intermediary outer TLS را terminate می‌کند، stream خام داخل HTTP/2 را capture می‌کند و اگر plaintext BAFT در capture دیده شود fail می‌شود.

## Installer v0.1

installer:

- Debian/Ubuntu را تشخیص می‌دهد؛
- amd64/arm64 را تشخیص می‌دهد؛
- Go 1.27.1 را از manifest رسمی با SHA-256 نصب می‌کند؛
- repo یا mirror پیکربندی‌شده را clone می‌کند؛
- تست Noise را پیش از نصب باینری اجرا می‌کند؛
- BAFT و `baft-pair` را با strip flags build می‌کند؛
- system user و systemd hardening می‌سازد؛
- EX pairing code تولید می‌کند؛
- IR pairing descriptor را atomically stage می‌کند.

سرویس فقط enable می‌شود؛ start شدن آن به وجود `/etc/baft/baft.yaml` معتبر و wiring production SecurityInternal وابسته است.

## محدودیت و قانون توقف

SecurityInternal هنوز وارد `node.Runtime` اصلی نشده است. بنابراین هیچ ادعایی درباره‌ی فعال بودن Noise روی تمام ترافیک production BAFT وجود ندارد.

Replay Engine و ادامه Stage D در این فاز شروع نمی‌شوند.

## Stealth / Statistical Morphing

این سند برای packet-size/timing randomization با هدف شکست سامانه‌های تحلیل یا فیلترینگ، الگوریتم اجرایی تعریف نمی‌کند. v0.1 فعلی روی E2E confidentiality، pairing، deployment correctness و تست‌پذیری تمرکز دارد.

</div>
