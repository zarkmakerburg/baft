<div dir="rtl" align="right" lang="fa">

# وضعیت پروژه

> نسخه انگلیسی: [STATUS.en.md](STATUS.en.md)  
> تاریخ این گزارش: 2026-09-27

## مخزن

- مخزن: `zarkmakerburg/baft`
- شاخه اصلی: `main`
- آخرین commit کد قبل از بازنویسی مستندات: `b1ddb44512fa0f48ff4629e1faf2f37523a9fe85`

## خلاصه

- **Stage A:** کامل برای scope تعریف‌شده.
- **Stage B:** کامل برای secure vertical slice تعریف‌شده.
- **Stage C:** از نظر پیاده‌سازی اصلی نزدیک به بسته‌شدن است؛ فقط soak مستقل باید PASS شود.
- **Stage D به بعد:** هنوز کامل نشده‌اند.

## Stage A

موارد پیاده و آزموده‌شده:

- BAFT/1 frame codec و golden vectors؛
- strict config contract؛
- test PKI؛
- HTTP/2 full-duplex واقعی؛
- TLS 1.3 + mTLS؛
- cancellation؛
- چهار Transport مستقل برای چهار Shard در smoke test.

## Stage B

موارد پیاده و آزموده‌شده:

- HELLO / HELLO_ACK / READY دوطرفه برای Session جدید؛
- Route ثابت و allowlisted؛
- OPEN / OPEN_OK / OPEN_ERR؛
- DATA / ACK / WINDOW؛
- FIN / FIN_ACK؛
- RESET با error code ثابت؛
- duplicate DATA suppression؛
- idempotent OPEN؛
- بررسی certificate chain، hostname، EKU و URI SAN؛
- peer allowlist مستقل از CA trust؛
- active revocation بر اساس identity، serial و SHA-256 fingerprint؛
- strict YAML loader؛
- CLI واقعی `config validate` و `run`؛
- مسیر واقعی TCP → BAFT/H2+mTLS → TCP؛
- COR-01 یک GiB دوطرفه.

## آخرین شواهد COR-01

Workflow run `36338439622` روی commit `b1ddb445...`:

- Go: `go1.27.1 linux/amd64`
- بایت در هر جهت: `1073741824`
- SHA-256 هر دو جهت:
  `1efd9d3aab21f9e312a2a0b5a6886b2a640c810ecb1fbe33f64614b26cfb27e3`
- زمان همان اجرای correctness: حدود `10.56s`
- نتیجه: **PASS**

این عدد performance عمومی اینترنت نیست؛ فقط correctness روی محیط runner/local است.

## Stage C — وضعیت واقعی

### انجام‌شده یا وارد data path شده

- allocator سراسری receive/replay؛
- poolهای مستقل و bounded؛
- replay reservation و آزادسازی با ACK؛
- backpressure مبتنی بر credit و memory؛
- DRR برحسب byte برای DATA؛
- control queue محدود؛
- اولویت control با burst cap محدود برای جلوگیری از starvation؛
- integration test چند Flow؛
- COR-01 پس از تغییرات allocator/DRR دوباره پاس شده است.

### TWRL و slow-receiver gate

در commit `66c4d06d...` طراحی TWRL وارد مسیر واقعی شد. سه watermark مستقل نگه داشته می‌شوند:

- A: داده پذیرفته‌شده در حافظه محدود BAFT؛
- D: داده واقعاً تحویل‌شده به socket مقصد؛
- C: بیشترین offset اعلام‌شده با WINDOW.

invariant اصلی: `D ≤ A ≤ C` و `C-D ≤ R` که R ظرفیت receive رزروشده است.

GitHub Actions run `36340860552` همه‌ی unit/integration tests، race detector، vet و fuzz smoke را **PASS** کرد. تست slow receiver نیز در همین run سبز شد. یک تست deterministic با `net.Pipe` علاوه بر integration test اثبات می‌کند آزادشدن target حداقل دو مرحله credit را جلو می‌برد و Carrier مجبور نیست target write را inline انجام دهد.

COR-01 روی همین commit در run `36340860568` نیز **PASS** شد.

نتیجه: slow-receiver liveness gate فعلی بسته شده است.

### PADL، چند Shard و conservation observability

مسیر فعال DATA از DRR ساده به **PADL (Pressure-Aged Deficit Leasing)** ارتقا یافته است. PADL accounting بایتی DRR را حفظ می‌کند، اما انتخاب Flow واجدشرایط را با replay-memory debt و aging ضد-starvation ترکیب می‌کند. DRR کلاسیک در مخزن به‌عنوان baseline مقایسه‌ای باقی مانده است.

روی commit `fc64ef6b...`:
- CI run `36341912070`: **PASS**
- COR-01 run `36341912044`: **PASS**
- high-volume PADL liveness test: PASS
- race detector / vet / fuzz smoke: PASS

همچنین shared allocator چند Shard و reuse ظرفیت بدون leak در commit `efe837fb...` وارد gate شده و CI run `36341666646` **PASS** است.

Conservation Telemetry و metrics روی loopback نیز پیاده شده‌اند. metrics به‌صورت aggregate رابطه‌های A-D، C-D، replay outstanding و invariant violations را بدون peer/route/target/stream label حساس منتشر می‌کنند. CI run `36341810504` برای این مسیر **PASS** است.

### تنها گیت باز Stage C

workflow مستقل `stagec-soak` اضافه شده است. این workflow multi-Flow و slow-receiver واقعی را ۲۵ بار تکرار و ۵ دور زیر race detector اجرا می‌کند. تا PASS شدن این gate، Stage C کامل اعلام نمی‌شود.

## Stage D و بعد

هنوز به‌عنوان قابلیت کامل وجود ندارند:

- resume کامل؛
- epoch fencing؛
- snapshot/replay/tombstone؛
- duplicate-free Carrier replacement؛
- endpoint pool/relay production path؛
- benchmark رسمی 60s × 5؛
- systemd/packaging/operations کامل؛
- real-path pilot.

## اصل ثبت وضعیت

این فایل فقط چیزی را «انجام‌شده» اعلام می‌کند که کد و شواهد اجرای آن وجود داشته باشد. برنامه Blueprint به‌تنهایی وضعیت پیاده‌سازی نیست.

</div>
