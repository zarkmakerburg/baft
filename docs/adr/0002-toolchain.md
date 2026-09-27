<div dir="rtl" align="right" lang="fa">

# ADR-0002 — Toolchain زبان Go

> English: [en/0002-toolchain.md](en/0002-toolchain.md)  
> وضعیت: پذیرفته‌شده  
> تاریخ: 2026-09-27

## تصمیم

نسخه هدف مخزن `Go 1.27.1` است و از `go.mod` خوانده می‌شود.

GitHub Actions نسخه pin‌شده را نصب و تست‌ها را روی همان toolchain اجرا می‌کند. بنابراین نتیجه compatibility smoke روی نسخه‌های قدیمی‌تر جای gate اصلی را نمی‌گیرد.

## کنترل dependency

`go.mod` و `go.sum` باید reproducible بمانند. CI:

<div dir="ltr" align="left">

```bash
go mod tidy
git diff --exit-code -- go.mod go.sum
```

</div>

را اجرا می‌کند تا drift dependency وارد branch نشود.

جزئیات checksum در [dependency-lock.md](../../dependency-lock.md) ثبت شده است.

</div>
