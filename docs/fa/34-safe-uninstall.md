<div dir="rtl" align="right" lang="fa">

# 34 — حذف امن (Safe uninstall)

HQ A3. حذف، یک قابلیت رسمی چرخهٔ عمر BAFT است: `baft uninstall` و گزینهٔ ۱۳ منو. فقط **محلی** است: حذف از راه دور یا روی ناوگان وجود ندارد (HOLD) و agent هیچ راهی برای اجرای آن ندارد.

<div dir="ltr">

```
INSPECT -> BUILD REMOVAL PLAN -> SHOW EXACT CHANGES -> EXPLICIT CONFIRMATION
        -> BACKUP IF REQUIRED -> STOP SERVICES -> REMOVE -> VERIFY
```

</div>

## فرمان‌ها

<div dir="ltr">

```
baft uninstall --preview            # the exact plan; changes nothing
baft uninstall --preview --json     # the same, for automation
baft uninstall                      # standard uninstall: BAFT services + agent, their binaries; KEEP DATA
baft uninstall --binaries           # BAFT binaries only
baft uninstall --agent              # the agent only
baft uninstall --bcc                # the BCC only
baft uninstall --services           # BAFT services + binaries
baft uninstall --full               # everything above
baft uninstall --resume | --restore # finish, or undo, an interrupted uninstall
```

</div>

روی ترمینال، فرمان plan را نشان می‌دهد و می‌پرسد؛ در اسکریپت `--yes` لازم است، و اگر تونل فعالی متوقف شود `--stop-active-tunnels` هم. فلگ‌های مسیر (`--unit-dir`، `--bin-dir`، `--prefix`، `--config-dir`، `--state-dir`، `--agent-dir`، `--agent-state-dir`، `--bcc-state-file`، `--journal-dir`، `--backup-dir`) پیش‌فرضِ مسیرهای installer را دارند.

| کد خروج | معنی |
|---|---|
| 0 | انجام و وارسی شد، یا پیش‌نمایش بود، یا چیزی برای حذف نبود |
| 1 | شکست خورد؛ هر چه تغییر داده بود برگردانده شد (journal می‌گوید چه شد) |
| 2 | usage |
| 3 | مسدود (تعارض مالکیت، تغییر تونلِ در جریان، BCC در حال بازیابی)؛ چیزی تغییر نکرد |
| 4 | تأیید لازم است یا داده نشد؛ چیزی تغییر نکرد |
| 5 | یک uninstall قبلی تمام نشده: اول `--resume` یا `--restore` |

## قانون مالکیت

فقط artifactهایی که مالکیت BAFT آن‌ها **اثبات شده** باشد قابل حذف‌اند. اثبات یکی از این‌هاست:

| Artifact | اثبات |
|---|---|
| unit انتقالِ ساختهٔ `install.sh` | بایت‌به‌بایت همان چیزی که installer برای پارامترهای خود unit می‌نویسد (یک تست تابع shell خودِ installer را اجرا و مقایسه می‌کند) |
| unit تونلِ ساختهٔ BCC | هدر `# baft-managed: true` / `# baft-tunnel:` و marker `baft.managed.json` کنار config که همان تونل را نام ببرد و با unit و config بایت‌به‌بایت بخواند |
| unit مربوط به agent | بایت‌به‌بایت همان unit agentِ installer |
| unit مربوط به BCC | برچسب `# baft-managed: true` و `# baft-component: bcc` (BAFT نصب‌کنندهٔ BCC ندارد؛ اپراتور unitی را که می‌خواهد BAFT مالکش باشد برچسب می‌زند) |
| باینری | اطلاعات build تعبیه‌شدهٔ Go (از فایل خوانده می‌شود، هرگز اجرا نمی‌شود) ماژول BAFT و برنامهٔ مورد انتظار را نام ببرد |
| فایل‌ها | نامی که BAFT می‌نویسد، در محل خود BAFT، با قالب BAFT (config که load شود، کلید Noise، فایل‌های PEM، marker، release state، باقی‌ماندهٔ pairing، سوابق سازندهٔ تونل، backupهای rerun نصب‌کننده، پایگاه state، فایل دسترسی، کلید job، audit log و backupهای BCC) |

| طبقه‌بندی | Uninstall |
|---|---|
| `BAFT_MANAGED` | قابل حذف |
| `DRIFTED` (BAFT نوشته، بعداً ویرایش شده) | برای بازبینی دستی نگه داشته می‌شود: نه متوقف، نه حذف |
| `DISCOVERED_UNMANAGED` | هرگز دست نمی‌خورد |
| `OWNERSHIP_CONFLICT` | HOLD / بازبینی دستی، و **کل اجرا را مسدود می‌کند** (کد ۳) |
| `UNKNOWN` (ناخوانا، symlink، فایل غیرمنتظره) | هرگز دست نمی‌خورد |

