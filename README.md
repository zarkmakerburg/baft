<div dir="rtl" align="right" lang="fa">

<p align="center"><img src="docs/assets/baft-logo.png" alt="BAFT" width="220"></p>

> © 2026 BAFT Project. همهٔ حقوق محفوظ است. این مخزن فقط برای مشاهده عمومی است و هیچ مجوز استفاده‌ای داده نشده است؛ [COPYRIGHT](COPYRIGHT) را ببینید.

# BAFT — بافت

> **زبان:** فارسی | [English](README.en.md)

[![ci](https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml)
[![stagec-soak](https://github.com/zarkmakerburg/baft/actions/workflows/stagec-soak.yml/badge.svg?branch=main)](https://github.com/zarkmakerburg/baft/actions/workflows/stagec-soak.yml)
[![vulncheck](https://github.com/zarkmakerburg/baft/actions/workflows/vulncheck.yml/badge.svg)](https://github.com/zarkmakerburg/baft/actions/workflows/vulncheck.yml)

BAFT جریان‌های TCP احرازشده را بین دو نود تحت کنترل اپراتور، روی یک Carrier چندگانه و دارای کنترل جریان (HTTP/2 روی TLS 1.3) منتقل می‌کند. یک سرویس محلی روی نود **IR** به یک مقصد ثابت و از پیش مجاز پشت نود **EX** نگاشت می‌شود و peer هرگز مقصد را تعیین نمی‌کند. صفحهٔ کنترل **BCC** سرورها را ثبت می‌کند، تونل را روی هر دو سر می‌سازد، سلامتش را می‌سنجد و در صورت خطا هر دو سر را برمی‌گرداند. ریلیزها پیش از نصب هر چیز امضا و تأیید می‌شوند.

**نرم‌افزار پژوهشی است و آمادهٔ تولید اعلام نشده.**

## وضعیت

| بخش | وضعیت |
|---|---|
| صفحهٔ داده A–B | برای دامنهٔ تعریف‌شده کامل است: پروتکل، پیکربندی سخت‌گیرانه، Carrier، peerهای احرازشده، Route ثابت، Flow، ابطال فعال و آزمون درستی ۱ GiB دوطرفه (COR-01). |
| صفحهٔ داده C | allocator، backpressure، زمان‌بندی بایتی (PADL) و دفتر دریافت (TWRL) در مسیر داده‌اند؛ soak تکرارشدهٔ چند Flow / گیرندهٔ کند و نمونهٔ race روی شبکهٔ CI/loopback سبز است. benchmark شبکهٔ عمومی نیست. |
| صفحهٔ داده D | جایگزینی Carrier در همان process با epoch fencing و replay محدود. resume بعد از restart یا ریبوت **پیاده نشده**. |
| صفحهٔ کنترل Launch-1 | روی `main`: ریلیز امضاشده، نصب‌کنندهٔ تأییدکننده، دسترسی BCC و state با SQLite، jobهای امضاشدهٔ agent، SSH bootstrap، سازندهٔ تونل با rollback و تست انتها‌به‌انتها روی systemd ([22](docs/fa/22-launch-1-roadmap.md)). |
| ریلیز | [`v0.1.0`](https://github.com/zarkmakerburg/baft/releases/tag/v0.1.0)، امضاشده؛ شناسهٔ کلید root: `98741d81da746d2e435302f90569911d`. |

شواهد: [STATUS](STATUS.md) · [TEST-RESULTS](TEST-RESULTS.md) · [KNOWN-LIMITATIONS](KNOWN-LIMITATIONS.md).

## معماری

<div dir="ltr" align="left">

```text
application ─► 127.0.0.1:1443 (IR) ─► BAFT IR ══ Shard 0..N: TCP + TLS 1.3 + HTTP/2 ══► BAFT EX ─► fixed target (EX)
                                      OPEN carries a route_id, never a host

operator ─► BCC (web UI + API, SQLite, audit) ── signed jobs, pulled over HTTPS ──► baft-agent ─► baft · baft-pair · systemd
```

</div>

در هر Carrier: `HELLO → HELLO_ACK → READY`؛ در هر Flow: `OPEN/OPEN_OK`، `DATA`، `ACK`، `WINDOW`، `FIN/FIN_ACK` و `RESET`. هر Shard انتقال مستقل خودش را دارد و حافظه با یک allocator سراسری و backpressure محدود می‌شود.

| احراز هویت Carrier | کی | سازوکار |
|---|---|---|
| mTLS | پیش‌فرض (بدون بخش `noise`) | زنجیرهٔ گواهی، نام میزبان، اعتبار زمانی و EKU؛ هویت از URI SAN تأییدشده. |
| Noise IK با کلید pin‌شده | ساختهٔ جفت‌سازی `install.sh` | TLS بیرونی با CA محلی اعتبارسنجی می‌شود؛ peerها با کلید ایستای Noise که pin شده و هرکدام به یک هویت نگاشت شده احراز می‌شوند ([ADR-0007](docs/adr/0007-pinned-noise-morphing.md)). |

## اجزا

| باینری | نقش |
|---|---|
| `baft` | نود صفحهٔ داده (`run`)، `config validate`، `status`، `doctor`، `logs`. |
| `baft-pair` | کلیدها، PKI بیرونی و جفت‌سازی یک‌بارمصرفی که هر دو پیکربندی را می‌نویسد. |
| `baft-bcc` | صفحهٔ کنترل: پایش، مالی، audit، بکاپ، jobهای امضاشده، SSH bootstrap، سازندهٔ تونل و دسترسی وب. |
| `baft-agent` | agent هر سرور: jobهای امضاشدهٔ مجاز؛ فقط به باینری امضاشدهٔ ریلیز به‌روز می‌شود. |
| `baft-release` | `keygen`، `certify`، `revoke`، `sign`، `verify`. |
| `baft-bcc-audit-verify` | تأیید آفلاین زنجیرهٔ audit و لنگرها. |
| `baft-master`، `baft-worker`، `baft-cluster-keygen` | runtime و ابزار کلید قدیمی‌تر cluster/mesh. |

## امنیت

| ویژگی | سازوکار | مرجع |
|---|---|---|
| بدون متن ساده و بدون peer تأییدنشده | حداقل TLS 1.3؛ بررسی زنجیره، نام میزبان، اعتبار و EKU خاموش‌شدنی نیست؛ `node_id` در HELLO به‌تنهایی اعتماد نمی‌سازد. | [03](docs/fa/03-security-model.md) |
| بدون مقصد انتخابی peer | Route یک `route_id` را به مقصد ثابت می‌برد؛ فریم‌ها پیش از تخصیص حافظه اعتبارسنجی می‌شوند. | [04](docs/fa/04-protocol-baft1.md) |
| ریلیز معتبر | root آفلاین Ed25519 کلید release را گواهی می‌کند؛ کلید عمومی root در `install.sh` و agent pin است؛ فهرست ابطال الزامی و دارای انقضا؛ manifest، `SHA256SUMS` و هش هر باینری پیش از استفاده تأیید می‌شود؛ بدون downgrade و دوباره‌تگ؛ ساخت تکرارپذیر. | [23](docs/fa/23-p1a-signed-releases.md) |
| کنسول محافظت‌شده | مسیر مخفی، نام کاربری و گذرواژهٔ تصادفی (فقط هش PBKDF2)، نشست با CSRF، محدودیت نرخ، بازتولید فقط از کنسول که همهٔ نشست‌ها را باطل می‌کند. | [24](docs/fa/24-p1c-bcc-access.md) |
| agent محدود | jobها را BCC امضا می‌کند؛ actionهای مجاز با الگوی سخت‌گیرانهٔ پارامتر، انقضای ۱ ساعته، بدون اجرای دوباره؛ بدون shell یا فرمان دلخواه. | [25](docs/fa/25-p1d-agent.md) |
| bootstrap ایمن | کلید میزبان SSH pin می‌شود (بدون اعتماد در اولین اتصال)؛ اعتبارنامه فقط در حافظه است و هرگز ذخیره، لاگ یا audit نمی‌شود. | [26](docs/fa/26-p1d-ssh-bootstrap.md) |
| تونل برگشت‌پذیر | prepare فقط staging است، commit بکاپ نگه می‌دارد، هر خطا همهٔ نودهای درگیر را برمی‌گرداند؛ رازهای جفت‌سازی پس از استفاده پاک می‌شوند. | [27](docs/fa/27-p1e-tunnel-builder.md) |
| قابل ممیزی | audit ادمین با زنجیرهٔ هش؛ state نسخه‌دار در SQLite؛ payload هرگز لاگ نمی‌شود. | [24](docs/fa/24-p1c-bcc-access.md) |

## نصب

پیش‌نیاز سرور: لینوکس با systemd، `curl`، `openssl`، `python3` و کاربر `sudo`. نصب‌کننده ریلیز امضاشدهٔ معماری (`amd64` یا `arm64`) را می‌گیرد، با کلید root پین‌شده و فهرست ابطال تأیید می‌کند و اگر تأیید نشود چیزی نصب نمی‌کند.

<div dir="ltr" align="left">

```bash
# EX: prints a one-time pairing code
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft --role ex --public-address EX_IP_OR_HOST

# IR: paste the pairing code, then give the printed reply code back to the EX
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft --role ir

# Server managed by BCC (agent only)
sudo env BAFT_BCC_JOB_KEY="$(baft-bcc jobkey show)" BAFT_AGENT_TOKEN=NODE_TOKEN \
  bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft \
  --agent-only --bcc-url https://bcc.example.com --node-id ex-1
```

</div>

مقدار `BAFT_BCC_JOB_KEY` از `baft-bcc jobkey show` روی سرور BCC می‌آید. برای نسخهٔ مشخص `--version vX.Y.Z` اضافه کن. استفاده از `bash -c` به‌جای `| bash` ترمینال را برای وارد کردن کدها آزاد نگه می‌دارد. تأیید دستی ریلیز:

<div dir="ltr" align="left">

```bash
baft-release verify -dir <download dir> -root-pub release/keys/root.pub -revocations release/keys/revocations.json
```

</div>

## کار با BCC

| گام | کار |
|---|---|
| ۱ | `baft-bcc` را اجرا کن (HTTPS یا loopback) و دسترسی وب را با `baft-bcc access init` بساز. |
| ۲ | سرور اضافه کن: **Add a server over SSH** در داشبورد (خواندن کلید میزبان، تأیید اثر انگشت، نصب agent) یا دستور agent-only بالا. |
| ۳ | تونل بساز: **Tunnels** در داشبورد یا `POST /api/tunnels {"ex_node":"…","ir_node":"…"}`. |
| ۴ | BCC هر دو سر را آماده، نصب، بررسی سلامت و نهایی می‌کند؛ هر خطا یا `POST /api/tunnels/cancel` هر دو را برمی‌گرداند. |

بررسی محلی: `baft status`، `baft doctor`، `baft logs`.

## ساخت و آزمون

<div dir="ltr" align="left">

```bash
go build ./... && go vet ./... && go test ./... && go test -race ./...
```

</div>

نسخهٔ Go: ‏1.27.1 (`go.mod`).

## CI چه چیزی را ثابت می‌کند

| job | شواهد |
|---|---|
| `test` | آزمون واحد و یکپارچه، race detector و دروازه‌های شواهد مرحله‌ای. |
| `e2e-binaries`, `e2e-install` | ترافیک واقعی IR→EX، ساخته‌شده از سورس و نصب‌شده روی systemd. |
| `e2e-install-release`, `release-dry-run` | نصب از ریلیز امضاشده؛ ساخت تکرارپذیر؛ دستکاری، ابطال، replay، downgrade و دوباره‌تگ رد می‌شوند. |
| `e2e-agent-enroll`, `e2e-ssh-bootstrap` | ثبت agent و job امضاشده؛ SSH bootstrap روی `sshd` واقعی و رد شدن کلید میزبان اشتباه. |
| `e2e-launch1` | دو سرور ثبت می‌شوند، BCC تونل را با ترافیک واقعی می‌سازد، تغییر خراب روی هر دو سر برمی‌گردد و رازی نمی‌ماند. |
| `stagec-soak`, `step57-recovery-soak` | soakهای تکراری چند Flow، گیرندهٔ کند و recovery. |
| `vulncheck` | `govulncheck` هفتگی و با تغییر وابستگی‌ها. |

## مستندات

| موضوع | سندها |
|---|---|
| طراحی | [01 مرور](docs/fa/01-overview.md) · [02 معماری](docs/fa/02-architecture.md) · [03 مدل امنیتی](docs/fa/03-security-model.md) · [04 پروتکل](docs/fa/04-protocol-baft1.md) · [05 پیکربندی](docs/fa/05-configuration.md) · [11 واژه‌نامه](docs/fa/11-glossary.md) · [ADRها](docs/adr/) |
| عملیات | [06 اجرای IR/EX](docs/fa/06-running-ir-ex.md) · [07 کنترل منابع](docs/fa/07-resource-control.md) · [08 آزمون و CI](docs/fa/08-testing-and-ci.md) · [10 ساختار مخزن](docs/fa/10-repository-layout.md) |
| Launch-1 | [22 نقشهٔ راه](docs/fa/22-launch-1-roadmap.md) · [23 ریلیز](docs/fa/23-p1a-signed-releases.md) · [24 دسترسی BCC](docs/fa/24-p1c-bcc-access.md) · [25 agent](docs/fa/25-p1d-agent.md) · [26 SSH bootstrap](docs/fa/26-p1d-ssh-bootstrap.md) · [27 سازندهٔ تونل](docs/fa/27-p1e-tunnel-builder.md) |
| پژوهش | [09 نقشهٔ راه A–H](docs/fa/09-roadmap.md) · [12 روش](docs/fa/12-innovation-method.md) · [13 TWRL](docs/fa/13-stage-c-twrl.md) · [14 PADL](docs/fa/14-stage-c-padl.md) · [15 telemetry](docs/fa/15-conservation-telemetry.md) · [15D ECRL](docs/fa/15-stage-d-ecrl.md) · [17 دروازه‌ها](docs/fa/17-correctness-gates.md) · [18 soak](docs/fa/18-stage-c-soak.md) · [21 morphing (فقط انگلیسی)](docs/en/21-stealth-pro.md) |
| سوابق | [STATUS](STATUS.md) · [PLAN](PLAN.md) · [BLOCKERS](BLOCKERS.md) · [قفل وابستگی‌ها](dependency-lock.md) · [ماتریس نوآوری](novelty-matrix.md) |

## محدودیت‌ها

- آمادهٔ تولید نیست؛ پایلوت واقعی IR↔EX اجرا نشده؛ تضمینی برای throughput، غیرقابل‌تشخیص بودن یا اتصال داده نمی‌شود.
- resume بعد از restart فرایند یا ریبوت وجود ندارد؛ recovery فقط در همان process است.
- شنوندهٔ IR کاربر نهایی را احراز نمی‌کند؛ بیرون از loopback به امنیت لایهٔ سرویس نیاز دارد.
- record shaping / morphing آزمایشی است و شباهت به HTTPS اثبات نشده.
- مسیرهای H3، relay و Worker خارج از هستهٔ پیش‌فرض‌اند؛ BCC زنده بودن سرویس را می‌سنجد، نه کیفیت ترافیک را.
- VPN عمومی یا proxy با مقصد دلخواه نیست.

## مجوز

همهٔ حقوق برای BAFT Project محفوظ است؛ فقط برای مشاهده، بدون مجوز استفاده، کپی، تغییر، توزیع یا اجرا. [COPYRIGHT](COPYRIGHT) را ببینید.

</div>
