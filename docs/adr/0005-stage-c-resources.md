<div dir="rtl" align="right" lang="fa">

# ADR-0005 — منابع و حافظه Stage C

> English: [en/0005-stage-c-resources.md](en/0005-stage-c-resources.md)  
> وضعیت: در حال تثبیت  
> تاریخ: 2026-09-27

## تصمیم

Stage C از یک allocator سراسری با poolهای جدا برای receive و replay استفاده می‌کند.

baseline نمونه:

- data memory کل: 256 MiB؛
- receive pool: 128 MiB؛
- replay pool: 128 MiB؛
- per-flow receive cap: 16 MiB؛
- per-flow replay cap: 16 MiB؛
- borrowing بین poolها ممنوع.

Replay قبل از read اضافی source رزرو می‌شود و با ACK آزاد می‌شود. Receive credit باید پشتوانه reservation واقعی داشته باشد.

## زمان‌بندی

DATA با DRR برحسب byte و Control با queue محدود زمان‌بندی می‌شود.

## وضعیت

این primitives اکنون به Session data path وصل شده‌اند و COR-01 بعد از اتصال دوباره پاس شده است. با این حال slow-receiver liveness gate هنوز سبز پایدار نیست؛ بنابراین ADR از نظر طراحی پذیرفته شده ولی **Stage C هنوز complete نیست**.

</div>
