# 10 — ساختار کد و مسئولیت ماژول‌ها

```text
cmd/baft/                 CLI و entry point
configs/                  schema و نمونه پیکربندی
internal/config/          parsing و validation config
internal/identity/        TLS، هویت و revocation
internal/carrier/h2/      Carrier پایه HTTP/2
internal/carrier/h3/      رزرو برای مسیر آزمایشی آینده
internal/protocol/        BAFT/1 frame/control codec
internal/session/         Session، Flow state و outbound sender
internal/flow/            محل توسعه logic جداشده Flow در آینده
internal/scheduler/       DRR و سیاست زمان‌بندی
internal/resources/       allocator و queue budget
internal/routes/          Route table و authorization مقصد
internal/health/          health/failover آینده
internal/admin/           management API آینده
internal/metrics/         metrics آینده
internal/policy/          Profile/policy آینده
internal/logging/         logging support
internal/update/          update/operations آینده
tests/integration/        تست componentهای واقعی کنار هم
tests/correctness/        gateهای حجیم صحت
tests/fuzz/               fuzz targets
tests/chaos/              fault/chaos آینده
bench/                    benchmark و raw results
research/                 آزمایش‌های پژوهشی
deploy/systemd/           واحدهای systemd آینده
packaging/                بسته‌بندی
docs/adr/                 تصمیم‌های معماری
docs/protocol/            قراردادهای wire و vectors
docs/fa/                  مستندات فارسی
docs/en/                  مستندات انگلیسی
```

## اصل dependency direction

ماژول‌های پایین‌دستی نباید مرز اعتماد را دور بزنند. مثال:

- `protocol` نباید target dial کند.
- `routes` نباید payload protocol را parse کند.
- `scheduler` نباید مفهوم payload را بداند.
- `carrier` نباید authorization Route را انجام دهد.
- `policy` نباید TLS verification را خاموش کند.

## فایل‌های وضعیت

- `PLAN.md`: چه کارهایی باید انجام شوند و کدام تمام شده‌اند.
- `STATUS.md`: وضعیت واقعی فعلی.
- `BLOCKERS.md`: مانع‌های حل‌شده/باز.
- `TEST-RESULTS.md`: شواهد اجرای واقعی.
- `KNOWN-LIMITATIONS.md`: محدودیت‌های فعلی.
- `dependency-lock.md`: وابستگی‌ها و checksum.
- `novelty-matrix.md`: ادعاهای پژوهشی و وضعیت شواهد.

## ADR

ADR باید توضیح دهد:

1. مسئله چه بوده؛
2. تصمیم چیست؛
3. چرا این تصمیم انتخاب شده؛
4. trade-off چیست؛
5. چه تستی از آن محافظت می‌کند؛
6. آیا تصمیم provisional یا accepted است.

تغییر wire format، trust boundary، resource limit و scheduling policy بدون ADR/test مناسب نباید فقط به شکل patch خام وارد شود.