هر چیزی که هنوز **چیزی که می‌ماند از آن استفاده می‌کند** نگه داشته می‌شود: باینری‌ای که یک unit باقی‌مانده اجرا می‌کند (همهٔ unitهای پوشه‌های unit برای برنامه‌هایی که اجرا می‌کنند بررسی می‌شوند)، config و کلیدها و فهرست ابطالِ یک تونل باقی‌مانده، release stateِ یک agent باقی‌مانده. یک unit با نام `baft*` که خوانده نشود همهٔ باینری‌های BAFT را نگه می‌دارد. پوشه‌ها فقط وقتی خالی شوند حذف می‌شوند؛ فایل‌های اپراتور (گواهی و کلید TLSِ BCC، admin token، install script) هرگز حذف نمی‌شوند.

## داده: پیش‌فرض KEEP

| دسته | فلگ | شامل |
|---|---|---|
| BCC state | `--delete-bcc-state` | پایگاه state (و نسخهٔ JSONِ مهاجرت)، فایل دسترسی، کلید امضای job |
| certificates | `--delete-certificates` | PKI بیرونی، کلیدهای Noise، CAِ EX که IR پین کرده، توکن و کلیدهای پین‌شدهٔ agent، release trust state |
| backups | `--delete-backups` | backupهای rerun نصب‌کننده، کپی‌های config، کپی PKI سازندهٔ تونل، backupهای رمزشدهٔ BCC |
| tunnel configs | `--delete-tunnel-configs` | configها، markerهای مالکیت، باقی‌ماندهٔ pairing، سوابق تغییر سازندهٔ تونل |
| audit history | `--delete-audit` | audit log و outbox لنگرِ BCC، سابقهٔ jobهای اجراشدهٔ agent |

هر دسته جداگانه انتخاب می‌شود. با `--full` روی ترمینال هر کدام جدا پرسیده می‌شود و پیش‌فرض NO است. backupهای اضطراری هرگز با uninstall حذف نمی‌شوند.

## تونل‌های فعال

تونلِ در حال اجرا هرگز بی‌صدا متوقف نمی‌شود: plan آن را با اثرش فهرست می‌کند (EX: روی آدرس listen دیگر نمی‌پذیرد؛ IR: کلاینت‌های محلی تونل را از دست می‌دهند) و اجرا رضایت صریح لازم دارد (`--stop-active-tunnels`، یا یک «بله»ی جداگانه روی ترمینال). پیکربندی‌اش حفظ می‌شود مگر tunnel configs انتخاب شده باشد.

## ایمنی BCC

پیش از حذف BCC state، BCC متوقف می‌شود و با نگه داشتنِ قفل state خودِ BCC یک **backup اضطراری** گرفته می‌شود (پس ثابت می‌شود BCC اجرا نمی‌شود و state و audit یک snapshot‌اند): همهٔ فایل‌های BCC در `/var/backups/baft/bcc-emergency-<time>/` کپی می‌شوند (۰۷۰۰، فایل‌ها ۰۶۰۰)، هر کپی بایت‌به‌بایت وارسی می‌شود، `manifest.json` منبع و digestها را ثبت می‌کند، و `baft-bcc verify-backup` یک کپی خصوصی از پایگاه را با خوانندهٔ خودِ BCC باز می‌کند و زنجیرهٔ hash مربوط به audit را وارسی می‌کند. محل نمایش داده می‌شود. فقط بعد از آن چیزی حذف می‌شود؛ اگر backup شکست بخورد، BCC دوباره راه می‌افتد و چیزی حذف نمی‌شود. مالک فقط صریحاً می‌تواند از آن بگذرد: `--no-backup`، یا تایپ `NO BACKUP` روی ترمینال. برای بازگرداندن: BCC را متوقف کنید، هر فایل را به مسیر منبعش در manifest برگردانید (mode ۰۶۰۰)، BCC را راه بیندازید.

## تراکنشی و قابل بازیابی

