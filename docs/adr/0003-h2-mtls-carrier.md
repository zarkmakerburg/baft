<div dir="rtl" align="right" lang="fa">

# ADR-0003 — Carrier پایه HTTP/2 + mTLS

> English: [en/0003-h2-mtls-carrier.md](en/0003-h2-mtls-carrier.md)  
> وضعیت: پذیرفته‌شده  
> تاریخ: 2026-09-27

## تصمیم

Carrier پایه از TLS کتابخانه استاندارد Go و HTTP/2 در `net/http` استفاده می‌کند.

قواعد:

- TLS حداقل 1.3؛
- mTLS اجباری؛
- hostname verification فعال؛
- identity سمت client از URI SAN گواهی تأییدشده؛
- peer allowlist جدا از CA trust؛
- redirect غیرفعال؛
- environment proxy برای Carrier غیرفعال؛
- HTTP/1 fallback خودکار وجود ندارد؛
- مسیر `/baft/v1/carrier` ثابت است و secret نیست؛
- هر Shard `http.Transport` مستقل دارد؛
- response header پیش از تکمیل request body flush می‌شود؛
- server-side Carrier write نیز flush می‌شود؛
- cancellation بخشی از قرارداد است.

## دلیل

این انتخاب baseline را dependency-minimal و قابل‌آزمون نگه می‌دارد و قبل از ورود H3 یا relay، correctness دوطرفه را روی transport ساده‌تر ثابت می‌کند.

</div>
