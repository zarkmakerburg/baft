<div dir="rtl" align="right" lang="fa">

# مستندات BAFT

[English](../en/README.md) · [README پروژه](../../README.md)

این فهرست مستندات فنی BAFT است. برای هر سند مشخص می‌کند از چه نوعی است و برای چه کسی لازم است تا خواننده بدون خواندن همه‌چیز به منبع مرجع برسد.

## ۱. مسیرهای مطالعه

| اگر شما… | به این ترتیب بخوانید |
|---|---|
| پروژه را ارزیابی می‌کنید | [README پروژه](../../README.md) ← [01 معرفی](01-overview.md) ← [03 مدل امنیتی](03-security-model.md) ← [STATUS](../../STATUS.md) ← [KNOWN-LIMITATIONS](../../KNOWN-LIMITATIONS.md) |
| سرورها را اداره می‌کنید | [06 اجرای IR/EX](06-running-ir-ex.md) ← [23 ریلیز امضاشده](23-p1a-signed-releases.md) ← [24 دسترسی BCC](24-p1c-bcc-access.md) ← [26 SSH bootstrap](26-p1d-ssh-bootstrap.md) ← [27 سازندهٔ تونل](27-p1e-tunnel-builder.md) |
| صفحهٔ داده را توسعه می‌دهید | [02 معماری](02-architecture.md) ← [04 پروتکل](04-protocol-baft1.md) ← [05 پیکربندی](05-configuration.md) ← [07 کنترل منابع](07-resource-control.md) ← [10 ساختار مخزن](10-repository-layout.md) ← [08 آزمون و CI](08-testing-and-ci.md) |
| صفحهٔ کنترل را توسعه می‌دهید | [22 نقشهٔ راه Launch-1](22-launch-1-roadmap.md) ← [23](23-p1a-signed-releases.md) ← [24](24-p1c-bcc-access.md) ← [25 jobهای agent](25-p1d-agent.md) ← [26](26-p1d-ssh-bootstrap.md) ← [27](27-p1e-tunnel-builder.md) |
| ادعاهای پژوهشی را بازبینی می‌کنید | [12 روش](12-innovation-method.md) ← [13 TWRL](13-stage-c-twrl.md) ← [14 PADL](14-stage-c-padl.md) ← [15D دروازهٔ ECRL](15-stage-d-ecrl.md) ← [17 دروازه‌ها](17-correctness-gates.md) ← [18 soak](18-stage-c-soak.md) |

## ۲. فهرست اسناد

نوع: **مشخصات** رفتاری را تعریف می‌کند که کد و تست باید با آن بخواند · **راهنما** توضیح و آموزش می‌دهد · **سابقه** تصمیم، برنامه یا نتیجه‌ای را ثبت می‌کند · **پژوهشی** فرضیه را با معیار ابطال بیان می‌کند · **منسوخ** برای تاریخچه نگه داشته شده · **آزمایشی** جزو هستهٔ پشتیبانی‌شده نیست.

| شناسه | سند | نوع | مخاطب |
|---|---|---|---|
| 01 | [معرفی، هدف و مرز پروژه](01-overview.md) | راهنما | همه |
| 02 | [معماری، نقش‌ها و جریان داده](02-architecture.md) | مشخصات | توسعه‌دهندگان |
| 03 | [مدل امنیت، هویت و مرز اعتماد](03-security-model.md) | مشخصات | همه |
| 04 | [پروتکل BAFT/1 و state machine](04-protocol-baft1.md) | مشخصات | توسعه‌دهندگان |
| 05 | [پیکربندی و Routeها](05-configuration.md) | مشخصات | اپراتورها، توسعه‌دهندگان |
| 06 | [ساخت و اجرای IR/EX](06-running-ir-ex.md) | راهنما | اپراتورها |
| 07 | [کنترل حافظه، backpressure و زمان‌بندی](07-resource-control.md) | مشخصات | توسعه‌دهندگان |
| 08 | [آزمون‌ها، CI و معیار پذیرش](08-testing-and-ci.md) | راهنما | توسعه‌دهندگان |
| 09 | [نقشهٔ راه و مراحل A تا H](09-roadmap.md) | سابقه | همه |
| 10 | [ساختار کد و مسئولیت ماژول‌ها](10-repository-layout.md) | راهنما | توسعه‌دهندگان |
| 11 | [فرهنگ واژگان](11-glossary.md) | راهنما | همه |
| 12 | [روش‌شناسی نوآوری](12-innovation-method.md) | سابقه | بازبین‌ها |
| 13 | [TWRL: دفتر دریافت سه‌سطحی](13-stage-c-twrl.md) | پژوهشی | بازبین‌ها |
| 14 | [PADL: اجارهٔ deficit با aging فشار](14-stage-c-padl.md) | پژوهشی | بازبین‌ها |
| 15 | [Conservation telemetry و بودجهٔ چند Shard](15-conservation-telemetry.md) | مشخصات | توسعه‌دهندگان |
| 15D | [ECRL: prior art، مدل تهدید و invariantها](15-stage-d-ecrl.md) | پژوهشی | بازبین‌ها |
| 16 | [Metricهای حفاظتی و conservation](16-conservation-metrics.md) | مشخصات | توسعه‌دهندگان |
| 16R | [R1/R2: Reachability و SecurityInternal](16-reachability-innovation.md) | پژوهشی | بازبین‌ها |
| 17 | [دروازه‌های صحت single-flight](17-correctness-gates.md) | سابقه | توسعه‌دهندگان |
| 18 | [دروازهٔ soak مرحلهٔ C](18-stage-c-soak.md) | سابقه | توسعه‌دهندگان |
| 19 | [قرنطینهٔ محدود وضعیت پایانی Flow](19-terminal-quarantine.md) | مشخصات | توسعه‌دهندگان |
| 20 | [یادداشت ECRL](20-stage-d-ecrl.md) | منسوخ | — |
| 21 | [Record shaping / morphing (R3.1)](../en/21-stealth-pro.md) (فقط انگلیسی) | آزمایشی | بازبین‌ها |
| 22 | [نقشهٔ راه Launch-1](22-launch-1-roadmap.md) | سابقه | همه |
| 23 | [P1-A: artifactهای ریلیز امضاشده](23-p1a-signed-releases.md) | مشخصات | اپراتورها، توسعه‌دهندگان |
| 24 | [P1-C: دسترسی وب BCC و state با SQLite](24-p1c-bcc-access.md) | مشخصات | اپراتورها، توسعه‌دهندگان |
| 25 | [P1-D: jobهای امن agent](25-p1d-agent.md) | مشخصات | اپراتورها، توسعه‌دهندگان |
| 26 | [P1-D: فهرست سرورها و SSH bootstrap](26-p1d-ssh-bootstrap.md) | مشخصات | اپراتورها، توسعه‌دهندگان |
| 27 | [P1-E: سازندهٔ بومی تونل](27-p1e-tunnel-builder.md) | مشخصات | اپراتورها، توسعه‌دهندگان |

