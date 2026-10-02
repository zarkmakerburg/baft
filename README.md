<div dir="rtl" align="right" lang="fa">

<p align="center"><img src="docs/assets/baft-logo.png" alt="BAFT" width="220"></p>

> © 2026 BAFT Project. همهٔ حقوق محفوظ است. این مخزن فقط برای مشاهده عمومی است و هیچ مجوز استفاده‌ای داده نشده است؛ [COPYRIGHT](COPYRIGHT) را ببینید.

# BAFT — رله TCP احرازشده و صفحهٔ کنترل تونل

> **زبان:** فارسی | [English](README.en.md)

[![ci](https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/zarkmakerburg/baft/actions/workflows/ci.yml)
[![stagec-soak](https://github.com/zarkmakerburg/baft/actions/workflows/stagec-soak.yml/badge.svg?branch=main)](https://github.com/zarkmakerburg/baft/actions/workflows/stagec-soak.yml)
[![vulncheck](https://github.com/zarkmakerburg/baft/actions/workflows/vulncheck.yml/badge.svg)](https://github.com/zarkmakerburg/baft/actions/workflows/vulncheck.yml)

## چکیده

BAFT یک رله برای جریان‌های TCP احرازشده بین دو نود تحت کنترل اپراتور است. پروتکل چندگانه و مبتنی بر اعتبار (credit) به نام BAFT/1 داخل یک Carrier روی TLS 1.3 و HTTP/2 اجرا می‌شود. یک شنوندهٔ محلی روی نود **IR** دقیقاً به یک مقصد از پیش مجاز پشت نود **EX** بسته می‌شود؛ peer راه دور فقط می‌تواند یک Route را نام ببرد و هرگز مقصد را تعیین نمی‌کند. صفحهٔ کنترل **BCC** سرورها را ثبت می‌کند، تونل را روی هر دو سر به‌صورت یک تراکنش می‌سازد، آن را راستی‌آزمایی می‌کند و در صورت خطا هر دو سر را برمی‌گرداند. هر artifact که روی سرور نصب می‌شود، پیش از اجرا با یک ریشهٔ اعتماد آفلاین امضا و تأیید می‌شود.

**بلوغ: نرم‌افزار پژوهشی است و آمادهٔ تولید اعلام نشده.** بخش ۹ می‌گوید چه چیزی اثبات شده و چه چیزی نشده.

## ۱. دامنه

| در دامنه | خارج از دامنه |
|---|---|
| رلهٔ TCP با حافظهٔ محدود بین دو نود با Route ثابت | VPN عمومی، SOCKS یا proxy با مقصد دلخواه |
| احراز هویت peer، مجوز Route، ابطال | احراز هویت کاربر نهایی روی شنوندهٔ IR |
| ریلیز امضاشده، نصب تأییدشده، ضد بازگشت به نسخهٔ قدیمی | توزیع بستهٔ شخص ثالث |
| ثبت سرور و ساخت/راستی‌آزمایی/rollback تونل از BCC | تضمین throughput، غیرقابل‌تشخیص بودن یا دسترس‌پذیری |
| بازیابی Carrier در همان process با epoch fencing | resume بعد از restart فرایند یا ریبوت ماشین |

## ۲. مدل سیستم

<div dir="ltr" align="left">

```text
 application ─► 127.0.0.1:1443 ─► BAFT IR ═══ Shard 0..N ═══► BAFT EX ─► fixed target
                  (IR host)       dialer     TCP+TLS1.3+H2    listener    (EX host)

 operator ─► BCC ── signed jobs (Ed25519), pulled over HTTPS ──► baft-agent ─► baft · baft-pair · systemd
```

</div>

| اصطلاح | تعریف |
|---|---|
| Node | یک فرایند BAFT در نقش `dialer` (‏IR) یا `listener` (‏EX). |
| Carrier | جریان بایتی دوطرفهٔ احرازشده که فریم‌های BAFT/1 را حمل می‌کند. |
| Shard | Carrier مستقل با انتقال و زمان‌بندی خودش؛ Shardها هرگز روی یک اتصال TCP جمع نمی‌شوند. |
| Flow | یک اتصال TCP دوطرفهٔ برنامه، با فضای آفست بایتی مستقل برای هر جهت. |
| Route | اتصال از پیش مجاز یک `route_id` به شنوندهٔ محلی (IR) و مقصد ثابت (EX). |
| ACK / WINDOW | ACK نزدیک‌ترین بایت پیوستهٔ پذیرفته‌شده توسط BAFT است، نه اثبات پردازش در برنامه. WINDOW بیشینهٔ مطلق آفست (اعتبار) است، نه اختلاف. |
| BCC / agent | سرور صفحهٔ کنترل و مجری jobهای امضاشدهٔ مجاز روی هر سرور. |

## ۳. طراحی صفحهٔ داده

### ۳.۱ فریم‌بندی BAFT/1

همهٔ اعداد big-endian هستند. هدر ۲۴ بایت و payload حداکثر ۶۵٬۵۳۶ بایت است (اندازهٔ کل فریم ۲۴ تا ۶۵٬۵۶۰). نوع، flags و بیت‌های reserved اعتبارسنجی می‌شوند و مرزها **پیش از** تخصیص حافظهٔ payload بررسی می‌شوند.

| آفست | اندازه | فیلد | قاعده |
|---:|---:|---|---|
| 0 | 4 | `frame_len` | طول کل شامل هدر. |
| 4 | 1 | `type` | ‏`HELLO`، `HELLO_ACK`، `READY`، `OPEN`، `OPEN_OK`، `OPEN_ERR`، `DATA`، `ACK`، `WINDOW`، `FIN`، `FIN_ACK`، `RESET`، `PING`، `PONG`، `GOAWAY` و نوع‌های رزروشدهٔ resume/profile. |
| 5 | 1 | `flags` | در v1 صفر. |
| 6 | 2 | `reserved` | در v1 صفر. |
| 8 | 8 | `stream_id` | برای کنترل Session صفر، برای Flow غیرصفر. |
| 16 | 8 | `offset` | آفست بایتی یا وضعیتیِ وابسته به نوع. |

شروع Session به‌صورت `HELLO → HELLO_ACK → READY دوطرفه` است. `OPEN` یک `route_id` و یک nonce تصادفی حمل می‌کند و هرگز میزبان نمی‌دهد؛ `OPEN` تکراری idempotent است و مقصد را دوباره dial نمی‌کند. `DATA` تکراری یا هم‌پوشان هرگز دوبار نوشته نمی‌شود؛ شکاف و تخطی از اعتبار رد می‌شود. خطاهای روی سیم واژگان ثابت دارند (`AUTH_FAILED`، `ROUTE_DENIED`، `FLOW_CONTROL_ERROR`، `RESOURCE_EXHAUSTED`، `STALE_EPOCH` و …)؛ متن خطای سیستم‌عامل هرگز روی سیم نمی‌رود. ماشین حالت کامل: [04](docs/fa/04-protocol-baft1.md).

### ۳.۲ کنترل منابع

| سازوکار | رفتار |
|---|---|
| allocator سراسری | استخرهای جدای دریافت و replay که از هم قرض نمی‌گیرند (پیکربندی نمونه: ۲۵۶ MiB کل، ۱۲۸/۱۲۸ MiB) و سقف هر Flow که از استخر خودش بیشتر نمی‌شود. |
| رزرو اعتبار | گیرنده پیش از اعلام `WINDOW` بزرگ‌تر ظرفیت واقعی رزرو می‌کند؛ فرستنده پیش از خواندن بایت برنامه ظرفیت replay رزرو می‌کند. |
| backpressure | وقتی اعتبار یا بودجه تمام شود BAFT خواندن از مبدأ را متوقف می‌کند و backpressure TCP منتشر می‌شود. هیچ صف نامحدودی وجود ندارد. |
| زمان‌بندی | deficit round robin بایتی با aging فشار (PADL)؛ صف کنترل محدود (۲۵۶ پیام / ۱ MiB) با سقف burst متناهی جلوی گیر کردن ACK/WINDOW/FIN پشت DATA حجیم را می‌گیرد، بی‌آنکه DATA گرسنه بماند. |

جزئیات: [07](docs/fa/07-resource-control.md)، [13](docs/fa/13-stage-c-twrl.md)، [14](docs/fa/14-stage-c-padl.md).

### ۳.۳ احراز هویت Carrier

| حالت | زمان انتخاب | سازوکار |
|---|---|---|
| mTLS | پیکربندی بخش `noise` ندارد | زنجیرهٔ گواهی، نام میزبان، اعتبار زمانی و EKU بررسی می‌شود؛ هویت نود از URI SAN تأییدشده می‌آید. |
| Noise IK با کلید pin‌شده | ساختهٔ جفت‌سازی `install.sh` | TLS بیرونی با CA محلی اعتبارسنجی می‌شود؛ peerها با کلید ایستای Noise که pin شده و هرکدام به یک هویت مجاز نگاشت شده احراز می‌شوند ([ADR-0007](docs/adr/0007-pinned-noise-morphing.md)). |

### ۳.۴ بازیابی

یک Session زنده می‌تواند Carrier خود را در همان process جایگزین کند: epoch fencing، replay محدود از planهای اعتبارسنجی‌شده، مانع prepare/commit دوطرفه با شناسهٔ commit ایدمپوتنت و رفتار fail-closed در برابر تغییر boot-ID peer. snapshot پایدار و resume بعد از restart/ریبوت پیاده نشده است.

## ۴. صفحهٔ کنترل و زنجیرهٔ تأمین

### ۴.۱ اجزا

| باینری | مسئولیت |
|---|---|
| `baft` | runtime نود (`run`)، `config validate`، `status`، `doctor`، `logs`. |
| `baft-pair` | تولید کلید، PKI بیرونی، جفت‌سازی یک‌بارمصرف که هر دو پیکربندی را می‌نویسد. |
| `baft-bcc` | پایش، مالی، audit زنجیره‌ای، بکاپ رمزشده، چرخش/ابطال توکن، jobهای امضاشده، SSH bootstrap، سازندهٔ تونل، دسترسی وب. |
| `baft-agent` | jobهای امضاشدهٔ مجاز را اجرا می‌کند؛ فقط به باینری امضاشدهٔ ریلیز به‌روز می‌شود. |
| `baft-release` | ‏`keygen`، `certify`، `revoke`، `sign`، `verify`. |
| `baft-bcc-audit-verify` | تأیید آفلاین زنجیرهٔ audit و لنگرها. |
| `baft-master`، `baft-worker`، `baft-cluster-keygen` | runtime و ابزار کلید قدیمی‌تر cluster/mesh. |

### ۴.۲ زنجیرهٔ اعتماد ریلیز

<div dir="ltr" align="left">

```text
 offline Ed25519 root key ──certifies──► CI release key ──signs──► manifest + SHA256SUMS + binaries
        │                                                                   ▲
        └── signs ──► revocation list (sequence, expiry ≤ 400 days)         │
 root public key pinned in install.sh and baft-agent ── verification before any artifact runs ──┘
```

</div>

تأیید شامل گواهی، فهرست ابطال، امضای manifest، `SHA256SUMS` و هش هر باینری است. نصب، downgrade، نسخهٔ دوباره‌تگ‌شده و فهرست ابطال replay‌شده را رد می‌کند و وضعیت را در فایلی متعلق به root می‌نویسد. ساخت‌ها تکرارپذیرند و در CI مقایسه می‌شوند. [23](docs/fa/23-p1a-signed-releases.md) را ببینید.

### ۴.۳ مدل job

BCC هر job را امضا می‌کند (Ed25519، envelope به سبک DSSE). agent فقط وقتی job را اجرا می‌کند که امضا با کلید pin‌شدهٔ BCC درست باشد، job نام همین نود را داشته باشد، action در فهرست مجاز باشد و دقیقاً پارامترهای خودش را داشته باشد (هرکدام مطابق الگوی سخت‌گیرانه)، در بازهٔ اعتبار باشد (۱ ساعت هنگام صدور) و شناسه‌اش قبلاً دیده نشده باشد. هیچ action برای shell یا فرمان دلخواه وجود ندارد. فهرست مجاز: `health`، `restart`، `reload`، `update_baft` و actionهای `tunnel_*` ([25](docs/fa/25-p1d-agent.md)).

### ۴.۴ تراکنش تونل

| فاز | اثر |
|---|---|
| prepare روی EX / IR | کلیدها و PKI تضمین می‌شوند؛ کد جفت‌سازی و پاسخ تولید می‌شود؛ پیکربندی جدید staging می‌شود. هیچ چیز زنده تغییر نمی‌کند. |
| commit روی EX / IR | پیکربندی و unit قبلی بکاپ می‌شود؛ پیکربندی staging نصب می‌شود؛ سرویس باید در یک پنجرهٔ settle فعال بماند وگرنه نود خودش را برمی‌گرداند. |
| سلامت IR / EX | سرویس بدون restart پایدار است؛ شنونده و Route محلی اتصال می‌پذیرند؛ با تکرار. |
| finalize | بکاپ‌ها و رازهای یک‌بارمصرف پاک می‌شوند. |
| rollback | از هر فاز، هر نودی که به یک مرحله رسیده پیکربندی، unit و وضعیت سرویس قبلی را برمی‌گرداند. شکست، timeout مرحله یا لغو اپراتور آن را آغاز می‌کند. |

رازهای جفت‌سازی فقط تا لازم بودن یک مرحله در jobها هستند و بعد حذف می‌شوند. [27](docs/fa/27-p1e-tunnel-builder.md) را ببینید.

## ۵. الزامات امنیتی

کلیدواژه‌های هنجاری مطابق RFC 2119 هستند.

| شناسه | الزام | مشخصات · شواهد |
|---|---|---|
| SR-1 | Carrier باید از TLS 1.3 یا بالاتر و بدون بازگشت به متن ساده استفاده کند؛ بررسی زنجیره، نام میزبان، اعتبار و EKU نباید قابل خاموش شدن باشد. | [03](docs/fa/03-security-model.md) · `ci/test` |
| SR-2 | هویت نود باید از احراز هویت بیاید، نه از metadata ‏HELLO؛ اعتماد به CA و مجوز Route باید جدا باشند. | [03](docs/fa/03-security-model.md) · `ci/test` |
| SR-3 | peer نباید بتواند مقصد انتخاب کند؛ فریم‌ها باید پیش از تخصیص حافظه اعتبارسنجی شوند. | [04](docs/fa/04-protocol-baft1.md) · `ci/test`، COR-01 |
| SR-4 | حافظهٔ دریافت و replay باید محدود باشد؛ پر شدن باید backpressure ایجاد کند، نه رشد. | [07](docs/fa/07-resource-control.md) · `stagec-soak` |
| SR-5 | payload کاربر نباید در لاگ یا support bundle ظاهر شود. | [03](docs/fa/03-security-model.md) · `ci/test` |
| SR-6 | artifactهای نصب‌شده باید با root پین‌شده، فهرست ابطال منقضی‌نشده و هش‌های ثبت‌شده تأیید شوند؛ downgrade و دوباره‌تگ باید رد شوند. | [23](docs/fa/23-p1a-signed-releases.md) · `e2e-install-release`، `release-dry-run` |
| SR-7 | دسترسی وب BCC باید مسیر مخفی و اعتبارنامهٔ تصادفی داشته باشد، فقط هش گذرواژه را نگه دارد، نشست را در برابر CSRF محافظت کند و ورود را محدود نرخ کند. | [24](docs/fa/24-p1c-bcc-access.md) · `ci/test` |
| SR-8 | agent باید فقط jobهای امضاشدهٔ BCC، مجاز، منقضی‌نشده و قبلاً دیده‌نشده با پارامترهای سخت‌گیرانه را اجرا کند. | [25](docs/fa/25-p1d-agent.md) · `ci/test`، `e2e-agent-enroll` |
| SR-9 | SSH bootstrap باید کلید میزبان را pin کند و نباید اعتبارنامه را ذخیره، لاگ یا audit کند. | [26](docs/fa/26-p1d-ssh-bootstrap.md) · `e2e-ssh-bootstrap` |
| SR-10 | تغییر تونل باید تا قبل از finalize روی هر نود درگیر برگشت‌پذیر باشد و رازهای جفت‌سازی باید پس از استفاده حذف شوند. | [27](docs/fa/27-p1e-tunnel-builder.md) · `e2e-launch1` |
| SR-11 | کنش‌های مدیریتی باید در لاگ audit ضدتخریب ثبت شوند. | [24](docs/fa/24-p1c-bcc-access.md) · `ci/test` |

## ۶. راستی‌آزمایی

| job در CI | شواهد تولیدشده |
|---|---|
| `test` | آزمون واحد و یکپارچه، race detector و دروازه‌های شواهد مرحله‌ای (پایداری، یکپارچگی sync، mesh، telemetry، سخت‌سازی BCC، backup/restore، runtime ‏ECRL). |
| `e2e-binaries`, `e2e-install` | ترافیک واقعی IR→EX با باینری‌های ساخته‌شده از سورس و نصب‌شده روی systemd. |
| `e2e-install-release`, `release-dry-run` | نصب از ریلیز امضاشده؛ ساخت تکرارپذیر؛ دستکاری، ابطال، replay، downgrade و دوباره‌تگ رد می‌شوند. |
| `e2e-agent-enroll`, `e2e-ssh-bootstrap` | ثبت agent و job امضاشده روی systemd؛ SSH bootstrap روی `sshd` واقعی و رد شدن کلید میزبان اشتباه. |
| `e2e-launch1` | دو سرور ثبت می‌شوند؛ BCC تونل را با ترافیک واقعی می‌سازد؛ تغییر خراب روی هر دو سر برمی‌گردد؛ هیچ رازی روی BCC یا سرورها نمی‌ماند. |
| `stagec-soak`, `step57-recovery-soak` | soakهای تکراری چند Flow، گیرندهٔ کند و recovery. |
| `vulncheck` | ‏`govulncheck` هفتگی و با تغییر وابستگی‌ها. |

نتایج، شواهد درستی روی CI/loopback هستند، نه benchmark شبکهٔ عمومی. سوابق: [STATUS](STATUS.md)، [TEST-RESULTS](TEST-RESULTS.md).

## ۷. نصب و بهره‌برداری

پیش‌نیاز: لینوکس با systemd، `curl`، `openssl`، `python3` و کاربر `sudo`. نصب‌کننده ریلیز امضاشدهٔ معماری (`amd64` یا `arm64`) را می‌گیرد، تأیید می‌کند (SR-6) و در صورت شکست چیزی نصب نمی‌کند.

<div dir="ltr" align="left">

```bash
# EX: prints a one-time pairing code
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft --role ex --public-address EX_IP_OR_HOST

# IR: paste the pairing code, then return the printed reply code to the EX
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft --role ir

# Server managed by BCC (agent only)
sudo env BAFT_BCC_JOB_KEY="$(baft-bcc jobkey show)" BAFT_AGENT_TOKEN=NODE_TOKEN \
  bash -c "$(curl -fsSL https://raw.githubusercontent.com/zarkmakerburg/baft/main/install.sh)" baft \
  --agent-only --bcc-url https://bcc.example.com --node-id ex-1
```

</div>

مقدار `BAFT_BCC_JOB_KEY` را `baft-bcc jobkey show` روی سرور BCC چاپ می‌کند و `--version vX.Y.Z` نسخه را مشخص می‌کند. به‌جای `| bash` از `bash -c` استفاده شده تا ترمینال برای وارد کردن کدها آزاد بماند. راستی‌آزمایی مستقل یک ریلیز:

<div dir="ltr" align="left">

```bash
baft-release verify -dir <download dir> -root-pub release/keys/root.pub -revocations release/keys/revocations.json
```

</div>

کار با BCC: `baft-bcc` را اجرا کن (HTTPS یا loopback) و `baft-bcc access init` را بزن؛ سرور را از داشبورد (**Add a server over SSH**) یا با دستور agent-only اضافه کن؛ تونل را از **Tunnels** یا `POST /api/tunnels {"ex_node":"…","ir_node":"…"}` بساز؛ با `POST /api/tunnels/cancel` لغو کن. بررسی محلی: `baft status`، `baft doctor`، `baft logs`.

ساخت: `go build ./... && go vet ./... && go test ./... && go test -race ./...` (‏Go 1.27.1، `go.mod`).

## ۸. ریلیز و نسخه‌گذاری

ریلیزها با برچسب `vMAJOR.MINOR.PATCH` از یک commit روی `main` توسط workflow ‏`release` ساخته می‌شوند، در یک محیط محافظت‌شده امضا می‌شوند، با root پین‌شده تأیید می‌شوند و به‌صورت پیش‌نویس برای بازبینی مالک منتشر می‌شوند. یادداشت‌های ریلیز در `release/notes/<tag>.md` هستند. نسخهٔ فعلی: [`v0.1.0`](https://github.com/zarkmakerburg/baft/releases/tag/v0.1.0)، شناسهٔ کلید root: ‏`98741d81da746d2e435302f90569911d`. فهرست ابطال با root امضا شده، شمارهٔ ترتیب دارد و انقضای آن حداکثر ۴۰۰ روز است.

## ۹. وضعیت انطباق و محدودیت‌های شناخته‌شده

| بخش | اثبات‌شده | اثبات‌نشده |
|---|---|---|
| صفحهٔ داده A–B | مسیر عمودی امن؛ آزمون ۱ GiB دوطرفه (COR-01) | — |
| صفحهٔ داده C | allocator، backpressure، PADL، TWRL؛ soak تکرارشده و نمونهٔ race | benchmark شبکهٔ عمومی؛ profiling رسمی ۶۰ ثانیه × ۵ |
| صفحهٔ داده D | بازیابی در همان process با epoch fencing | resume بعد از restart/ریبوت؛ snapshot پایدار |
| صفحهٔ کنترل | SR-6 تا SR-11 مطابق بخش ۶ | سنجش کیفیت ترافیک فراتر از زنده بودن سرویس |
| استقرار | نصب systemd روی میزبان‌های CI | پایلوت واقعی IR↔EX؛ چرخش گواهی و عملیات support bundle |

محدودیت‌های دیگر: شنوندهٔ IR کاربر نهایی را احراز نمی‌کند و بیرون از loopback به امنیت لایهٔ سرویس نیاز دارد؛ record shaping / morphing آزمایشی است و شباهت به HTTPS اثبات نشده؛ مسیرهای H3، relay و Worker خارج از هستهٔ پیش‌فرض‌اند. [KNOWN-LIMITATIONS](KNOWN-LIMITATIONS.md) را ببینید.

## ۱۰. مستندات

| موضوع | سندها |
|---|---|
| طراحی | [01 مرور](docs/fa/01-overview.md) · [02 معماری](docs/fa/02-architecture.md) · [03 مدل امنیتی](docs/fa/03-security-model.md) · [04 پروتکل](docs/fa/04-protocol-baft1.md) · [05 پیکربندی](docs/fa/05-configuration.md) · [11 واژه‌نامه](docs/fa/11-glossary.md) · [ADRها](docs/adr/) |
| عملیات | [06 اجرای IR/EX](docs/fa/06-running-ir-ex.md) · [07 کنترل منابع](docs/fa/07-resource-control.md) · [08 آزمون و CI](docs/fa/08-testing-and-ci.md) · [10 ساختار مخزن](docs/fa/10-repository-layout.md) |
| Launch-1 | [22 نقشهٔ راه](docs/fa/22-launch-1-roadmap.md) · [23 ریلیز](docs/fa/23-p1a-signed-releases.md) · [24 دسترسی BCC](docs/fa/24-p1c-bcc-access.md) · [25 agent](docs/fa/25-p1d-agent.md) · [26 SSH bootstrap](docs/fa/26-p1d-ssh-bootstrap.md) · [27 سازندهٔ تونل](docs/fa/27-p1e-tunnel-builder.md) |
| پژوهش | [09 نقشهٔ راه A–H](docs/fa/09-roadmap.md) · [12 روش](docs/fa/12-innovation-method.md) · [15 telemetry](docs/fa/15-conservation-telemetry.md) · [15D ECRL](docs/fa/15-stage-d-ecrl.md) · [17 دروازه‌ها](docs/fa/17-correctness-gates.md) · [18 soak](docs/fa/18-stage-c-soak.md) · [21 morphing (فقط انگلیسی)](docs/en/21-stealth-pro.md) |
| سوابق | [STATUS](STATUS.md) · [PLAN](PLAN.md) · [BLOCKERS](BLOCKERS.md) · [قفل وابستگی‌ها](dependency-lock.md) · [ماتریس نوآوری](novelty-matrix.md) |

## مجوز

همهٔ حقوق برای BAFT Project محفوظ است. مخزن فقط برای مشاهده است و هیچ مجوزی برای استفاده، کپی، تغییر، توزیع یا اجرای نرم‌افزار داده نشده است. [COPYRIGHT](COPYRIGHT) را ببینید.

</div>
