# ADR-0004 — semantics مسیر Flow در Stage B

> English: [en/0004-stage-b-flow-semantics.md](en/0004-stage-b-flow-semantics.md)  
> وضعیت: پذیرفته‌شده برای Stage B؛ بعضی جزئیات در Stage C توسعه یافته‌اند  
> تاریخ: 2026-09-27

## تصمیم‌های Stage B

- Session جدید قبل از application frame از `HELLO → HELLO_ACK → READY` عبور می‌کند.
- OPEN فقط `route_id` و `open_nonce` دارد.
- مقصد واقعی از Route table محلی resolve می‌شود.
- DATA دارای offset مستقل در هر جهت است.
- duplicate DATA دوباره روی target نوشته نمی‌شود.
- ACK با target-written یکی نیست.
- FIN/FIN_ACK half-close را مدل می‌کند.
- OPEN تکراری همسان idempotent است و target را دوباره dial نمی‌کند.

## تغییر Stage C

Window ساده Stage B بعداً با reservation سراسری receive/replay و backpressure واقعی تکمیل شده است. برای وضعیت فعلی [docs/fa/07-resource-control.md](../fa/07-resource-control.md) را ببینید.