ثبت تصمیم‌ها: [ADR-0001 قرارداد پروژه](../adr/0001-project-contract.md) · [0002 toolchain](../adr/0002-toolchain.md) · [0003 Carrier ‏HTTP/2 + mTLS](../adr/0003-h2-mtls-carrier.md) · [0004 معنای Flow](../adr/0004-stage-b-flow-semantics.md) · [0005 منابع مرحلهٔ C](../adr/0005-stage-c-resources.md) · [0006 زمان‌بندی کنترل](../adr/0006-control-scheduling.md) · [0007 Noise با کلید pin‌شده](../adr/0007-pinned-noise-morphing.md). قرارداد سیم: [یادداشت‌های BAFT/1](../protocol/baft1.md) و [golden vectors](../protocol/golden-vectors.json).

## ۳. سلسله‌مراتب منابع

وقتی میان اسناد اختلاف هست، منبع با رتبهٔ بالاتر برنده است.

| رتبه | منبع | حاکم بر |
|---:|---|---|
| ۱ | Blueprint محتوایی 1.4 و Implementation Master Prompt 1.2 | نیت و دامنه |
| ۲ | ADRهای پذیرفته‌شده | تصمیم‌های پیاده‌سازی |
| ۳ | schema، wire tests و golden vectors | قراردادهای قابل‌اجرا |
| ۴ | [STATUS](../../STATUS.md) و [TEST-RESULTS](../../TEST-RESULTS.md) | آنچه ساخته و اندازه‌گیری شده |
| ۵ | همین راهنماها | توضیح |

هر قابلیتی که در Blueprint آمده ولی در STATUS به‌عنوان پیاده‌شده ثبت نشده، **قرارداد برنامه‌ریزی‌شده** است، نه قابلیت موجود.

## ۴. قراردادها

| موضوع | قاعده |
|---|---|
| زبان هنجاری | ‏MUST، MUST NOT، SHOULD و MAY مطابق RFC 2119 هستند. |
| شواهد | ادعای اندازه‌گیری‌شده به یک اجرای CI، تست یا نتیجهٔ ثبت‌شده ارجاع می‌دهد؛ هیچ عددی تخمینی نیست. نتایج روی CI یا loopback شواهد درستی هستند، نه benchmark شبکهٔ عمومی. |
| وضعیت ادعا | نتیجهٔ مهندسی، فرضیهٔ پژوهشی و نوآوری حقوقی وضعیت‌های جدا هستند ([12](12-innovation-method.md)). |
| شماره‌گذاری | اسناد به ترتیب ایجاد شماره دارند. `15` (conservation telemetry) و `15D` (دروازهٔ طراحی ECRL) به دلیل تاریخی یک شماره دارند و `20` را `15D` منسوخ کرده است. |
| زبان‌ها | درخت فارسی و انگلیسی موازی نگه داشته می‌شود. استثناها: `21` فقط انگلیسی و `16-reachability-innovation.md` فقط فارسی است. |

</div>
