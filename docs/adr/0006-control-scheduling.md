# ADR-0006 — زمان‌بندی محدود Control

> English: [en/0006-control-scheduling.md](en/0006-control-scheduling.md)  
> وضعیت: پذیرفته‌شده  
> تاریخ: 2026-09-27

## مسئله

اگر ACK/WINDOW/FIN پشت DATA حجیم بمانند، data path می‌تواند خودش را قفل کند. اگر از طرف دیگر Control اولویت مطلق و نامحدود داشته باشد، DATA ممکن است starve شود.

## تصمیم

- Control queue در هر sender محدود است.
- سقف baseline: 256 message یا 1 MiB.
- DATA از byte-based DRR عبور می‌کند.
- Control به‌طور عادی اولویت دارد.
- وقتی DATA منتظر است، پس از burst محدود Control یک DATA واجد شرایط سرویس می‌گیرد.
- مقدار فعلی burst برابر 32 است.

عدد 32 wire constant نیست و فقط با تست/اندازه‌گیری قابل تغییر است. سقف queue امنیتی/منبعی باقی می‌ماند.
