<div dir="rtl" align="right" lang="fa">

# 25 — P1-D: jobهای امن agent

قدم P1-D از [22-launch-1-roadmap.md](22-launch-1-roadmap.md). بخش اول (این سند): BCC هر job را امضا می‌کند و agent پیش از اجرای هر job این قواعد را بررسی می‌کند (`internal/agentjob`). باینری agent، فهرست سرورها و راه‌اندازی با SSH بعداً می‌آیند. جزئیات در [نسخهٔ انگلیسی](../en/25-p1d-agent.md).

## اعتماد

- BCC یک کلید Ed25519 برای امضای job دارد: `<state-file>.job-key` (فقط برای مالک، در اولین اجرا ساخته می‌شود؛ `baft-bcc jobkey show` کلید عمومی را نشان می‌دهد).
- agent هنگام enrollment همین کلید عمومی را pin می‌کند. این کلید با کلید Root release فرق دارد: BCC نفوذشده نمی‌تواند release امضا کند و agent فقط releaseی را نصب می‌کند که با Root release تأیید شود.

## agent چه چیزی را اجرا می‌کند

فقط jobی که: امضایش با کلید pin‌شدهٔ BCC درست باشد؛ دقیق و بدون فیلد ناشناخته parse شود؛ برای همین نود باشد؛ action آن در فهرست مجاز باشد (`health`، `restart`، `reload`، `update_baft` با نسخهٔ `vX.Y.Z`، `enroll_peer`) و دقیقاً پارامترهای مجاز را داشته باشد؛ منقضی نشده باشد (BCC یک ساعت اعتبار می‌دهد، حداکثر ۲۴ ساعت)؛ و قبلاً اجرا نشده باشد.

هیچ action ای برای اجرای shell یا فرمان دلخواه وجود ندارد. BCC همین بررسی‌ها را قبل از امضا انجام می‌دهد. deploy حالا فقط نسخه‌ای با شکل tag release (`vX.Y.Z`) می‌پذیرد.

## تست‌ها

`internal/agentjob` و `internal/bcc/jobsign_test.go`: پذیرش job درست؛ رد action shell، پارامتر ناقص یا اضافه یا نادرست، کلید اشتباه، نود اشتباه، job منقضی یا آینده، تکراری، دستکاری‌شده و envelope با نوع اشتباه؛ jobهای API با envelopeی که agent می‌پذیرد؛ کلید job فقط برای مالک.

</div>