هیچ چیز مستقیم حذف نمی‌شود. هر فایل اول با digestی که plan دیده مقایسه می‌شود، بعد به یک قرنطینهٔ journalدار (`/var/lib/baft-uninstall/run-<time>/`) منتقل می‌شود؛ سرویس‌ها با ثبت وضعیت قبلی‌شان متوقف و disable می‌شوند؛ باینری‌ها آخر از همه. بعد وارسی ثابت می‌کند هر مسیر حذف‌شده رفته، هر artifactِ نگه‌داشته بایت‌به‌بایت یکسان است و هر unitِ باقی‌مانده که در حال اجرا بود هنوز اجرا می‌شود. فقط آن‌وقت قرنطینه پاک می‌شود (commit) و پوشه‌های BAFT که خالی شده‌اند حذف می‌شوند. شکست پیش از commit همه چیز را برمی‌گرداند (فایل‌ها سر جایشان، unitها مثل قبل enable و start). اگر process کشته شود، اجرای بعدی امتناع می‌کند تا `baft uninstall --restore` آن را برگرداند یا `baft uninstall --resume` تمامش کند (اگر خودِ باینری منتقل شده بود، کپیِ داخل قرنطینهٔ همان run را اجرا کنید). journal فقط مسیر، digest و وضعیت دارد، هرگز محتوای فایل، و به‌عنوان سابقهٔ آنچه حذف شد می‌ماند.

## نصب دوباره

uninstallِ استاندارد config، کلیدها و release state را نگه می‌دارد، پس `install.sh --yes` بعد از آن **همان node** را برمی‌گرداند: config با باینری release اعتبارسنجی می‌شود، pairing رد می‌شود و تونل بدون pairing دوباره برمی‌گردد. چون release state نگه داشته می‌شود، release قدیمی‌تر بعد از uninstall و نصب دوباره هم رد می‌شود (anti-rollback برقرار است). بعد از حذف کامل با حذف همهٔ دسته‌ها، نصب دوباره یک نصب تازه است و دوباره pair می‌کند. کاربر سرویس نگه داشته می‌شود.

## تست‌ها

- `internal/uninstall`: مالکیت (تطابق با قالب installer و عدم تطابقِ یک‌بایتی، marker سازگارِ BCC / drift / همهٔ شکل‌های تعارض، BCC برچسب‌دار و بی‌برچسب، اطلاعات build در Go، symlinkها)، scopeها، پیش‌فرض داده، قاعدهٔ «در حال استفاده»، تونل‌های فعال، تغییر بین plan و apply، تزریق شکست در هر مرحله (فایل‌ها و وضعیت سرویس برمی‌گردند)، crash در هر مرحله و سپس restore یا resume، backupِ BCC (BCC متوقف، رد وقتی قفل گرفته شده، backupِ وارسی‌نشده برگردانده می‌شود، `--no-backup`، journalِ معلقِ BCC)، یکسانی قالب unitها با `install.sh`. با mutation بررسی شده.
- `tests/uninstall`: یک BCC state واقعی از backup اضطراریِ uninstall و وارسی‌کنندهٔ BCC عبور می‌کند؛ فایل‌های برگردانده در BCC باز می‌شوند؛ کپیِ دستکاری‌شده رد می‌شود.
- `cmd/baft`: فرمان (usage، پیش‌نمایش، رضایت، تعارض، تأییدهای تعاملی، run معلق و restore) و گزینهٔ ۱۳ منو.
- CI `e2e-uninstall` (systemd، releaseهای امضاشده): نصب EX + IR، یک BCC برچسب‌دار، یک agent و دو unit اپراتور؛ تعارض مسدود می‌کند؛ بدون رضایت رد می‌شود؛ run کشته‌شده restore و run کشته‌شده resume می‌شود؛ دادهٔ کاربر حفظ و نصب دوباره بدون pairing؛ upgrade، uninstall، رد release قدیمی‌تر، نصب دوبارهٔ release فعلی؛ حذف BCC state فقط بعد از backup اضطراریِ وارسی‌شده؛ حذف کامل فقط دادهٔ تأییدشده را حذف می‌کند؛ وارسی پاکی؛ نصب تازه؛ unitهای اپراتور در تمام مدت سالم می‌مانند.

## محدودیت‌ها

همهٔ سرویس‌های BAFT روی میزبان با هم در scope هستند؛ prefixهای installer غیر از پیش‌فرض با `--prefix` داده می‌شوند (یکی در هر اجرا). پوشه‌های خودِ BCC حذف نمی‌شوند. تونلی که BCC ساخته بعد از نصب دوباره از طریق BCC برمی‌گردد (config و markerِ نگه‌داشته‌اش تا بازسازی MISSING دیده می‌شوند). `BAFT_UNINSTALL_FAIL_AT` / `BAFT_UNINSTALL_CRASH_AT` hookهای آزمایشی‌اند.

</div>
