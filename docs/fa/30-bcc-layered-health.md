<div dir="rtl" align="right" lang="fa">

# 30 — سلامت چندلایهٔ BCC با hysteresis

HQ P1-1. هر نود برای هر **لایه** یک وضعیت سلامت دارد و وضعیت فقط بعد از چند نمونهٔ هم‌جهت در یک زمان حداقلی عوض می‌شود، نه با یک خواندن. هر transition دلیل و evidence دارد، در audit ثبت می‌شود و در تاریخچه‌ای قابل مشاهده می‌ماند که از restart جان به در می‌برد.

## لایه‌ها

| لایه | BCC چه می‌بیند | منبع |
|---|---|---|
| L0 | process BAFT گزارش می‌دهد | telemetry خود نود: telemetry کهنه از نودی که قبلاً گزارش می‌داد BAD است |
| L1 | نود در دسترس است | probe TCP خود BCC روی آدرس نود (نه carrier بین IR و EX) |
| L2 | نشست‌های peer احراز شده | نشست فعال در telemetry = OK؛ نرخ خطای handshake بالای آستانهٔ alert = BAD؛ بدون نشست = بدون evidence |
| L3 | درستی نشست | **NOT_ASSESSED** (هنوز سیگنال recovery یا epoch به BCC نمی‌رسد) |
| L4 | probeهای route | probeهایی که نود اجرا می‌کند: هر `down` = BAD، دست‌کم یک `up` و هیچ down = OK |
| L5 | مقصد در دسترس | **NOT_ASSESSED** (telemetry نمی‌گوید کدام probe مقصد است) |
| L6 | ترافیک برنامه | **NOT_ASSESSED** (به probe سرتاسری نیاز دارد) |
| A | poll شدن agent | آخرین poll دادن agent به BCC (صفحهٔ مدیریت) |

انطباق پیکربندی (وضعیت drift تونل active نود، سند ۲۷) کنار لایه‌ها نشان داده می‌شود ولی جزو این ماشین‌های وضعیت نیست: مشاهدهٔ فایل‌هاست، نه زنده بودن.

## ماشین وضعیت (برای هر لایه)

<div dir="ltr" align="left">

```
UNKNOWN --(5 OK)--> UP --(2 consecutive BAD)--> DEGRADED --(5 BAD and 30 s)--> DOWN
DEGRADED --(3 consecutive OK)--> UP
DOWN --(first OK)--> RECOVERING --(5 OK and 30 s)--> UP
RECOVERING --(2 consecutive BAD)--> DOWN
```

</div>

هر نمونه **OK**، **BAD** یا **NONE** (بدون evidence) است. یک BAD میان OKها یا تناوب کامل، هرگز UP را ترک نمی‌دهد. لایه تا با نمونه‌های OK پیاپی خودش را ثابت نکرده UP نیست، پس موقع شروع هم UP کاذب نداریم.

- **سکوت نه خرابی است نه سلامت.** NONE نه OK حساب می‌شود نه BAD: runها را صفر می‌کند، هرگز recovery یا خرابی را کامل نمی‌کند و بعد از ۳ دقیقه NONE پیوسته لایه **UNKNOWN** می‌شود، نه DOWN.
- **NOT_ASSESSED نه DOWN است نه UNKNOWN.** برچسب ثابتِ لایه‌ای است که BCC سیگنالی برایش ندارد؛ وضعیتی نیست که لایه وارد یا خارجش شود.
- **قطعی BCC حساب نمی‌شود.** اگر بین دو نمونهٔ یک لایه بیش از ۲ دقیقه فاصله باشد (BCC متوقف یا گیر کرده بود)، runها از صفر شروع می‌شوند؛ خود وضعیت می‌ماند. یک نمونه بعد از قطعی یک مشاهده است، نه حکم.
- پیش‌فرض‌ها (نمونه‌گیری هر `--health-interval`، یعنی ۱۰ ثانیه) فعلاً در کد ثابت‌اند؛ `GET /api/health` آن‌ها را برمی‌گرداند (`policy`).

## وضعیت کلی نود

از روی لایه‌ها محاسبه می‌شود، جدا ذخیره نمی‌شود و هرگز فقط از یک لایه گرفته نمی‌شود:

- **DOWN** فقط وقتی L0 یا L1 در DOWN باشد. **RECOVERING** وقتی L0 یا L1 در RECOVERING باشد و هیچ‌کدام DOWN نباشد.
- **DEGRADED** وقتی لایهٔ دیگری (L2، L4، A) در DEGRADED، DOWN یا RECOVERING باشد، یا هر لایه‌ای DEGRADED باشد.
- **UP** فقط وقتی L0 و L1 هر دو UP باشند و هیچ‌چیز degraded، down یا recovering نباشد. لایه‌های بدون evidence جلوی UP را نمی‌گیرند و سالم هم حساب نمی‌شوند؛ نودی که L0 یا L1 آن وضعیت ثابت‌شده ندارد **UNKNOWN** است، نه UP.
- **UNKNOWN** وقتی هیچ لایه‌ای evidence ندارد.

پس نشست‌های سالم (L2) نمی‌توانند process ساکت (L0) را سالم نشان دهند و probe خوب (L1) نمی‌تواند route خراب (L4) را پنهان کند.

## کجا دیده می‌شود

- `GET /api/health` (ادمین، فقط‌خواندنی): برای هر نود وضعیت کلی با دلیل‌ها، هر لایه با وضعیت، از کی و آخرین evidence، انطباق پیکربندی و ۱۰ transition آخر؛ `?node=ID&all=1` کل تاریخچه را می‌دهد (۲۰۰ transition آخر نگه داشته می‌شود).
- کارت **Layered health** در داشبورد.
- audit: هر transition یک ورودی `health.transition` با target `<node>/<layer>` است، outcome برابر `failure` برای رفتن به DEGRADED یا DOWN، و جزئیات `layer`، `from`، `to`، `node_from`، `node_to`، `reason`، `evidence`.
- state: رکوردها بخشی از پایگاه state در BCC (migration شمارهٔ ۳، جدول `node_health`) و پشتیبان‌های رمزشدهٔ آن هستند.

## چه چیزی عوض نشده

قواعد alert (`telemetry_stale`، `route_down`، `handshake_error_rate`) و webhook هنوز از مقدارهای لحظه‌ای استفاده می‌کنند؛ سوار کردنشان روی این hysteresis تغییر جداست. L3 و L5 و L6 تا وقتی BCC سیگنالی برایشان نگیرد NOT_ASSESSED می‌مانند.

</div>
