<div dir="rtl" align="right" lang="fa">

# 23 — P1-A: آرتیفکت‌های امضاشدهٔ Release

وضعیت: پیاده‌سازی برای تأیید صاحب پروژه پیشنهاد شده است. قدم P1-A از [22-launch-1-roadmap.md](22-launch-1-roadmap.md). نسخهٔ کامل جزئیات فنی در [نسخهٔ انگلیسی](../en/23-p1a-signed-releases.md) است.

## Scope

- Build تکرارپذیر `baft`، `baft-pair` و `baft-bcc` برای linux/amd64 و linux/arm64 (`scripts/release/build.sh`).
- Release امضاشده: `SHA256SUMS`، manifest امضاشده (`manifest.json`) همراه با provenance، و گواهی کلید release (`release-key.cert.json`).
- کلید دوسطحی طبق تصمیم ۲۰۲۶-۱۰-۰۱: کلید Root آفلاین (Ed25519) کلید امضای release را که در CI است گواهی می‌کند. سرورها فقط کلید عمومی Root را pin می‌کنند.
- ابزار `baft-release` با فرمان‌های `keygen`، `keyid`، `certify`، `revoke`، `sign`، `verify`.
- Workflow `release`: روی tag `v*` می‌سازد، امضا می‌کند، با Root pinشده verify می‌کند و یک draft release می‌سازد.
- Job `release-dry-run` در CI که کل مسیر را با کلیدهای یک‌بارمصرف تمرین می‌کند.
- Branch protection: rulesetهای فعال از ۲۰۲۶-۱۰-۰۱.

## Non-scope

- نصب از روی release و دریافت فهرست ابطال روی سرور (P1-B؛ حالا در `install.sh`).
- verify به‌روزرسانی agent (P1-D).
- Sigstore یا attestation گیت‌هاب؛ بعداً قابل افزودن است ولی ریشهٔ اعتماد نیست.

## قواعد verify

Release فقط وقتی پذیرفته می‌شود که: گواهی با Root pinشده امضا شده باشد؛ فهرست ابطالِ امضاشده با Root داده شده باشد، منقضی نشده باشد و `sequence` آن از مقدار ثبت‌شده در trust state کمتر نباشد؛ کلید release در آن فهرست نباشد؛ manifest با کلید گواهی‌شده امضا شده باشد؛ زمان امضای manifest داخل بازهٔ اعتبار گواهی باشد؛ `SHA256SUMS` با manifest بخواند؛ و هر فایل پوشه دقیقاً همان آرتیفکت امضاشده با همان اندازه و hash باشد. با trust state (`-state`)، نسخهٔ قدیمی‌تر از نسخهٔ پذیرفته‌شده رد می‌شود مگر با `-allow-downgrade`، و همان نسخه از commit دیگر هرگز پذیرفته نمی‌شود.

## Trust state (جلوگیری از downgrade)

هر سرور فایل `/opt/baft/release-state.json` (`$BAFT_PREFIX/release-state.json`، با مالک root) را نگه می‌دارد: نسخه و commit پذیرفته‌شده و بیشترین `sequence` فهرست ابطال. `install.sh` قبل از نصب آن را بررسی و بعد از نصب به‌روز می‌کند ([06-running-ir-ex.md](06-running-ir-ex.md)). `baft-release verify -state` هم همین قالب فایل را به کار می‌برد. `sequence` در state هرگز پایین نمی‌آید. فهرست ابطال اجباری است و تاریخ انقضا دارد، پس مهاجم نه می‌تواند آن را حذف کند و نه فهرست قدیمی را بعد از انقضا یا بعد از دیدن `sequence` بالاتر دوباره بدهد.

## Invariantها

- هیچ کلید خصوصی وارد مخزن نمی‌شود.
- چیزی منتشر نمی‌شود که در همان اجرا با `release/keys/root.pub` verify نشده باشد.
- Release به‌صورت draft ساخته می‌شود و انتشار با صاحب پروژه است.
- Tag روی commitی خارج از `main` / `release-v1-goldapp` release نمی‌سازد.

## راه‌اندازی یک‌باره توسط صاحب پروژه

روی ماشین آفلاین: `keygen` برای root و release، سپس `certify` (دستورها در نسخهٔ انگلیسی). فهرست ابطال اولیه (خالی) را هم امضا کنید: `baft-release revoke -root-key root.key -valid-days 180 -out revocations.json`. بعد `root.pub` و `revocations.json` را با PR در `release/keys/` بگذارید و همان کلید Root را در `BAFT_PINNED_ROOT_PUB` داخل `install.sh` قرار دهید (اگر این دو فرق کنند `tests/docs` رد می‌شود)، در گیت‌هاب environment به نام `release` (محدود به tagهای `v*`) با secretهای `BAFT_RELEASE_SIGNING_KEY` و `BAFT_RELEASE_KEY_CERT` بسازید، و `release.key` را از ماشین آفلاین پاک کنید.

## چرخش و ابطال

- چرخش: کلید release جدید، گواهی با Root، جایگزینی secretها. سرورها تغییری لازم ندارند.
- ابطال: `baft-release revoke -in release/keys/revocations.json -key-id <id> -out revocations.json` و commit آن. همیشه `-in` بدهید تا `sequence` بالا برود.
- تمدید: قبل از `expires_at` فهرست را بدون تغییر با `revoke -in ...` دوباره امضا و commit کنید. فهرست منقضی، release و نصب را تا تمدید متوقف می‌کند.

## تست‌ها و Exit Criteria

- `go test ./internal/release` و `scripts/release/dry_run.sh` (شامل بررسی تکرارپذیری build و رد آرتیفکت دستکاری‌شده، Root اشتباه، کلید باطل‌شده، کلید بدون گواهی، نبودِ فهرست ابطال، فهرست تکراری قدیمی و downgrade).
- CI سبز، و یک tag آزمایشی که draft release آن روی ماشین دیگری verify شود.

## Rollback

حذف draft release و tag. تغییر افزایشی است و revert کردن PR هیچ کد runtime را تغییر نمی‌دهد.

</div>
