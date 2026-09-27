<div dir="rtl" align="right" lang="fa">

# 15 — Conservation Telemetry و تست چند Shard

## مسئله

counterهای جداگانه مثل «تعداد بایت» یا «حافظه مصرفی» به‌تنهایی نمی‌گویند state داخلی سازگار است. Stage C به observability نیاز دارد که مستقیماً invariant را بسنجد.

## طراحی

برای هر Flow snapshot زیر ثبت می‌شود:

- A: Accepted؛
- D: Delivered؛
- C: Credit؛
- R: receive reservation؛
- bytes موجود در receive ring؛
- replay outstanding.

بررسی مستقیم:

<div dir="ltr" align="left">

```text
D <= A <= C
C - D <= R
ring_bytes == A - D
```

</div>

این snapshot منبع داخلی برای metrics بعدی است؛ یعنی dashboard آینده از counterهای مستقل حقیقت جداگانه نمی‌سازد.

## تست چند Shard

چند Peer با shard_id متفاوت یک allocator مشترک می‌گیرند. با receive pool کوچک، دو Flow هر کدام 64 KiB را رزرو می‌کنند و Flow سوم باید RESOURCE_EXHAUSTED بگیرد. بعد از بسته‌شدن Flow اول، همان ظرفیت باید بدون leak برای Flow سوم قابل رزرو باشد.

## نوآوری ادعاشده؟

فعلاً این مکانیزم به‌عنوان روش مهندسی برای **conservation-oriented observability** ثبت می‌شود، نه patent claim. تفاوت اصلی با metrics معمول این است که رابطه میان چند counter به‌عنوان داده درجه‌اول گزارش می‌شود.

</div>
