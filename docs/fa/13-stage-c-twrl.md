<div dir="rtl" align="right" lang="fa">

# 13 — فرضیه پژوهشی Stage C: دفتر سه‌نشانگر دریافت (TWRL)

## خلأ

HTTP/2 و QUIC هر دو از flow control مبتنی بر credit استفاده می‌کنند و credit معمولاً با مصرف داده توسط گیرنده افزایش می‌یابد. DRR نیز fairness بایتی میان Flowها را حل می‌کند. در BAFT یک تمایز اضافی وجود دارد: «پذیرش DATA در پروتکل» با «نوشته‌شدن همان DATA روی socket مقصد» یک رویداد نیست.

پیاده‌سازی قبلی این دو مرحله را در حلقه خواندن Carrier به هم متصل کرده بود؛ بنابراین target کند می‌توانست همان goroutine را در write نگه دارد و liveness پردازش فریم‌ها را کاهش دهد.

## فرضیه

اگر سه watermark مستقل نگه داریم:

- **A — Accepted:** بایت پیوسته‌ای که وارد حافظه bounded BAFT شده است؛ مبنای ACK.
- **D — Delivered:** بایتی که واقعاً روی socket مقصد نوشته شده است.
- **C — Credit:** بیشترین offset مجاز اعلام‌شده با WINDOW.

و invariant زیر را نگه داریم:

<div dir="ltr" align="left">

```text
D ≤ A ≤ C
C - D ≤ R
```

</div>

که R ظرفیت واقعی رزروشده‌ی receive است، آنگاه target کند بدون متوقف‌کردن حلقه Carrier backpressure ایجاد می‌کند و RAM نیز bounded می‌ماند.

## مکانیزم

به‌جای sliceهای موقت، هر Flow یک **ring buffer با ظرفیت دقیقاً متناظر با reservation** دارد.

1. DATA معتبر وارد ring می‌شود.
2. A جلو می‌رود و ACK(A) صادر می‌شود.
3. یک writer مستقل ring را روی target می‌نویسد.
4. فقط بعد از write موفق، D جلو می‌رود.
5. WINDOW به `D + R` حرکت می‌کند.
6. FIN_ACK فقط وقتی صادر می‌شود که `D == final_offset` و half-close مقصد موفق باشد.

## تفاوت با نسخه پایه قبلی

نسخه قبلی DATA را در حلقه Carrier مستقیماً روی target می‌نوشت. TWRL پذیرش پروتکل و تحویل socket را از نظر اجرا جدا می‌کند، ولی رابطه ریاضی آن‌ها را با invariant محدود نگه می‌دارد.

## prior art اولیه

- RFC 9113: HTTP/2 credit/window flow control و افزایش WINDOW با آزادشدن ظرفیت.
- RFC 9000: QUIC limit-based stream/connection flow control و absolute maximum offsets.
- Shreedhar/Varghese: Deficit Round Robin برای fairness بایتی.

این منابع اجزای شناخته‌شده را نشان می‌دهند؛ در این مرحله **ادعای novelty حقوقی برای TWRL نداریم**. بررسی prior art اختصاصی برای ترکیب accepted/delivered/credit در relay application-layer هنوز لازم است.

## معیار ابطال

فرضیه رد یا نیازمند بازطراحی است اگر یکی از این موارد رخ دهد:

- slow receiver باعث توقف پردازش Carrier شود؛
- `C-D` از reservation واقعی بیشتر شود؛
- داده قبل از target write از ring آزاد شود؛
- FIN_ACK قبل از تحویل همه bytes قبل از FIN صادر شود؛
- COR-01، race detector یا multi-Flow regression شکست بخورد.

## ریسک‌ها

- یک goroutine writer اضافه به‌ازای Flow؛
- پیچیدگی lifecycle هنگام RESET/cancel؛
- parser payload هنوز حافظه bounded جدا از ring مصرف می‌کند؛
- patentability یا novelty هنوز اثبات نشده است.


## نتیجه اجرای فعلی

پیاده‌سازی اولیه TWRL در commit `66c4d06d...` از unit/integration، race detector، vet و fuzz smoke عبور کرده است. slow-receiver gate سبز شد و COR-01 یک GiB نیز بدون تغییر hash پاس شد.

بنابراین وضعیت فعلی مکانیزم: **نتیجه مهندسی پشتیبانی‌شده در محیط CI**.

این نتیجه هنوز به معنی novelty حقوقی یا برتری performance روی اینترنت واقعی نیست.

</div>
