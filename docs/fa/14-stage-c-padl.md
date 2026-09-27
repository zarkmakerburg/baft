# 14 — فرضیه پژوهشی Stage C: PADL

## نام

**Pressure-Aged Deficit Leasing — PADL**

## خلأ

DRR استاندارد fairness را برحسب بایت فراهم می‌کند، اما نمی‌داند یک Flow چه مقدار replay memory را نگه داشته است. FQ-CoDel delay صف را با AQM و drop/mark مدیریت می‌کند؛ BAFT روی جریان TCP قابل‌اعتماد نمی‌تواند بایت application را برای مدیریت صف حذف کند. الگوریتم‌های BackPressure/MaxWeight نیز queue pressure را به scheduling وارد می‌کنند، اما معمولاً برای شبکه‌های constrained/multihop طراحی شده‌اند و trade-off تأخیر متفاوتی دارند.

## فرضیه

در میان Flowهایی که از نظر deficit واجد ارسال هستند، Flow با **replay debt کمتر** در اولویت قرار گیرد، ولی یک aging bound اجازه ندهد Flow پرفشار برای همیشه گرسنه بماند.

PADL سه جزء دارد:

1. deficit بایتی DRR برای حفظ accounting fairness؛
2. replay pressure به‌عنوان سیگنال هزینه نگهداری حافظه؛
3. max-skip aging برای bound کردن starvation.

## چرا این فقط tuning DRR نیست؟

quantum ثابت DRR فقط «سهم سرویس» را مدل می‌کند. PADL تصمیم dequeue را به state حافظه‌ی reliability layer متصل می‌کند؛ بنابراین scheduler و replay allocator دیگر دو subsystem بی‌خبر از هم نیستند.

## معیارهای ابطال

- equal-pressure fairness باید regression نداشته باشد؛
- low-pressure flow باید در contention ترجیح داده شود؛
- high-pressure flow نباید بیش از bound تعریف‌شده skip شود؛
- COR-01 و multi-Flow باید بدون corruption پاس شوند؛
- benchmark باید نشان دهد هزینه O(n) prototype قابل‌قبول است؛ در غیر این صورت نسخه bucketed/O(1) لازم است.

## ریسک

prototype فعلی برای انتخاب، Flowهای فعال را scan می‌کند؛ بنابراین نسبت به DRR کلاسیک O(1) هزینه CPU بیشتری دارد. این هزینه تا Stage E پذیرفته یا رد نمی‌شود.

## prior art اولیه

- DRR: Shreedhar/Varghese؛
- FQ-CoDel: RFC 8290؛
- MaxWeight/BackPressure: Tassiulas/Ephremides.

وضعیت novelty حقوقی: **بررسی نشده / ادعایی وجود ندارد**.
