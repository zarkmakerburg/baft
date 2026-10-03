<div dir="rtl" align="right" lang="fa">

# 29 — پشتیبان BCC و پیش‌نمایش restore

## پشتیبان‌ها

وقتی `BAFT_BCC_BACKUP_KEY` (base64 یک مقدار ۳۲ بایتی) تنظیم باشد، BCC پشتیبان رمزشده می‌نویسد: در `--backup-dir` (پیش‌فرض `./backups`)، هر `--backup-interval` (پیش‌فرض ۲۴ ساعت)، با نگه‌داری ۷ فایل روزانه و ۴ هفتگی (`daily-<time>.baftbak` و `weekly-<time>.baftbak`، دسترسی `0600`). پشتیبان یعنی AES-256-GCM روی کل state و زنجیرهٔ audit؛ header (زمان، نسخهٔ schema، SHA-256 محتوا، شمارهٔ و hash آخرین audit) به‌عنوان داده‌ی همراه احراز می‌شود. کلید فقط از محیط می‌آید و هرگز در پشتیبان نوشته نمی‌شود.

## پیش‌نمایش restore

<div dir="ltr" align="left">

```bash
export BAFT_BCC_BACKUP_KEY=...        # the same key
baft-bcc restore-preview --backup backups/daily-20261002T000000Z.baftbak --state-file bcc-state.json
baft-bcc restore-preview ... --json   # machine-readable
```

</div>

فقط‌خواندنی. پشتیبان را باز و verify می‌کند (احراز، checksum، زنجیرهٔ audit، anchor)، بعد بررسی‌های restore واقعی و **ادغام anti-rollback** آن را روی کپی تکرار می‌کند و گزارش می‌دهد restore چه چیزی باقی می‌گذارد:

- گره‌ها، تونل‌ها و jobها: تعداد فعلی و بعد از restore، و اینکه کدام شناسه‌ها حذف می‌شوند، دوباره پیدا می‌شوند یا تغییر می‌کنند؛
- آنچه ادغام از وضعیت زنده نگه می‌دارد نه از پشتیبان: چرخش توکن و ابطال‌های بعد از پشتیبان و بیشینهٔ telemetry (restore نمی‌تواند آن‌ها را برگرداند)؛
- اینکه restore واقعی **رد می‌شد** یا نه (audit فعلی نامعتبر است یا anchor پشتیبان را ادامه نمی‌دهد)؛
- هشدارها: گره‌های ثبت‌شده بعد از پشتیبان ناپدید می‌شوند، رکورد تونل‌ها ناپدید می‌شود در حالی‌که سرورهایشان کار می‌کنند، تونلی در پشتیبان وسط تغییر بوده، گره‌ای که فقط در پشتیبان هست دوباره ظاهر می‌شود، پشتیبان از ۷ روز قدیمی‌تر است.

پیش‌نمایش فقط شناسه و تعداد می‌دهد: هیچ hash توکن، کد جفت‌سازی یا کلیدی در آن نیست. کد خروج `0` = verify شد و رد نمی‌شد، `1` = verify نشد، رد می‌شد یا اجرا نشد، `2` = استفادهٔ اشتباه.

**BCC باید متوقف باشد.** state (SQLite) و audit دو فایل‌اند که BCC در حال اجرا آن‌ها را در لحظه‌های متفاوت عوض می‌کند و پردازش دیگر نمی‌تواند قفل درون‌پردازشی BCC را شریک شود، پس فقط BCC متوقف یک تصویر هماهنگ از هر دو می‌دهد. این اجباری است، نه فقط مستند: BCC در تمام عمرش قفل انحصاری `<state-file>.lock` را نگه می‌دارد (کرنل هر طور BCC تمام شود آن را آزاد می‌کند؛ BCC دوم روی همان state رد می‌شود) و اگر `restore-preview` نتواند آن قفل را بگیرد با «stop BCC before previewing from files» شکست می‌خورد. تا وقتی نوشتن (`<state-file>-journal`) یا restore (`.restore-journal.json`) نیمه‌کاره مانده هم رد می‌کند: یک بار BCC را بالا بیاورید تا بازیابی کند، متوقفش کنید، بعد پیش‌نمایش بگیرید. در حافظه و یک پوشهٔ موقت خصوصی می‌خواند و در پوشهٔ زنده چیزی نمی‌نویسد (جز فایل خالی `.lock` که خود BCC نگه می‌دارد).

