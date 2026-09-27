<div dir="rtl" align="right" lang="fa">

# 19 — قرنطینه محدود وضعیت پایانی Flow

## مسئله

soak تکرارشونده یک failure تصادفی multi-Flow با `unexpected EOF` آشکار کرد. تحلیل lifecycle نشان داد آخرین target delivery می‌تواند با دریافت FIN هم‌زمان شود: یک مسیر Flow را کامل و بسته می‌کند، در حالی که target-pump هنوز قصد refresh کردن WINDOW را دارد. تبدیل خطای «Flow بسته شده» به RESET می‌تواند یک control frame دیررس تولید کند.

## تصمیم

دو invariant اعمال می‌شود:

1. پس از terminal شدن Flow، intent دیررس برای افزایش credit **جذب می‌شود و RESET تولید نمی‌کند**.
2. شناسه Flowهای واقعاً بسته‌شده در یک **Bounded Terminal Quarantine** حداکثر ۲۵۶تایی نگه داشته می‌شود. فقط ACK/WINDOW/FIN_ACK/RESET دیررس برای همین شناسه‌های شناخته‌شده جذب می‌شوند؛ stream_id ناشناخته همچنان protocol error است.

## چرا اینجا روش متعارف را ترجیح دادیم؟

در semantics پایانی، ابداع رفتار کاملاً جدید ارزش بیشتری از ریسک correctness ندارد. پروتکل‌های جاافتاده نیز برای frameهای درراه پس از transition به حالت بسته، مفهوم draining/closed processing دارند. بنابراین این بخش عمداً یک الگوی محافظه‌کارانه و bounded است؛ نوآوری پروژه در recovery ledger و scheduling دنبال می‌شود، نه در مبهم‌کردن terminal semantics.

## ریسک

- tombstoneها state اضافی هستند، پس cap سخت ۲۵۶ دارند؛
- tombstone فعلی فقط در طول عمر Process است؛
- Stage D باید retention/epoch/tombstone را به state-machine resume دقیق متصل کند.

</div>
