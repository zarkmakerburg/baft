<div dir="rtl" align="right" lang="fa">

# ADR-0001 — قرارداد پروژه

> English: [en/0001-project-contract.md](en/0001-project-contract.md)  
> وضعیت: پذیرفته‌شده  
> تاریخ: 2026-09-27

## مسئله

پروژه‌ای که transport، security، recovery و performance را هم‌زمان توسعه می‌دهد به‌راحتی می‌تواند «وجود کد» را با «اثبات correctness» اشتباه بگیرد یا برای دسترس‌پذیری، مرزهای امنیتی را تضعیف کند.

## تصمیم

BAFT تا زمانی که شواهد کافی تولید نشده **research software** باقی می‌ماند.

ترتیب اولویت:

1. correctness و security؛
2. bounded resource behavior؛
3. recovery دقیق؛
4. performance قابل‌بازتولید؛
5. قابلیت‌های آزمایشی.

هیچ feature مجاز نیست برای ساده‌کردن مسیر:

- TLS verification را خاموش کند؛
- authorization را دور بزند؛
- frame validation را ضعیف کند؛
- queue/memory limit را نامحدود کند؛
- نتیجه تست اجرا‌نشده را موفق گزارش کند.

## پیامد

هر Stage باید gate و evidence داشته باشد. قابلیت experimental از core جدا می‌ماند و fail آن نباید باعث downgrade امنیتی مسیر اصلی شود.

</div>
