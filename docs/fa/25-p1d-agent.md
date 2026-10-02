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

## خود agent (`baft-agent`)

- هر ۳۰ ثانیه jobها را از BCC می‌گیرد. آدرس BCC باید `https://` باشد و فایل توکن فقط برای مالک قابل دسترس.
- فقط jobی را اجرا می‌کند که `agentjob.Verifier` بپذیرد، شناسهٔ آن را **پیش از** اجرا ثبت می‌کند تا بعد از crash دوباره اجرا نشود، و نتیجه را به BCC گزارش می‌دهد.
- `health` فرمان `baft doctor` را اجرا می‌کند؛ `restart` و `reload` بعد از اجرا بررسی می‌کنند سرویس واقعاً فعال باشد.
- `update_baft`: release را دانلود می‌کند، با **کلید Root release** (نه کلید BCC)، فهرست ابطال و trust state نصب (بدون downgrade و re-tag) verify می‌کند، باینری‌های فعلی را به‌صورت `.prev` نگه می‌دارد، جایگزین و restart می‌کند، و اگر سرویس بالا نیاید به نسخهٔ قبلی برمی‌گرداند و شکست را گزارش می‌دهد.
- `baft-agent` حالا جزو آرتیفکت‌های امضاشدهٔ release است.

تست‌ها با یک BCC واقعی و release امضاشدهٔ محلی: نصب موفق، rollback وقتی سرویس بالا نمی‌آید، بی‌اثر بودن release دستکاری‌شده یا قدیمی‌تر، رد job با کلید pin‌نشده، و actionهای restart/reload/health.

## ثبت سرور (`install.sh --agent-only`)

اول سرور، بعد تونل: سرور جدید فقط باینری‌های BAFT و agent را می‌گیرد و هنوز config تونل ندارد.

<div dir="ltr" align="left">

```bash
sudo BAFT_BCC_JOB_KEY="$(baft-bcc jobkey show)" BAFT_AGENT_TOKEN_FILE=/root/agent-token \
  bash install.sh --agent-only --bcc-url https://bcc.example.com --node-id ex-1
```

</div>

- `baft`، `baft-pair` و `baft-agent` را از release امضاشده نصب و verify می‌کند.
- توکن agent (فقط root، 0600)، کلید pin‌شدهٔ BCC و کلید Root release را در `/etc/baft-agent/` می‌نویسد و `baft-agent.service` را با محدودیت‌های systemd (فقط مسیر باینری‌ها، `$BAFT_PREFIX` و state agent قابل نوشتن) راه می‌اندازد.
- آدرس BCC باید `https://` باشد.
- job CI به نام `e2e-agent-enroll` این مسیر را با یک BCC واقعی روی runner دارای systemd اجرا می‌کند.

راه‌اندازی با SSH از BCC (قدم بعد) دقیقاً همین فرمان را روی سرور جدید اجرا می‌کند.

</div>
