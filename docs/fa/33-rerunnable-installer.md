<div dir="rtl" align="right" lang="fa">

# 33 — نصب‌کننده‌ی قابل اجرای مجدد

HQ A3. `install.sh` را می‌شود دوباره روی سروری که BAFT دارد اجرا کرد، و **اجرای مجدد یعنی نصب مجدد نیست**. اول نگاه می‌کند، یک plan نشان می‌دهد، فقط همان چیزهای plan را تغییر می‌دهد، نتیجه را وارسی می‌کند و اگر وارسی رد شد برمی‌گرداند.

<div dir="ltr">

```
INSPECT -> BUILD PLAN -> SHOW PLAN -> APPLY -> VERIFY -> ROLLBACK IF FAILED
```

</div>

## حالت‌ها

| حالت | معنی | اجرا چه می‌کند |
|---|---|---|
| `FRESH_INSTALL` | چیزی از BAFT نیست | نصب معمولی؛ هرگز نمی‌پرسد |
| `ALREADY_INSTALLED` | نصب، pair و در حال اجرا | هیچ (نسخه هنگام دانلود مقایسه می‌شود) |
| `CURRENT` | مثل بالا و برابر نسخه‌ی تأییدشده | **هیچ تغییری** |
| `UPGRADE_AVAILABLE` | مثل بالا، نسخه‌ی تأییدشده جدیدتر است | باینری‌ها جایگزین و یک بار restart |
| `REPAIR_REQUIRED` | نصب و pair است ولی متوقف/غیرفعال است یا یک permission خراب است | فقط start/enable/اصلاح permission |
| `PARTIAL_INSTALL` | نصب نیمه‌کاره، یا EX که منتظر pairing است | آنچه کم است کامل می‌شود |
| `BROKEN_INSTALL` | config معتبر نیست، کلید Noise نیست، یا EX گواهی‌هایش را از دست داده | **رد می‌شود**؛ هیچ چیز تغییر نمی‌کند (کد خروج ۳) |

## آنچه اجرای مجدد هرگز نمی‌کند

کلید Noise، CA و گواهی سرور بیرونی، توکن agent و کلید job مربوط به BCC را نمی‌چرخاند و دوباره نمی‌سازد؛ config معتبر را بازنویسی نمی‌کند؛ unit موجود systemd را بازنویسی نمی‌کند (پس unitی که سازنده‌ی تونل BCC ساخته digest مالکیتش را نگه می‌دارد)؛ تونلی را حذف نمی‌کند؛ state را پاک نمی‌کند؛ release state را ریست یا کم نمی‌کند (نسخه‌ی قدیمی‌تر به‌عنوان downgrade رد می‌شود)؛ و مالک یا mode دایرکتوری‌های موجود را عوض نمی‌کند. توکن، کلید job یا root نسخه‌ی متفاوت در plan یک `replace` دیده‌شدنی است، هرگز بی‌صدا نیست.

## کنترل‌ها

<div dir="ltr">

```
sudo bash install.sh --role ex --plan            # inspect and show the plan; no root, no network, writes nothing
sudo bash install.sh --role ex --plan --json     # the same, for scripts
sudo bash install.sh --role ex --yes             # apply a plan that changes an existing install
sudo bash install.sh --role ex --re-pair --yes   # deliberately start a new pairing (config is backed up first)
```

</div>

نصب تازه هرگز نمی‌پرسد. planی که نصب موجود را تغییر می‌دهد روی ترمینال `Apply this plan? [y/N]` می‌پرسد و بدون ترمینال `--yes` لازم دارد (وگرنه با کد ۴ خارج می‌شود و چیزی تغییر نمی‌کند). SSH bootstrap مربوط به BCC `--yes` می‌دهد چون اپراتور خود bootstrap را تأیید کرده. `--re-pair` تنها راه جایگزینی config معتبر است و تا پایان pairing تونل را قطع می‌کند.

## اعمال، وارسی، برگشت

پیش از جایگزینی هر فایل، یک کپی در `/opt/baft/backups/rerun-<time>/` گذاشته می‌شود (سه اجرای آخر نگه داشته می‌شود). باینری جدید باید اجرا شود (`baft version`) پیش از آنکه به سرویسی دست بخورد؛ بعد از restart سرویس باید چند ثانیه با همان process بالا بماند؛ config باید معتبر باشد. تازه آن‌وقت release state ثبت می‌شود. اگر هر مرحله رد شود، فایل‌های قبلی برمی‌گردند، فایل‌های ساخته‌شده در همین اجرا پاک می‌شوند و سرویسی که در حال اجرا بود دوباره راه می‌افتد. سرویسی که این اجرا به آن دست نزده restart نمی‌شود. release state هرگز برای نسخه‌ای که وارسی‌اش رد شده ثبت نمی‌شود.

## گیت idempotency (CI)

`tests/e2e/install_two_roles.sh` (نصب از release) هر دو installer را **پنج بار** روی یک جفت EX/IR سالم دوباره اجرا می‌کند و می‌خواهد: SHA-256 یکسان برای config، کلید Noise، گواهی‌ها، unitها، release state و باینری‌ها؛ مالک و mode یکسان؛ همان process سرویس (بدون restart)؛ ترافیک همچنان جاری؛ بدون چاپ کد pairing یا reply؛ بدون نوشتن backup. بعد بررسی می‌کند سرویسِ دستی‌متوقف‌شده با اجرای مجدد تعمیر می‌شود، planِ تغییردهنده بدون `--yes` رد می‌شود، upgrade همه‌ی secretها را نگه می‌دارد و release state را جلو می‌برد، downgrade رد می‌شود، و upgrade به نسخه‌ای که باینری‌اش اجرا نمی‌شود (یا سرویسش می‌میرد) با سالم ماندن باینری، config، state و تونلِ قبلی برگردانده می‌شود. `tests/installer/plan_test.go` ماتریس حالت‌های `--plan --json` را بدون root و systemd پوشش می‌دهد.

## محدودیت‌ها

نصب از source (`--from-source`) باینری‌ها را دوباره می‌سازد، پس اجرای مجدد آنجا طبق تعریف یک upgrade است. `--plan` دانلود نمی‌کند، پس فقط با `BAFT_VERSION` می‌تواند مقایسه کند؛ مقایسه‌ی دقیق بعد از وارسی نسخه انجام می‌شود. گزینه‌های Update و Repair در منوی `baft` تا تغییر بعدی که به این installer وصلشان کند `(planned)` می‌مانند.

</div>
