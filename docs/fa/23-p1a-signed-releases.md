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

- نصب از روی release و دریافت فهرست ابطال روی سرور (P1-B).
- verify به‌روزرسانی agent (P1-D).
- Sigstore یا attestation گیت‌هاب؛ بعداً قابل افزودن است ولی ریشهٔ اعتماد نیست.

## قواعد verify

Release فقط وقتی پذیرفته می‌شود که: گواهی با Root pinشده امضا شده باشد؛ کلید release باطل نشده باشد؛ manifest با کلید گواهی‌شده امضا شده باشد؛ زمان امضای manifest داخل بازهٔ اعتبار گواهی باشد؛ `SHA256SUMS` با manifest بخواند؛ و هر فایل پوشه دقیقاً همان آرتیفکت امضاشده با همان اندازه و hash باشد.

## Invariantها

- هیچ کلید خصوصی وارد مخزن نمی‌شود.
- چیزی منتشر نمی‌شود که در همان اجرا با `release/keys/root.pub` verify نشده باشد.
- Release به‌صورت draft ساخته می‌شود و انتشار با صاحب پروژه است.
- Tag روی commitی خارج از `main` / `release-v1-goldapp` release نمی‌سازد.

## راه‌اندازی یک‌باره توسط صاحب پروژه

روی ماشین آفلاین: `keygen` برای root و release، سپس `certify` (دستورها در نسخهٔ انگلیسی). بعد `root.pub` را با PR در `release/keys/root.pub` بگذارید، در گیت‌هاب environment به نام `release` (محدود به tagهای `v*`) با secretهای `BAFT_RELEASE_SIGNING_KEY` و `BAFT_RELEASE_KEY_CERT` بسازید، و `release.key` را از ماشین آفلاین پاک کنید.

## چرخش و ابطال

- چرخش: کلید release جدید، گواهی با Root، جایگزینی secretها. سرورها تغییری لازم ندارند.
- ابطال: `baft-release revoke` و انتشار فهرست؛ P1-B سرورها را وادار به دریافت آن می‌کند.

## تست‌ها و Exit Criteria

- `go test ./internal/release` و `scripts/release/dry_run.sh` (شامل بررسی تکرارپذیری build و رد آرتیفکت دستکاری‌شده، Root اشتباه، کلید باطل‌شده و کلید بدون گواهی).
- CI سبز، و یک tag آزمایشی که draft release آن روی ماشین دیگری verify شود.

## Rollback

حذف draft release و tag. تغییر افزایشی است و revert کردن PR هیچ کد runtime را تغییر نمی‌دهد.

</div>