داخل سرور در حال اجرا همین منطق `Server.PreviewRestore` است. قفل‌ها را به همان ترتیب restore واقعی می‌گیرد (`backupMu` و بعد `mutationMu`) و اول audit فعلی را verify می‌کند، پس state و audit از یک لحظه‌اند که هیچ تغییری در جریان نیست، یعنی همان مرزی که `RestoreFromFile` در آن تصمیم می‌گیرد. جواب مربوط به همان لحظه است: BCC بعدش هم تغییر می‌کند. A5 این عملیات فقط‌خواندنی را برای Admin احراز هویت‌شده expose می‌کند، بدون اینکه مسیر دلخواه فایل‌سیستم را قبول کند.

## A5 — پیش‌نمایش Restore برای Admin

وقتی backup رمزگذاری‌شده فعال باشد، BCC API ادمین را به همان `--backup-dir` و همان کلیدی که حلقه backup استفاده می‌کند محدود می‌کند:

<div dir="ltr" align="left">

```text
GET  /api/backups
POST /api/backups/restore-preview   {"filename":"daily-...baftbak"}
```

</div>

هر دو endpoint فقط با احراز هویت Admin کار می‌کنند. فهرست فقط فایل‌های regular با پسوند `.baftbak` را نشان می‌دهد. Preview فقط **نام فایل** داخل همان پوشه را می‌پذیرد؛ path traversal، زیرپوشه و symlink رد می‌شوند. کلید backup فقط در حافظه نگه داشته می‌شود و هرگز در پاسخ API برنمی‌گردد. تلاش‌های preview با action `backup.restore.preview` و فقط metadata غیرمحرمانه audit می‌شوند.

این API فقط preview است و هیچ restore یا mutation روی state انجام نمی‌دهد.

## A6 — Restore محلی از CLI

Restore واقعی عمداً فقط **به‌صورت local و offline روی میزبان BCC** در دسترس است:

<div dir="ltr" align="left">

```bash
export BAFT_BCC_BACKUP_KEY=...
baft-bcc restore --backup backups/daily-20261002T000000Z.baftbak --state-file bcc-state.json
# preview only; exits 4 because explicit confirmation is still required

baft-bcc restore --backup backups/daily-20261002T000000Z.baftbak --state-file bcc-state.json --yes
# repeats all safety checks and commits the transactional restore
```

</div>

فرمان باید lock فایل state را بگیرد؛ بنابراین اگر BCC در حال اجرا باشد restore رد می‌شود. ابتدا همان preview رسمی اجرا می‌شود و backup خراب/نامعتبر، audit فعلی نامعتبر، anchor ناسازگار، state قدیمی که هنوز migration لازم دارد، یا journal نیمه‌کاره رد می‌شود. بدون `--yes` هیچ mutation انجام نمی‌شود. با `--yes`، تابع `RestoreFiles` دوباره lock را می‌گیرد و backup/audit/anchor را مجدداً بررسی می‌کند و فقط بعد وارد restore تراکنشی و fault-tested موجود می‌شود.

کد خروج: `0` = commit موفق، `1` = خطای ایمنی/verify/restore، `2` = استفاده یا تنظیم کلید اشتباه، `4` = preview معتبر است ولی confirmation داده نشده. گزینه `--json` preview و وضعیت commit را ماشین‌خوانا می‌دهد.

## آنچه اینجا نیست

عمداً **هیچ Remote Restore API** و هیچ restore زنده از داخل Admin UI/API اضافه نشده است. A5 فقط preview از API است؛ A6 نیازمند دسترسی محلی به میزبان، BCC متوقف، کلید backup و `--yes` صریح است.

</div>
