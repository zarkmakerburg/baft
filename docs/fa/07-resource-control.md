<div dir="rtl" align="right" lang="fa">

# 07 — کنترل حافظه، backpressure و زمان‌بندی

## چرا این بخش مهم است؟

یک relay که فقط «داده را عبور می‌دهد» اگر صف و حافظه نامحدود داشته باشد، زیر receiver کند یا چند Flow هم‌زمان می‌تواند RAM را مصرف کند یا یک Flow بقیه را گرسنه کند. Stage C برای حل همین مسئله است.

## allocator سراسری

در طراحی فعلی یک allocator مشترک برای data memory وجود دارد. budget پیش‌فرض نمونه 256 MiB است و به دو pool مستقل تقسیم می‌شود:

- receive pool: 128 MiB؛
- replay pool: 128 MiB.

این poolها **از هم قرض نمی‌گیرند**. اگر replay پر شود، خالی بودن receive نباید باعث رشد replay فراتر از سقف شود.

## سقف per-flow

نمونه baseline:

- receive max per Flow: 16 MiB؛
- replay max per Flow: 16 MiB.

این سقف‌ها باید از pool مربوط بزرگ‌تر نباشند. Runtime config این رابطه را validation می‌کند.

## receive reservation

WINDOW نباید صرفاً یک عدد خوش‌بینانه باشد. قبل از بالا بردن credit، گیرنده باید capacity مربوط را رزرو کرده باشد.

مدل ساده:

<div dir="ltr" align="left">

```text
capacity رزروشده
      │
      ▼
WINDOW(max_offset)
      │
      ▼
peer اجازه ارسال پیدا می‌کند
```

</div>

اگر ظرفیت وجود نداشته باشد، Window نباید بدون پشتوانه رشد کند.

## replay reservation

پیش از خواندن payload جدید از socket محلی، sender فضای replay لازم را رزرو می‌کند. بعد از ساخت DATA، نسخه لازم برای state تأییدنشده نگه داشته می‌شود.

با ACK پیوسته:

<div dir="ltr" align="left">

```text
ACK(new_offset)
    │
    ▼
chunkهای کاملاً ACKشده حذف
    │
    ▼
replay reservation آزاد
    │
    ▼
Flowهای منتظر می‌توانند ادامه دهند
```

</div>

## backpressure

وقتی credit یا replay budget تمام شود، رفتار درست **توقف خواندن بیشتر از socket منبع** است، نه ساختن queue بزرگ‌تر.

بنابراین backpressure باید به منبع TCP برگردد:

<div dir="ltr" align="left">

```text
target کند
  → write سمت گیرنده کند
  → WINDOW محدود
  → sender credit تمام می‌کند
  → خواندن socket منبع متوقف
  → TCP source نیز فشار را حس می‌کند
```

</div>

## وضعیت تست receiver کند

یک integration test برای receiver کند وجود دارد که دو چیز را می‌سنجد:

1. allocator از سقف configured عبور نکند؛
2. پس از شروع drain، source دوباره پیشروی کند.

در وضعیت فعلی کد، COR-01 یک GiB بعد از اتصال allocator/DRR همچنان پاس شده است، اما slow-receiver test هنوز در حال تثبیت است و یک مورد liveness/threshold در CI دیده شده است. بنابراین Stage C هنوز complete اعلام نمی‌شود.

## DRR برحسب بایت

DATA بین Flowها با Deficit Round Robin زمان‌بندی می‌شود. واحد fairness تعداد packet نیست؛ bytes است.

برای هر Flow:

- quantum مشخص می‌شود؛
- deficit با هر دور افزایش می‌یابد؛
- DATA وقتی ارسال می‌شود که اندازه آن در deficit جا شود؛
- deficit مصرف‌شده کم می‌شود.

این مدل مانع آن است که Flow با frameهای کوچک صرفاً به‌خاطر تعداد frame بیشتر سهم غیرمنصفانه بگیرد.

## Control و DATA جدا هستند

Control frameها مانند ACK/WINDOW/FIN نباید پشت حجم DATA قفل شوند، چون خود آن‌ها برای آزادشدن data path ضروری‌اند.

در implementation فعلی:

- control queue محدود است؛
- سقف 256 message یا 1 MiB دارد؛
- control معمولاً اولویت دارد؛
- برای جلوگیری از starvation DATA، وقتی DATA منتظر است حداکثر burst کنترل محدود می‌شود؛
- مقدار فعلی burst برابر 32 است و policy پیاده‌سازی است، نه wire constant.

## چرا queue کنترل bounded است؟

اگر peer بتواند control message نامحدود بسازد، حتی بدون payload بزرگ امکان OOM یا starvation وجود دارد. بنابراین control نیز باید budget داشته باشد.

## چند Shard

هر Shard sender و queue مستقل دارد. Node dialer Flowهای جدید را در production path فعلی میان Shardها round-robin توزیع می‌کند.

این round-robin به‌تنهایی تضمین کامل fairness سراسری نیست؛ allocator مشترک و scheduler داخل هر Shard بخش‌های دیگر کنترل منابع هستند.

## شرط کامل‌شدن Stage C

Stage C فقط وقتی باید complete شود که:

- allocator در مسیر واقعی فعال باشد؛
- receive/replay caps در تست فشار رعایت شوند؛
- DRR در چند Flow رفتار قابل‌قبول داشته باشد؛
- control queue bounded و بدون data starvation باشد؛
- receiver کند باعث OOM نشود؛
- بعد از drain مسیر به شکل قابل‌اعتماد resume کند؛
- چند Shard و چند Flow در CI پایدار باشند.

</div>
