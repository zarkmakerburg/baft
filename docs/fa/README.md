# راهنمای مستندات BAFT

[English documentation](../en/README.md) | [README اصلی فارسی](../../README.md)

این مجموعه برای خواننده‌ای نوشته شده که ممکن است هیچ شناخت قبلی از BAFT نداشته باشد. ترتیب پیشنهادی مطالعه:

1. [01 — معرفی، هدف و مرز پروژه](01-overview.md)
2. [02 — معماری، نقش‌ها و جریان داده](02-architecture.md)
3. [03 — مدل امنیت، هویت و مرز اعتماد](03-security-model.md)
4. [04 — پروتکل BAFT/1 و state machine](04-protocol-baft1.md)
5. [05 — پیکربندی و Routeها](05-configuration.md)
6. [06 — ساخت و اجرای IR/EX](06-running-ir-ex.md)
7. [07 — کنترل حافظه، backpressure و زمان‌بندی](07-resource-control.md)
8. [08 — آزمون‌ها، CI و معیار پذیرش](08-testing-and-ci.md)
9. [09 — نقشه راه و مراحل A تا H](09-roadmap.md)
10. [10 — ساختار کد و مسئولیت ماژول‌ها](10-repository-layout.md)
11. [11 — فرهنگ واژگان](11-glossary.md)

## سلسله‌مراتب منابع

وقتی میان اسناد اختلاف وجود دارد، این ترتیب را رعایت کنید:

1. Blueprint محتوایی 1.4 و Implementation Master Prompt 1.2؛
2. ADRهای پذیرفته‌شده برای تصمیم‌های پیاده‌سازی؛
3. schema، wire tests و golden vectors برای قرارداد قابل‌اجرا؛
4. STATUS و TEST-RESULTS برای آنچه واقعاً ساخته و آزمایش شده؛
5. این راهنماها برای توضیح و آموزش.

هر قابلیتی که در Blueprint آمده ولی در STATUS به‌عنوان پیاده‌شده ثبت نشده، **برنامه یا قرارداد آینده** است و نباید قابلیت موجود فرض شود.
