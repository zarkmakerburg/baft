<div dir="rtl" align="right" lang="fa">

# 18 — گیت Soak برای Stage C

## هدف

تست‌های کوتاه correctness برای کشف بسیاری از raceها، leakها و starvationهای زمان‌دار کافی نیستند. Stage C قبل از بسته‌شدن باید همان مسیر واقعی H2+mTLS را در چند دور متوالی و با رفتار چند Flow و گیرنده کند اجرا کند.

## workflow

فایل:

<div dir="ltr" align="left">

```text
.github/workflows/stagec-soak.yml
```

</div>

دو لایه اجرا می‌شود:

1. تست‌های `TestConcurrentMultiFlowTransfer` و `TestSlowReceiverCreatesBackpressureWithoutGrowingBAFTMemory` برای ۲۵ دور؛
2. همان دو تست برای ۵ دور زیر race detector.

## چرا workflow جدا؟

- CI عادی باید بازخورد سریع بدهد.
- soak باید سنگین‌تر باشد و شکست آن مستقل دیده شود.
- concurrency با `cancel-in-progress` تضمین می‌کند commit جدید run قدیمی همان branch را بی‌جهت مصرف نکند.
- نتیجه soak جای benchmark Stage E را نمی‌گیرد؛ هدف آن liveness، boundedness و race/leak regression است.

## معیار قبولی

- هیچ deadlock یا timeout؛
- هیچ corruption در multi-Flow؛
- slow receiver بعد از drain دوباره progress کند؛
- race detector هیچ data race گزارش نکند؛
- module lock تغییر نکند.

تا وقتی این workflow روی head مرتبط سبز نشده، Stage C کامل اعلام نمی‌شود.

</div>
