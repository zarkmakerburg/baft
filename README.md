<div dir="rtl" align="right" lang="fa">

<p align="center"><img src="docs/assets/baft-logo.png" alt="BAFT" width="220"></p>

> © 2026 BAFT Project. همهٔ حقوق محفوظ است. این مخزن فقط برای مشاهده عمومی است و هیچ مجوز استفاده‌ای داده نشده است؛ [COPYRIGHT](COPYRIGHT) را ببینید.

# BAFT — بافت

> **زبان:** فارسی | [English](README.en.md)

[![ci](https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml)
[![stagec-soak](https://github.com/zarkmakerburg/baft/actions/workflows/stagec-soak.yml/badge.svg?branch=main)](https://github.com/zarkmakerburg/baft/actions/workflows/stagec-soak.yml)
[![vulncheck](https://github.com/zarkmakerburg/baft/actions/workflows/vulncheck.yml/badge.svg)](https://github.com/zarkmakerburg/baft/actions/workflows/vulncheck.yml)

BAFT جریان‌های TCP احرازشده را بین دو نود تحت کنترل اپراتور، روی یک Carrier چندگانه و دارای کنترل جریان (HTTP/2 روی TLS 1.3) منتقل می‌کند. یک سرویس محلی روی نود **IR** به یک سرویس ثابت و از پیش مجاز پشت نود **EX** نگاشت می‌شود و peer هرگز مقصد را تعیین نمی‌کند. یک صفحهٔ کنترل (**BCC**) سرورها را ثبت می‌کند، تونل را روی هر دو سر می‌سازد، سلامتش را بررسی می‌کند و در صورت خطا هر دو سر را به حالت قبل برمی‌گرداند. ریلیزها قبل از نصب هر چیزی امضا و تأیید می‌شوند.

**وضعیت: نرم‌افزار پژوهشی است و آمادهٔ تولید اعلام نشده.** بخش‌های [وضعیت](#وضعیت) و [محدودیت‌ها](#محدودیتها) را ببینید.

## فهرست

[وضعیت](#وضعیت) · [معماری](#معماری) · [اجزا](#اجزا) · [ویژگی‌های امنیتی](#ویژگیهای-امنیتی) · [نصب](#نصب) · [کار با BCC](#کار-با-bcc) · [ساخت و آزمون](#ساخت-و-آزمون) · [راستی‌آزمایی در CI](#راستیآزمایی-در-ci) · [ساختار مخزن](#ساختار-مخزن) · [مستندات](#مستندات) · [محدودیت‌ها](#محدودیتها) · [مجوز](#مجوز)

## وضعیت

| بخش | وضعیت |
|---|---|
| صفحهٔ داده، مرحلهٔ A–B | برای دامنهٔ تعریف‌شده کامل است: قراردادهای پروتکل، parser، پیکربندی سخت‌گیرانه، Carrier روی HTTP/2 + TLS 1.3، peerهای احرازشده، Route ثابت، معنای Flow، ابطال فعال و آزمون درستی ۱ GiB دوطرفه (COR-01). |
| صفحهٔ داده، مرحلهٔ C | allocator، backpressure، زمان‌بندی بایتی (PADL) و دفتر دریافت TWRL در مسیر داده‌اند؛ soak تکرارشدهٔ چند Flow / گیرندهٔ کند و نمونهٔ race detector آن سبز است. این نتیجهٔ درستی روی شبکهٔ CI/loopback است، نه benchmark شبکهٔ عمومی. |
| صفحهٔ داده، مرحلهٔ D | جایگزینی Carrier در همان process با epoch fencing و replay محدود پیاده و آزموده شده است. resume بعد از restart فرایند یا ریبوت ماشین **پیاده نشده**. |
| صفحهٔ کنترل Launch-1 | روی `main` تحویل شده: ریلیز امضاشده، نصب‌کنندهٔ تأییدکنندهٔ ریلیز و `baft status/doctor/logs`، دسترسی BCC و state با SQLite، jobهای امضاشدهٔ agent، SSH bootstrap، سازندهٔ تونل با rollback و تست انتها‌به‌انتها روی systemd. [22](docs/fa/22-launch-1-roadmap.md) را ببینید. |
| آخرین ریلیز | [`v0.1.0`](https://github.com/zarkmakerburg/baft/releases/tag/v0.1.0): امضاشده؛ شناسهٔ کلید root: `98741d81da746d2e435302f90569911d`. |

شواهد در [STATUS.md](STATUS.md)، [TEST-RESULTS.md](TEST-RESULTS.md)، [PLAN.md](PLAN.md) و [KNOWN-LIMITATIONS.md](KNOWN-LIMITATIONS.md) ثبت می‌شود. هر ادعا در این مخزن باید از اجرای واقعی آمده باشد.

## معماری

<div dir="ltr" align="left">

```text
local application
      │
      ▼
127.0.0.1:1443 on IR                    one Route = one fixed target
      │                                 OPEN carries a route_id, never a host
      ▼
BAFT IR (dialer)
      │   Shard 0..N: independent TCP + TLS 1.3 + HTTP/2 Carriers
      ▼
BAFT EX (listener)
      │
      ▼
fixed authorized target, e.g. 127.0.0.1:2443
```

</div>

داخل Carrier: `HELLO → HELLO_ACK → READY`، سپس برای هر Flow این فریم‌ها: `OPEN / OPEN_OK`، `DATA`، `ACK`، `WINDOW` (اعتبار)، `FIN / FIN_ACK` و `RESET`. هر Shard انتقال مستقل خودش را دارد و Shardها بی‌صدا روی یک اتصال TCP جمع نمی‌شوند. حافظه با یک allocator سراسری و backpressure محدود می‌شود و DATA با deficit round robin بایتی همراه با aging فشار زمان‌بندی می‌شود.

دو حالت احراز هویت Carrier وجود دارد، هر دو روی TLS 1.3:

- **mTLS** (پیش‌فرض وقتی پیکربندی بخش `noise` ندارد): زنجیرهٔ گواهی، نام میزبان، اعتبار زمانی و EKU بررسی می‌شود و هویت نود از URI SAN تأییدشده می‌آید.
- **Noise IK با کلید pin‌شده** (آنچه جفت‌سازی `install.sh` می‌سازد): سرور TLS بیرونی با یک CA محلی اعتبارسنجی می‌شود و هر دو peer با کلیدهای ایستای Noise که pin شده‌اند و هرکدام به یک هویت مجاز نگاشت شده‌اند احراز می‌شوند ([ADR-0007](docs/adr/0007-pinned-noise-morphing.md)).

صفحهٔ کنترل:

<div dir="ltr" align="left">

```text
 operator ──► BCC (secret-path web UI + API, SQLite state, audit log)
                │  signed jobs (Ed25519), pulled over HTTPS
                ▼
         baft-agent on each server ──► baft, baft-pair, systemd
```

</div>

## اجزا

| باینری | نقش |
|---|---|
| `baft` | نود صفحهٔ داده (`run`)، `config validate` و دستورهای عملیاتی `status`، `doctor` و `logs`. |
| `baft-pair` | تولید کلید، PKI بیرونی و جفت‌سازی یک‌بارمصرف که هر دو پیکربندی را می‌نویسد (`ex-code`، `ir-apply`، `ex-accept`). |
| `baft-bcc` | صفحهٔ کنترل: پایش، مالی، audit زنجیره‌ای، بکاپ رمزشده، چرخش توکن و ابطال، jobهای امضاشده، SSH bootstrap، سازندهٔ تونل و دسترسی وب (زیرفرمان‌های `access` و `jobkey`). |
| `baft-agent` | روی هر سرور اجرا می‌شود؛ jobهای امضاشده را می‌گیرد، فقط actionهای مجاز را اجرا می‌کند و فقط به باینری امضاشدهٔ ریلیز به‌روز می‌شود. |
| `baft-release` | امضا و تأیید ریلیز: `keygen`، `certify`، `revoke`، `sign` و `verify`. |
| `baft-bcc-audit-verify` | تأیید آفلاین زنجیرهٔ audit و لنگرهای BCC. |
| `baft-master`، `baft-worker`، `baft-cluster-keygen` | runtime قدیمی‌تر cluster/mesh و ابزار کلید آن. |

## ویژگی‌های امنیتی

صفحهٔ داده

- حداقل TLS 1.3 و بدون بازگشت به متن ساده؛ بررسی زنجیرهٔ گواهی، نام میزبان، اعتبار زمانی و EKU قابل خاموش شدن نیست.
- اعتماد به CA و مجوز Route جدا هستند؛ `node_id` در HELLO به‌تنهایی اعتماد نمی‌سازد.
- peer نمی‌تواند مقصد تعیین کند؛ اندازه و نوع فریم پیش از تخصیص حافظه اعتبارسنجی می‌شود.
- payload کاربر هرگز در لاگ یا support bundle نوشته نمی‌شود.

اعتماد به ریلیز ([23](docs/fa/23-p1a-signed-releases.md))

- envelopeهایی به سبک DSSE. یک **کلید root آفلاین Ed25519** کلید **release** در CI را گواهی می‌کند؛ کلید عمومی root داخل `install.sh` و agent pin شده است. یک **فهرست ابطال** الزامی، دارای انقضا و امضاشده با root در هر نصب بررسی می‌شود.
- نصب‌کننده پیش از اجرای هر چیز دانلودشده، گواهی، فهرست ابطال، امضای manifest، `SHA256SUMS` و هش هر باینری را تأیید می‌کند، downgrade و نسخهٔ دوباره‌تگ‌شده را رد می‌کند و وضعیت را در فایلی متعلق به root می‌نویسد.
- ساخت ریلیز تکرارپذیر است (در CI بررسی می‌شود).

صفحهٔ کنترل ([24](docs/fa/24-p1c-bcc-access.md)، [25](docs/fa/25-p1d-agent.md)، [26](docs/fa/26-p1d-ssh-bootstrap.md)، [27](docs/fa/27-p1e-tunnel-builder.md))

- دسترسی وب BCC: مسیر مخفی تصادفی، نام کاربری و گذرواژهٔ تصادفی (فقط هش PBKDF2 ذخیره می‌شود)، نشست با محافظت CSRF، محدودیت نرخ و بازتولید فقط از کنسول که همهٔ نشست‌ها را باطل می‌کند.
- هر job را BCC امضا می‌کند و به فهرستی مجاز با الگوی سخت‌گیرانه برای هر پارامتر محدود است که agent دوباره بررسی می‌کند؛ هیچ action برای shell یا فرمان دلخواه وجود ندارد. jobها منقضی می‌شوند (۱ ساعت) و دوبار اجرا نمی‌شوند.
- SSH bootstrap کلید میزبان سرور را pin می‌کند (بدون اعتماد در اولین اتصال)؛ اعتبارنامهٔ SSH فقط در حافظه و فقط در مدت درخواست می‌ماند و هرگز ذخیره، لاگ یا audit نمی‌شود.
- تغییر تونل تراکنشی است: prepare فقط staging می‌کند، commit بکاپ نگه می‌دارد و هر خطا همهٔ نودهای درگیر را برمی‌گرداند. رازهای جفت‌سازی فقط تا لازم بودن یک مرحله در jobها می‌مانند و بعد پاک می‌شوند.
- state در BCC یک پایگاه SQLite نسخه‌دار است و کنش‌های ادمین در لاگ audit ضدتخریب ثبت می‌شود.

[مدل امنیتی](docs/fa/03-security-model.md) را بخوانید.

## نصب

پیش‌نیاز سرور: لینوکس با systemd، `curl`، `openssl`، `python3` و کاربری با `sudo`. Go، git و کامپایلر لازم نیست: نصب‌کننده ریلیز امضاشدهٔ معماری ماشین (`amd64` یا `arm64`) را می‌گیرد، با کلید root پین‌شده و فهرست ابطال جاری تأیید می‌کند و اگر تأیید نشود چیزی نصب نمی‌کند.

<div dir="ltr" align="left">

```bash
# EX (outside server): prints a one-time pairing code
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft --role ex --public-address EX_IP_OR_HOST

# IR (inside server): paste the pairing code, then give the printed reply code back to the EX
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft --role ir
```

</div>

برای ثبت سرور در BCC (و ساخت تونل توسط خود BCC) فقط agent نصب می‌شود:

<div dir="ltr" align="left">

```bash
sudo env BAFT_BCC_JOB_KEY="$(baft-bcc jobkey show)" BAFT_AGENT_TOKEN=NODE_TOKEN \
  bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft \
  --agent-only --bcc-url https://bcc.example.com --node-id ex-1
```

</div>

`BAFT_BCC_JOB_KEY` همان مقداری است که `baft-bcc jobkey show` روی سرور BCC چاپ می‌کند. برای نسخهٔ مشخص `--version vX.Y.Z` اضافه کن. اسکریپت با `bash -c` اجرا می‌شود، نه `| bash`، تا نصب‌کننده بتواند کدها را از ترمینال بخواند.

برای بررسی دستی یک ریلیز، فایل‌هایش را دانلود کن و اجرا کن:

<div dir="ltr" align="left">

```bash
baft-release verify -dir <download dir> -root-pub release/keys/root.pub -revocations release/keys/revocations.json
```

</div>

جزئیات و متغیرهای محیطی: [06](docs/fa/06-running-ir-ex.md) و `bash install.sh --help`.

## کار با BCC

1. `baft-bcc` را اجرا کن (HTTPS یا loopback) و دسترسی وب را با `baft-bcc access init` بساز.
2. سرورها را در داشبورد اضافه کن (**Add a server over SSH**: خواندن کلید میزبان، تأیید اثر انگشت، نصب agent) یا با دستور agent-only بالا ثبت کن.
3. تونل را در داشبورد (**Tunnels**) یا با `POST /api/tunnels {"ex_node":"…","ir_node":"…"}` بساز. BCC هر دو سر را آماده، نصب، بررسی سلامت و نهایی می‌کند؛ در هر خطا یا با `POST /api/tunnels/cancel` هر دو سر به حالت قبل برمی‌گردند.
4. مرحله‌ها را در داشبورد و نتیجه‌ها را در لاگ audit دنبال کن.

روی خود نود با `baft status`، `baft doctor` و `baft logs` وضعیت را ببین.

## ساخت و آزمون

<div dir="ltr" align="left">

```bash
git clone https://github.com/zarkmakerburg/baft.git
cd baft
go build ./...
go vet ./...
go test ./...
go test -race ./...
```

</div>

نسخهٔ Go همان است که در `go.mod` آمده (1.27.1). اعتبارسنجی و اجرای دستی یک نود:

<div dir="ltr" align="left">

```bash
./baft config validate --file configs/example-ir.yaml
./baft run --file /etc/baft/ir.yaml     # or ex.yaml on the EX
```

</div>

## راستی‌آزمایی در CI

| workflow / job | چه چیزی را ثابت می‌کند |
|---|---|
| `ci` · `test` | آزمون‌های واحد و یکپارچه، race detector و دروازه‌های شواهد مرحله‌ای (پایداری، یکپارچگی sync، mesh، telemetry، سخت‌سازی BCC، backup/restore، runtime ‏ECRL). |
| `ci` · `e2e-binaries`, `e2e-install` | ترافیک واقعی IR→EX، ساخته‌شده از سورس و نصب‌شده با `install.sh` روی systemd. |
| `ci` · `e2e-install-release`, `release-dry-run` | نصب از ریلیز امضاشده با کلیدهای یک‌بارمصرف؛ ساخت تکرارپذیر؛ دستکاری، ابطال، replay، downgrade و دوباره‌تگ رد می‌شوند. |
| `ci` · `e2e-agent-enroll`, `e2e-ssh-bootstrap` | ثبت agent و اجرای یک job امضاشده روی systemd؛ SSH bootstrap روی `sshd` واقعی از جمله کلید میزبان اشتباه. |
| `ci` · `e2e-launch1` | Launch-1 روی یک commit: دو سرور ثبت می‌شوند، BCC تونل را با ترافیک واقعی می‌سازد، یک تغییر خراب روی هر دو سر rollback می‌شود و هیچ رازی نمی‌ماند. |
| `stagec-soak`, `step57-recovery-soak` | soakهای تکراری چند Flow، گیرندهٔ کند و recovery. |
| `vulncheck` | `govulncheck` هفتگی و با تغییر وابستگی‌ها. |

## ساختار مخزن

| مسیر | محتوا |
|---|---|
| `cmd/` | باینری‌های بخش [اجزا](#اجزا). |
| `internal/protocol`, `session`, `scheduler`, `resources`, `routes`, `identity`, `config`, `carrier/h2`, `recovery`, `recordshape`, `securityinternal` | صفحهٔ داده: codec ‏BAFT/1، Session و Flow، زمان‌بندی، allocator، Route، هویت، پیکربندی، Carrier، recovery، record shaping، جفت‌سازی و Noise. |
| `internal/bcc`, `agent`, `agentjob`, `sshboot`, `tunnelnode`, `release`, `telemetry` | صفحهٔ کنترل: BCC، agent، قالب job، SSH bootstrap، تراکنش تونل سمت نود، امضای ریلیز، telemetry. |
| `internal/cluster`, `clustersync`, `mesh`, `failover`, `node`, `metrics` | runtime ‏cluster/mesh، failover و metrics. |
| `install.sh`, `scripts/release/` | نصب‌کننده؛ ساخت تکرارپذیر ریلیز و تمرین‌ها. |
| `release/keys/` | کلید عمومی root پین‌شده و فهرست ابطال امضاشدهٔ جاری. |
| `tests/` | `integration`، `correctness` (دروازه‌های بزرگ)، `installer`، `e2e` و `docs`. |
| `docs/en`, `docs/fa`, `docs/adr` | راهنماهای هم‌ساخت و ثبت تصمیم‌های معماری. |

مرزهای وابستگی عمداً رعایت می‌شود: `protocol` مقصد را dial نمی‌کند، `routes` payload سیم را parse نمی‌کند، `scheduler` معنای payload را نمی‌فهمد و `carrier` مجوز Route را تعیین نمی‌کند. [10](docs/fa/10-repository-layout.md) را ببینید.

## مستندات

معماری و پروتکل: [01 مرور](docs/fa/01-overview.md) · [02 معماری](docs/fa/02-architecture.md) · [03 مدل امنیتی](docs/fa/03-security-model.md) · [04 پروتکل BAFT/1](docs/fa/04-protocol-baft1.md) · [05 پیکربندی](docs/fa/05-configuration.md) · [11 واژه‌نامه](docs/fa/11-glossary.md)

عملیات و کیفیت: [06 اجرای IR/EX](docs/fa/06-running-ir-ex.md) · [07 کنترل منابع](docs/fa/07-resource-control.md) · [08 آزمون و CI](docs/fa/08-testing-and-ci.md) · [17 دروازه‌های درستی](docs/fa/17-correctness-gates.md) · [18 soak مرحلهٔ C](docs/fa/18-stage-c-soak.md) · [10 ساختار مخزن](docs/fa/10-repository-layout.md)

صفحهٔ کنترل Launch-1: [22 نقشهٔ راه](docs/fa/22-launch-1-roadmap.md) · [23 ریلیز امضاشده](docs/fa/23-p1a-signed-releases.md) · [24 دسترسی و state در BCC](docs/fa/24-p1c-bcc-access.md) · [25 jobهای agent](docs/fa/25-p1d-agent.md) · [26 SSH bootstrap](docs/fa/26-p1d-ssh-bootstrap.md) · [27 سازندهٔ تونل](docs/fa/27-p1e-tunnel-builder.md)

پژوهش و ثبت طراحی: [09 نقشهٔ راه، مراحل A–H](docs/fa/09-roadmap.md) · [12 روش نوآوری](docs/fa/12-innovation-method.md) · [13 TWRL](docs/fa/13-stage-c-twrl.md) · [14 PADL](docs/fa/14-stage-c-padl.md) · [15 telemetry پایستگی](docs/fa/15-conservation-telemetry.md) · [15D دروازهٔ طراحی ECRL](docs/fa/15-stage-d-ecrl.md) · [16 metricهای پایستگی](docs/fa/16-conservation-metrics.md) · [19 quarantine پایانی](docs/fa/19-terminal-quarantine.md) · [21 record shaping / morphing (آزمایشی، فقط انگلیسی)](docs/en/21-stealth-pro.md) · [ADRها](docs/adr/)

فایل‌های وضعیت: [STATUS](STATUS.md) · [PLAN](PLAN.md) · [BLOCKERS](BLOCKERS.md) · [TEST-RESULTS](TEST-RESULTS.md) · [KNOWN-LIMITATIONS](KNOWN-LIMITATIONS.md) · [قفل وابستگی‌ها](dependency-lock.md) · [ماتریس نوآوری](novelty-matrix.md)

## محدودیت‌ها

- آمادهٔ تولید نیست. هیچ پایلوت واقعی IR↔EX اجرا نشده و ادعایی دربارهٔ throughput عمومی، غیرقابل‌تشخیص بودن یا اتصال تضمین‌شده نمی‌شود.
- resume بعد از restart فرایند یا ریبوت ماشین پیاده نشده؛ recovery فقط در همان process است.
- شنوندهٔ عمومی IR کاربر نهایی را احراز نمی‌کند؛ اگر بیرون از loopback باز شود، امنیت لایهٔ سرویس لازم است.
- record shaping / morphing آزمایشی است و شباهت آماری به HTTPS اثبات نشده.
- مسیرهای H3، relay و Worker خارج از مسیر پیش‌فرض هسته‌اند؛ BCC کیفیت ترافیک را فراتر از زنده بودن سرویس نمی‌سنجد.
- BAFT یک VPN عمومی یا proxy با مقصد دلخواه نیست.

فهرست کامل: [KNOWN-LIMITATIONS.md](KNOWN-LIMITATIONS.md).

## مجوز

همهٔ حقوق برای BAFT Project محفوظ است. مخزن فقط برای مشاهده عمومی است و هیچ مجوزی برای استفاده، کپی، تغییر، توزیع یا اجرای نرم‌افزار داده نشده است. [COPYRIGHT](COPYRIGHT) را ببینید.

</div>
