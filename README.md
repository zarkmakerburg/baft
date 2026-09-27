# BAFT — بافت

BAFT یک پروژه پژوهشی برای انتقال امن TCP میان دو عامل تحت کنترل کاربر است. مرجع محصول، Blueprint محتوایی 1.4 و Implementation Master Prompt 1.2 مورخ 2026-09-27 است.

> وضعیت فعلی: Stage A spike پیاده‌سازی شده و Stage B vertical slice در حال تکمیل است. این ریپازیتوری production-ready نیست و هیچ ادعای «غیرقابل‌تشخیص بودن» یا «اتصال در هر شرایطی» ندارد.

## اصول سخت

- TLS 1.3 + mTLS؛ بدون plaintext fallback.
- هویت peer از certificate معتبر و URI SAN گرفته می‌شود؛ اعتماد به CA به‌تنهایی مجوز route نیست.
- مقصدها فقط از routeهای ثابت و allowlisted resolve می‌شوند؛ OPEN مقصد دلخواه حمل نمی‌کند.
- frame parser محدود و bounded است و flags/reserved ناشناخته را رد می‌کند.
- OPEN/DATA قبل از READY دوطرفه پذیرفته نمی‌شوند.
- نتیجه تست یا benchmark ساختگی ممنوع است.
- قابلیت‌های experimental مثل H3 و Cloudflare Worker از core جدا می‌مانند.

## Toolchain

نسخه هدف در `go.mod`: Go 1.27.1.

محیط bootstrap فعلی Go 1.23.2 دارد و امکان دانلود toolchain جدید ندارد. برای smoke-test، یک کپی موقت با تغییر صرفاً `go` directive به 1.23.2 اجرا شده است؛ سورس اصلی همچنان روی 1.27.1 pin است.

## آنچه اکنون وجود دارد

- BAFT/1 frame codec و golden vectors.
- HELLO / HELLO_ACK / READY برای session جدید.
- TLS 1.3 mTLS و peer allowlist.
- HTTP/2 full-duplex carrier با cancellation و چهار transport مستقل برای چهار Shard.
- strict config model + JSON Schema + نمونه YAML امن برای IR/EX.
- route table ثابت و allowlisted.
- OPEN/DATA/ACK/WINDOW/FIN/FIN_ACK پایه.
- duplicate DATA suppression و idempotent OPEN پایه.
- integration test واقعی TCP → BAFT/H2+mTLS → TCP و مسیر برگشت بعد از half-close.

## تست

با Go 1.27.1، دستور هدف:

```bash
go test ./...
go test -race ./...
go vet ./...
```

در sandbox فعلی همین سه فرمان روی compatibility copy با Go 1.23.2 اجرا و پاس شده‌اند. جزئیات دقیق و محدودیت‌ها در `TEST-RESULTS.md` ثبت شده‌اند.

## پیکربندی

`configs/schema-v1.json` قرارداد schema نسخه 1 است و `configs/example-ir.yaml` و `configs/example-ex.yaml` نمونه‌های بدون secret هستند. در این مرحله `recovery.enabled` عمداً `false` است، چون resume هنوز پیاده‌سازی نشده و نادیده‌گرفتن silent آن مجاز نیست.

## وضعیت مراحل

برای پیشرفت دقیق، `PLAN.md`، `STATUS.md`، `BLOCKERS.md` و `KNOWN-LIMITATIONS.md` را ببینید.
