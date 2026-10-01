<div dir="rtl" align="right" lang="fa">

# 22 — نقشه‌راه Launch-1 (دامنهٔ مصوب)

وضعیت: دامنه در ۲۰۲۶-۱۰-۰۱ پس از بررسی HQ روی Roadmap مشورتی، توسط صاحب پروژه تأیید شد. هر قدم زیر یک Mission جدا می‌شود (Scope، Non-scope، invariantها، تست، Exit Criteria، Rollback) و قبل از شروع جداگانه تأیید می‌شود.

## مبنا

- Freeze Candidate: کامیت `e38dd6a` روی `release-v1-goldapp`. Push CI، PR CI، Stage-C soak و recovery soak (همراه گیت churn) همه روی همین SHA پاس شدند. اعلام Core Freeze تصمیم صاحب پروژه است.
- آنچه امروز هست:
  - باینری‌های BAFT؛
  - `install.sh` که از سورس build می‌کند؛
  - pairing که `baft.yaml` هر دو طرف را می‌سازد؛
  - systemd و E2E با ترافیک واقعی در CI؛
  - BCC با monitoring، finance، audit، backup، revocation و چرخش توکن.
- آنچه هنوز نیست:
  - ورود BCC فقط یک توکن ثابت admin است؛ کاربر، session و مسیر مخفی ندارد.
  - BCC jobهای `deploy_baft` و `enroll_peer` را می‌سازد و `/api/agent/jobs` را ارائه می‌کند، ولی هیچ Agentی در ریپو آن‌ها را اجرا نمی‌کند.
  - Release امضاشده هم وجود ندارد.

## قدم‌های Launch-1

| قدم | محتوا | پیش‌نیاز |
|---|---|---|
| P1-A | Release امضاشده (baft، baft-pair، baft-bcc؛ amd64 و arm64؛ SHA256SUMS، امضا، manifest، provenance) و branch protection روی `main` و `release-v1-goldapp` | Core Freeze؛ تصمیم دربارهٔ کلید امضا |
| P1-B | Installer که فقط Release تأییدشده نصب کند (بدون Go، git و ابزار build روی سرور) و CLI حداقلی `baft`: `status`، `doctor`، `logs` | P1-A |
| P1-C | دسترسی BCC: مسیر مخفی تصادفی، username تصادفی و یکتا، password قوی (فقط hash)؛ چرخش هر سه فقط از console همراه با ابطال همهٔ sessionها؛ rate limit؛ چرخهٔ HTTPS. HTTP فقط روی localhost یا با تأیید صریح. state از فایل JSON به SQLite نسخه‌دار با migration منتقل شود | P1-B |
| P1-D | Agent امن: pull-based، فقط کارهای allowlist، jobهای امضاشده با کلید BCC که با کلید pin‌شده بررسی می‌شوند، update فقط برای باینری امضاشدهٔ Release. Inventory و SSH Bootstrap با host-key pinning؛ credential بوت‌استرپ بعد از enrollment نابود شود | P1-C |
| P1-E | Tunnel Builder بومی: plan، validate، prepare دو طرف، commit، health check، rollback هر دو طرف در صورت شکست. Health پایه، backup و update امن با rollback | P1-D |
| P1-F | E2E کامل Launch-1 روی یک RC SHA دقیق | همه |

## به P2 منتقل شد

- قرارداد Driver برای موتورهای خارجی، سپس اولین adapter. هر موتور شخص‌ثالث بررسی امنیت، لایسنس و نگهداری جداگانه می‌خواهد.
- Network Autotune. در Launch-1 فقط تشخیص فقط‌خواندنی در `baft doctor` هست.
- Canary، RBAC، 2FA/Passkey، control plane چندمنطقه‌ای و recovery بعد از ری‌استارت پروسه.

## تصمیم‌های باز صاحب پروژه

- اعلام Core Freeze روی Candidate SHA.
- محل نگهداری و چرخش کلید ریشهٔ امضای Release و jobها.
- تنظیمات branch protection.
- `ca.key` که فعلاً روی EX می‌ماند.

## اصول حفظ‌شده از سند مشورتی

- اول سرورها، بعد تونل‌ها.
- BAFT Native موتور اصلی است.
- Recovery بومی و failover بین موتورها دو چیز جدا هستند و جدا نمایش داده می‌شوند.
- هیچ Transportی با ادعای «همه‌جا کار می‌کند» عرضه نمی‌شود: اول اندازه‌گیری، بعد امتیاز، بعد انتخاب.

</div>
