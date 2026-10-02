<div dir="rtl" align="right" lang="fa">

<div dir="rtl" align="center" lang="fa">

<img src="docs/assets/baft-logo-transparent.png" alt="BAFT" width="190">

# BAFT — بافت

### لایهٔ انتقال امن، احرازشده و کنترل‌پذیر میان نودهای تحت مدیریت یک اپراتور

<p dir="ltr"><strong>Signed Releases · Secure Agent · BCC Control Plane · Native Tunnel Builder · Recovery-aware Data Plane</strong></p>

<p dir="ltr">
  <a href="https://github.com/zarkmakerburg/baft/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/zarkmakerburg/baft?style=for-the-badge&label=release"></a>
  <a href="https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.27.1-00ADD8?style=for-the-badge&logo=go&logoColor=white">
  <img alt="Signed Release" src="https://img.shields.io/badge/release-signed-2ea44f?style=for-the-badge">
  <img alt="License" src="https://img.shields.io/badge/license-All%20Rights%20Reserved-8a6a22?style=for-the-badge">
</p>

[فارسی](README.md) · [English](README.en.md) · [آخرین Release](https://github.com/zarkmakerburg/baft/releases/latest) · [وضعیت پروژه](STATUS.md) · [مستندات](docs/fa/README.md)

</div>

<blockquote dir="rtl" align="right">
<strong>⚠️ وضعیت پروژه:</strong> BAFT هنوز به‌عنوان نرم‌افزار <code>production-ready</code> اعلام نشده است. این مخزن یک پروژهٔ مهندسی/پژوهشی فعال است و هر ادعای عملکرد، بازیابی یا امنیت فقط در محدوده‌ای معتبر است که کد، تست و شواهد CI آن را پشتیبانی کنند.
</blockquote>

<blockquote dir="rtl" align="right">
<strong>© 2026 BAFT Project — همهٔ حقوق محفوظ است.</strong> این مخزن برای مشاهده و بررسی عمومی منتشر شده و هیچ مجوز عمومی برای استفاده، کپی، تغییر، توزیع یا ارائهٔ سرویس داده نشده است. جزئیات: <a href="LICENSE">LICENSE</a> و <a href="COPYRIGHT">COPYRIGHT</a>.
</blockquote>

---

## BAFT چیست؟

**BAFT یک transport fabric کنترل‌شده برای جابه‌جایی جریان‌های TCP میان نودهای تحت کنترل همان اپراتور است.**  
در معماری پایه، نود **IR** اتصال Carrier را آغاز می‌کند و نود **EX** آن را می‌پذیرد؛ اما دادهٔ برنامه در هر دو جهت منتقل می‌شود.

BAFT یک VPN عمومی، reverse proxy مقصد-دلخواه یا جایگزین امنیت سرویس مقصد نیست. هستهٔ طراحی بر این اصل بنا شده است:

> **Peer فقط Route مجاز را درخواست می‌کند؛ مقصد واقعی از پیکربندی محلی و ازپیش‌تأییدشده تعیین می‌شود.**

این مدل سطح اختیار peer را محدود می‌کند و کنترل مسیر، هویت، Release، Agent و تغییرات عملیاتی را در اختیار اپراتور نگه می‌دارد.

---

## در یک نگاه

<table dir="rtl" align="right">
<thead>
<tr>
<th align="right">مؤلفه</th>
<th align="center">وضعیت</th>
<th align="right">توضیح</th>
</tr>
</thead>
<tbody>
<tr><td align="right"><strong>Release</strong></td><td align="center">✅</td><td align="right">نسخهٔ عمومی و امضاشدهٔ <a href="https://github.com/zarkmakerburg/baft/releases/tag/v0.1.0"><code>v0.1.0</code></a></td></tr>
<tr><td align="right"><strong>Signed artifacts</strong></td><td align="center">✅</td><td align="right">باینری‌های <code>amd64</code>/<code>arm64</code> به‌همراه manifest، certificate و SHA256SUMS</td></tr>
<tr><td align="right"><strong>Installer</strong></td><td align="center">✅</td><td align="right">نصب پیش‌فرض از Release امضاشده، بدون نیاز به Go/git روی سرور</td></tr>
<tr><td align="right"><strong>BCC Control Plane</strong></td><td align="center">✅</td><td align="right">دسترسی وب امن، session، CSRF، rate limit، SQLite، audit و monitoring</td></tr>
<tr><td align="right"><strong>Secure Agent</strong></td><td align="center">✅</td><td align="right">Pull-based، job امضاشده، allowlist و بدون shell دلخواه</td></tr>
<tr><td align="right"><strong>SSH Bootstrap</strong></td><td align="center">✅</td><td align="right">host-key pinning و عدم نگه‌داری credential پس از enrollment</td></tr>
<tr><td align="right"><strong>Native Tunnel Builder</strong></td><td align="center">✅</td><td align="right"><code>plan → validate → prepare → commit → health → rollback</code></td></tr>
<tr><td align="right"><strong>Two-sided rollback</strong></td><td align="center">✅</td><td align="right">در failure، هر دو سمت به وضعیت قبل برمی‌گردند</td></tr>
<tr><td align="right"><strong>Launch-1 E2E</strong></td><td align="center">✅</td><td align="right">اجرای انتهابه‌انتها در CI روی مسیر واقعی محصول</td></tr>
<tr><td align="right"><strong>ECRL same-process recovery</strong></td><td align="center">✅ محدود</td><td align="right">تعویض Carrier و bounded replay در همان process/session</td></tr>
<tr><td align="right"><strong>Process restart / reboot resume</strong></td><td align="center">⚠️</td><td align="right">هنوز پیاده‌سازی و ادعا نشده است</td></tr>
<tr><td align="right"><strong>Production readiness</strong></td><td align="center">⚠️</td><td align="right">هنوز اعلام نشده است</td></tr>
</tbody>
</table>

---

## معماری سیستم

BAFT دو مسیر را عمداً از هم جدا می‌کند: **Control Plane** برای مدیریت، و **Data Plane** برای عبور داده.

<div dir="ltr" align="left">

```text
                         CONTROL PLANE

                        Operator / Admin
                               │
                     HTTPS + Session + CSRF
                               │
                               ▼
                    ┌──────────────────────┐
                    │       BAFT BCC       │
                    │ SQLite / Audit       │
                    │ Monitoring / Finance │
                    └──────────┬───────────┘
                               │
                 signed jobs   │   signed jobs
                     ┌─────────┴─────────┐
                     ▼                   ▼
              ┌─────────────┐     ┌─────────────┐
              │ baft-agent  │     │ baft-agent  │
              │     IR      │     │     EX      │
              └──────┬──────┘     └──────┬──────┘
                     │                   │
                     └──── Tunnel Builder┘
                          plan / prepare
                          commit / health
                          rollback


                           DATA PLANE

  Local Application
         │
         ▼
  IR Route Listener
         │
         ▼
      BAFT IR
         ║
         ║  authenticated Carrier
         ║  bounded flow control
         ║  recovery-aware session
         ▼
      BAFT EX
         │
         ▼
  Fixed Authorized Route
         │
         ▼
   Target Service
```

</div>

### اصل Route ثابت

نود مقابل اجازه ندارد یک host/port دلخواه را داخل فریم شبکه تعیین کند. IR یک **Route ID** می‌فرستد و EX مقصد واقعی را از پیکربندی محلی و allowlist خودش می‌خواند.

---

## چرا معماری BAFT متفاوت است؟

### ۱) Trust محدود و صریح

- اعتماد به CA با مجوز دسترسی به Route یکی نیست.
- هویت Node از credential تأییدشده استخراج می‌شود.
- peer allowlist و Route allowlist مستقل‌اند.
- مقصد دلخواه از peer پذیرفته نمی‌شود.

### ۲) Release قابل‌راستی‌آزمایی

Release پیش از نصب با زنجیرهٔ اعتماد pin‌شده بررسی می‌شود:

- Root public key ثابت؛
- release-key certificate؛
- revocation list امضاشده و منقضی‌نشده؛
- manifest امضاشده؛
- SHA-256 تمام artifactها؛
- anti-downgrade state؛
- جلوگیری از re-tag شدن همان version روی commit متفاوت.

### ۳) Agent بدون shell آزاد

`baft-agent` فقط actionهای ازپیش‌تعریف‌شده را اجرا می‌کند. jobها توسط BCC امضا می‌شوند و Agent آن‌ها را با کلید pin‌شده بررسی می‌کند. مسیر اجرای فرمان آزاد یا shell دلخواه در مدل Agent وجود ندارد.

### ۴) Bootstrap با تأیید هویت SSH

BCC قبل از نصب Agent، host key سرور را می‌خواند و fingerprint باید توسط اپراتور تأیید شود. password/private key فقط برای همان bootstrap استفاده می‌شود و در state یا audit ذخیره نمی‌شود.

### ۵) Tunnel Builder تراکنشی

ساخت تونل یک تغییر یک‌مرحله‌ای نیست:

<div dir="ltr" align="left">

```text
PLAN
  ↓
VALIDATE
  ↓
PREPARE EX + PREPARE IR
  ↓
COMMIT
  ↓
HEALTH CHECK
  ├── success ──► ACTIVE
  └── failure ──► ROLLBACK BOTH SIDES
```

</div>

### ۶) Recovery با مرز ادعای روشن

در Step 5.7، BAFT تعویض Carrier را برای **Session زنده در همان process** با epoch fencing، bounded replay و commit safety پشتیبانی و تست می‌کند.  
اما recovery بعد از restart پردازه یا reboot ماشین هنوز جزو قابلیت‌های تأییدشده نیست.

---

## Release فعلی — v0.1.0

Release رسمی فعلی:

### [BAFT v0.1.0](https://github.com/zarkmakerburg/baft/releases/tag/v0.1.0)

این Release شامل **۱۱ artifact امضاشده** است:

<table dir="rtl" align="right">
<thead><tr><th align="right">فایل</th><th align="right">معماری / نقش</th></tr></thead>
<tbody>
<tr><td dir="ltr" align="left"><code>baft-linux-amd64</code></td><td align="right">Runtime اصلی — amd64</td></tr>
<tr><td dir="ltr" align="left"><code>baft-linux-arm64</code></td><td align="right">Runtime اصلی — arm64</td></tr>
<tr><td dir="ltr" align="left"><code>baft-pair-linux-amd64</code></td><td align="right">Pairing — amd64</td></tr>
<tr><td dir="ltr" align="left"><code>baft-pair-linux-arm64</code></td><td align="right">Pairing — arm64</td></tr>
<tr><td dir="ltr" align="left"><code>baft-bcc-linux-amd64</code></td><td align="right">Control Plane — amd64</td></tr>
<tr><td dir="ltr" align="left"><code>baft-bcc-linux-arm64</code></td><td align="right">Control Plane — arm64</td></tr>
<tr><td dir="ltr" align="left"><code>baft-agent-linux-amd64</code></td><td align="right">Secure Agent — amd64</td></tr>
<tr><td dir="ltr" align="left"><code>baft-agent-linux-arm64</code></td><td align="right">Secure Agent — arm64</td></tr>
<tr><td dir="ltr" align="left"><code>manifest.json</code></td><td align="right">manifest امضاشدهٔ Release</td></tr>
<tr><td dir="ltr" align="left"><code>release-key.cert.json</code></td><td align="right">certificate کلید Release</td></tr>
<tr><td dir="ltr" align="left"><code>SHA256SUMS</code></td><td align="right">checksum تمام artifactها</td></tr>
</tbody>
</table>

دو Source archive استاندارد GitHub نیز در UI نمایش داده می‌شوند؛ بنابراین صفحهٔ Release مجموعاً ۱۳ مورد قابل دانلود نشان می‌دهد.

---

## نصب سریع

Installer به‌صورت پیش‌فرض **Release امضاشده را دانلود و verify می‌کند**. برای نصب عادی روی سرور نیازی به Go، git یا compiler نیست.

### ۱. دریافت و بررسی installer

<div dir="ltr" align="left">

```bash
curl -fsSLO https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh
less install.sh
chmod +x install.sh
```

</div>

### ۲. نصب EX

<div dir="ltr" align="left">

```bash
sudo bash install.sh   --role ex   --public-address EX_HOST_OR_IP   --version v0.1.0
```

</div>

EX پس از آماده‌سازی، یک کد یک‌بارمصرف با prefix زیر تولید می‌کند:

<div dir="ltr" align="left">

```text
BAFTPAIR1:...
```

</div>

### ۳. نصب IR

<div dir="ltr" align="left">

```bash
sudo bash install.sh   --role ir   --pairing-code 'BAFTPAIR1:...'   --version v0.1.0
```

</div>

IR یک پاسخ `BAFTREPLY1:...` تولید می‌کند که باید روی EX پذیرفته شود تا pairing کامل و configهای pin‌شده فعال شوند.

> مسیر کامل نصب و Pairing: [ساخت و اجرای IR و EX](docs/fa/06-running-ir-ex.md)

---

## CLI اپراتور

پس از نصب، سه فرمان فقط‌خواندنی برای تشخیص وضعیت وجود دارد:

<div dir="ltr" align="left">

```bash
sudo baft status
sudo baft doctor
sudo baft logs -n 200 -f
```

</div>

- **status**: نسخه، Release نصب‌شده، نقش، peer، Routeها، service state، Flowها و recovery counterها.
- **doctor**: بررسی config، revocation، permission کلیدها، service، Release، metrics، reachability و تنظیمات شبکه.
- **logs**: دسترسی کنترل‌شده به journal سرویس.

---

## BCC — مرکز کنترل BAFT

BCC برای مدیریت fleet و tunnelها طراحی شده است و state عملیاتی را در SQLite نسخه‌دار نگه می‌دارد.

### قابلیت‌های اصلی

- مسیر مخفی تصادفی برای رابط وب؛
- username و password تولیدشده از console؛
- ذخیرهٔ hash رمز، نه plaintext؛
- session با `HttpOnly` و `SameSite=Strict`؛
- CSRF protection؛
- rate limit و lock موقت پس از login failure؛
- HTTPS یا loopback-only HTTP؛
- audit log؛
- monitoring و history؛
- finance ledger؛
- token rotation و node revocation؛
- SSH bootstrap؛
- Native Tunnel Builder.

### مدیریت credential فقط از console

<div dir="ltr" align="left">

```bash
sudo baft-bcc access init
sudo baft-bcc access show
sudo baft-bcc access regenerate
```

</div>

Regenerate مسیر، username و password را با هم عوض می‌کند و sessionهای قبلی را باطل می‌کند.

---

## امنیت Release و نصب

Installer قبل از اجرای باینری دانلودشده این موارد را verify می‌کند:

<div dir="ltr" align="left">

```text
Pinned Root
    │
    ▼
Release Key Certificate
    │
    ├── not revoked
    ├── valid time window
    ▼
Signed Manifest
    │
    ├── version
    ├── exact commit
    ├── artifact sizes
    └── SHA-256
    ▼
SHA256SUMS
    │
    ▼
Downloaded Binaries
```

</div>

اگر verify شکست بخورد، نصب انجام نمی‌شود.

جزئیات کامل: [Release امضاشده و anti-rollback](docs/fa/23-p1a-signed-releases.md)

---

## کیفیت و گیت‌های مهندسی

BAFT فقط به تست unit محدود نیست. مسیر CI فعلی شامل مجموعه‌ای از گیت‌های correctness و integration است:

- unit / integration tests؛
- race detector؛
- `go vet`؛
- protocol fuzz smoke؛
- installer E2E؛
- signed-release installer E2E؛
- Agent enrollment E2E؛
- SSH bootstrap روی `sshd` واقعی؛
- Launch-1 end-to-end؛
- rollback در failure؛
- COR-01 انتقال دوطرفهٔ 1 GiB؛
- Stage-C multi-flow / slow-receiver soak؛
- recovery-specific soak.

> نتیجهٔ این تست‌ها **معادل benchmark اینترنت عمومی یا production readiness نیست**؛ آن‌ها correctness و رفتار سیستم را در محیط‌های تعریف‌شده اثبات می‌کنند.

---

## وضعیت Recovery

### تأییدشده در محدودهٔ فعلی

- Carrier replacement در همان process؛
- epoch fencing در همان process؛
- bounded replay؛
- حفظ Flow فعال؛
- FIN / FIN_ACK recovery؛
- multi-flow continuity؛
- commit validation پیش از authority change؛
- idempotent commit identity؛
- monotonic generation readiness؛
- telemetry/finance continuity بدون double-count.

### هنوز خارج از محدودهٔ تأییدشده

- process-restart resume؛
- machine-reboot resume؛
- durable ECRL session snapshots؛
- endpoint-pool/relay production path؛
- benchmark عمومی رسمی؛
- ادعای تضمین سرعت، تضمین اتصال یا تشخیص‌ناپذیری.

---

## نقشهٔ مستندات

<table dir="rtl" align="right">
<thead><tr><th align="right">موضوع</th><th align="right">فارسی</th><th align="left" dir="ltr">English</th></tr></thead>
<tbody>
<tr><td align="right">معرفی پروژه</td><td align="right"><a href="docs/fa/01-overview.md">01-overview</a></td><td align="left" dir="ltr"><a href="docs/en/01-overview.md">Overview</a></td></tr>
<tr><td align="right">معماری</td><td align="right"><a href="docs/fa/02-architecture.md">02-architecture</a></td><td align="left" dir="ltr"><a href="docs/en/02-architecture.md">Architecture</a></td></tr>
<tr><td align="right">مدل امنیت</td><td align="right"><a href="docs/fa/03-security-model.md">03-security-model</a></td><td align="left" dir="ltr"><a href="docs/en/03-security-model.md">Security Model</a></td></tr>
<tr><td align="right">پروتکل BAFT/1</td><td align="right"><a href="docs/fa/04-protocol-baft1.md">04-protocol-baft1</a></td><td align="left" dir="ltr"><a href="docs/en/04-protocol-baft1.md">Protocol</a></td></tr>
<tr><td align="right">پیکربندی</td><td align="right"><a href="docs/fa/05-configuration.md">05-configuration</a></td><td align="left" dir="ltr"><a href="docs/en/05-configuration.md">Configuration</a></td></tr>
<tr><td align="right">نصب و اجرا</td><td align="right"><a href="docs/fa/06-running-ir-ex.md">06-running-ir-ex</a></td><td align="left" dir="ltr"><a href="docs/en/06-running-ir-ex.md">Running IR/EX</a></td></tr>
<tr><td align="right">تست و CI</td><td align="right"><a href="docs/fa/08-testing-and-ci.md">08-testing-and-ci</a></td><td align="left" dir="ltr"><a href="docs/en/08-testing-and-ci.md">Testing &amp; CI</a></td></tr>
<tr><td align="right">ECRL / Stage D</td><td align="right"><a href="docs/fa/20-stage-d-ecrl.md">20-stage-d-ecrl</a></td><td align="left" dir="ltr"><a href="docs/en/20-stage-d-ecrl.md">Stage D ECRL</a></td></tr>
<tr><td align="right">Launch-1</td><td align="right"><a href="docs/fa/22-launch-1-roadmap.md">22-launch-1-roadmap</a></td><td align="left" dir="ltr"><a href="docs/en/22-launch-1-roadmap.md">Launch-1 Roadmap</a></td></tr>
<tr><td align="right">Signed Release</td><td align="right"><a href="docs/fa/23-p1a-signed-releases.md">23-p1a-signed-releases</a></td><td align="left" dir="ltr"><a href="docs/en/23-p1a-signed-releases.md">Signed Releases</a></td></tr>
<tr><td align="right">BCC Access</td><td align="right"><a href="docs/fa/24-p1c-bcc-access.md">24-p1c-bcc-access</a></td><td align="left" dir="ltr"><a href="docs/en/24-p1c-bcc-access.md">BCC Access</a></td></tr>
<tr><td align="right">Secure Agent</td><td align="right"><a href="docs/fa/25-p1d-agent.md">25-p1d-agent</a></td><td align="left" dir="ltr"><a href="docs/en/25-p1d-agent.md">Secure Agent</a></td></tr>
<tr><td align="right">SSH Bootstrap</td><td align="right"><a href="docs/fa/26-p1d-ssh-bootstrap.md">26-p1d-ssh-bootstrap</a></td><td align="left" dir="ltr"><a href="docs/en/26-p1d-ssh-bootstrap.md">SSH Bootstrap</a></td></tr>
<tr><td align="right">Tunnel Builder</td><td align="right"><a href="docs/fa/27-p1e-tunnel-builder.md">27-p1e-tunnel-builder</a></td><td align="left" dir="ltr"><a href="docs/en/27-p1e-tunnel-builder.md">Tunnel Builder</a></td></tr>
</tbody>
</table>

### اسناد وضعیت

- [STATUS.md](STATUS.md) — وضعیت واقعی پیاده‌سازی
- [PLAN.md](PLAN.md) — برنامه و گیت‌ها
- [TEST-RESULTS.md](TEST-RESULTS.md) — شواهد تست
- [KNOWN-LIMITATIONS.md](KNOWN-LIMITATIONS.md) — محدودیت‌های شناخته‌شده
- [BLOCKERS.md](BLOCKERS.md) — blockerهای ثبت‌شده
- [docs/adr](docs/adr) — تصمیم‌های معماری
- [docs/protocol](docs/protocol) — protocol notes و golden vectors

---

## ساختار مخزن

<div dir="ltr" align="left">

```text
cmd/                 command-line binaries
internal/            runtime, protocol, BCC, agent, recovery
configs/             example configurations
tests/               integration, correctness and E2E tests
scripts/             release/build/test automation
release/             root trust material and revocation metadata
docs/fa/             Persian documentation
docs/en/             English documentation
reports/             engineering/research reports
.github/workflows/   CI, soak, release and security workflows
```

</div>

---

## توسعه از سورس

برای توسعه، نه نصب production-like:

<div dir="ltr" align="left">

```bash
git clone https://github.com/zarkmakerburg/baft.git
cd baft

go build ./cmd/baft
go test ./...
go test -race ./...
go vet ./...
```

</div>

نسخهٔ Go مرجع پروژه از `go.mod` خوانده می‌شود و در حال حاضر **Go 1.27.1** است.

---

## چیزی که BAFT نیست

BAFT در وضعیت فعلی:

- VPN عمومی چندکاربره نیست؛
- مقصد دلخواه را از peer قبول نمی‌کند؛
- جایگزین ACL/PKI سرویس مقصد نیست؛
- کیفیت شبکه عمومی را تضمین نمی‌کند؛
- «همه‌جا قابل اتصال» یا «غیرقابل‌تشخیص» بودن را ادعا نمی‌کند؛
- process-restart recovery را هنوز ارائه نمی‌کند؛
- production-ready اعلام نشده است.

---

## حقوق استفاده

این مخزن **source-visible** است، نه open-source دارای مجوز آزاد.

مشاهده و بررسی عمومی مخزن مجاز است، اما استفاده، اجرا، کپی، تغییر، توزیع، ساخت سرویس یا استفاده از نام و لوگوی BAFT بدون اجازهٔ کتبی قبلی مجاز نیست.

[متن کامل LICENSE](LICENSE) · [COPYRIGHT](COPYRIGHT)

---

<div dir="rtl" align="center" lang="fa">

### BAFT

**Bounded · Authenticated · Fail-safe · Transactional**

پروژه‌ای برای ساخت یک transport قابل‌اندازه‌گیری، قابل‌آزمون و قابل‌کنترل — بدون بزرگ‌تر کردن ادعا از شواهد.

</div>

</div>
