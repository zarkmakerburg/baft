<div dir="rtl" align="right" lang="fa">

# وضعیت پروژه

> نسخه انگلیسی: [STATUS.en.md](STATUS.en.md)  
> تاریخ این گزارش: 2026-09-29

## مخزن

- مخزن: `zarkmakerburg/baft`
- شاخه اصلی: `main`
- آخرین commit کد قبل از بازنویسی مستندات: `b1ddb44512fa0f48ff4629e1faf2f37523a9fe85`

## خلاصه

- **Stage A:** کامل برای scope تعریف‌شده.
- **Stage B:** کامل برای secure vertical slice تعریف‌شده.
- **Stage C:** گیت فعلی multi-Flow/slow-receiver soak سبز است؛ این به معنی benchmark عمومی یا production-ready بودن نیست.
- **Stage D:** بخش same-process ECRL تا Step 5.7 وارد Runtime شده و تست شده است، اما Stage D کامل یا production-ready اعلام نشده است؛ process-restart/machine-reboot resume و snapshot پایدار ECRL همچنان پیاده نشده‌اند.

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

### Stage-C soak — وضعیت فعلی

workflow مستقل `stagec-soak`، multi-Flow و slow-receiver واقعی را ۲۵ بار تکرار و ۵ دور زیر race detector اجرا می‌کند.

شواهد:
- failure تاریخی: run `36342169299` — **FAIL** با `TestConcurrentMultiFlowTransfer: unexpected EOF`.
- pass تاریخی پس از اصلاحات: run `36342627897` — **PASS**.
- شواهد code-head پیش از commit مستندات: run `36519987991` روی `6fcf41631dc963af6f9c124f245a3ce7a47bc2fe` — **PASS** شامل repeated soak و race sample.

در نتیجه گیت فعلی Stage C سبز است، اما این نتیجه benchmark عمومی، پایلوت واقعی یا ادعای production-ready بودن نیست.

## Stage D و بعد

### ECRL runtime gate — وضعیت فعلی Step 5.7

بررسی prior art، مدل تهدید، invariantهای I0–I6 و معیارهای ابطال F01–F10 همچنان در [docs/fa/15-stage-d-ecrl.md](docs/fa/15-stage-d-ecrl.md) مرجع طراحی هستند. از Step 5.7، بخش محدود و مشخصی از ECRL از حالت model/test-only وارد Runtime واقعی شده است.

موارد پیاده‌سازی و تست‌شده در همین scope:

- تعویض Carrier برای Session زنده در **همان process**؛
- same-process epoch fencing و رد frame مربوط به owner/epoch قدیمی؛
- bounded replay از Plan اعتبارسنجی‌شده و buffer موجود؛
- حفظ Flowهای TCP فعال، FIN/FIN_ACK، multi-flow و route identity؛
- commit safety با validation/materialization پیش از authority commit، Plan Digest canonical، barrier دوطرفه، commit idempotent و post-commit failure accounting؛
- پیوستگی telemetry و finance بدون double-count در replacement.

مواردی که همچنان پیاده نشده‌اند یا ادعا نمی‌شوند:

- process-restart resume؛
- machine-reboot resume؛
- durable ECRL session snapshots؛
- endpoint pool/relay production path؛
- benchmark رسمی 60s × 5؛
- systemd/packaging/operations کامل؛
- real-path pilot.

این وضعیت به معنی production-ready بودن کل BAFT یا کامل‌شدن تمام Stage D نیست.

## اصل ثبت وضعیت

این فایل فقط چیزی را «انجام‌شده» اعلام می‌کند که کد و شواهد اجرای آن وجود داشته باشد. برنامه Blueprint به‌تنهایی وضعیت پیاده‌سازی نیست.


## R2 v0.1 — Installer + SecurityInternal

شاخه‌ی آزمایشی: `r2-noise-v01`

شواهد اجرای واقعی:

- code head پیش از مستندسازی: `77724ee9eaa69fbec459b41896c3abbb04408e37`
- GitHub Actions run: `36352142629`
- Go: `1.27.1`
- `TestIKPSK0PairThenPinnedIK`: **PASS**
- `TestNoiseIKOverHTTP2TerminatingIntermediary`: **PASS**
- race detector برای Noise/unit+integration: **PASS**
- `bash -n install.sh`: **PASS**
- build ابزار `baft-pair`: **PASS**

در تست intermediary، outer TLS روی HTTP/2 endpoint خاتمه یافت و Noise IK/IKpsk0 داخل stream اجرا شد. capture خام واسطه شامل plaintext کاربردی BAFT نبود؛ تست در صورت مشاهده plaintext fail می‌شود.

### وضعیت Pairing

- EX: X25519 static key + pairing code با prefix `BAFTPAIR1:` و PSK یک‌بارمصرف کوتاه‌عمر.
- IR: تولید کلید مستقل، decode و apply اتمیک descriptor.
- اولین enrollment: `IKpsk0` برای اثبات possession کد pairing و یادگیری static key سمت IR.
- اتصال‌های بعدی: Noise `IK` با static keyهای pin‌شده.
- private key داخل pairing code قرار نمی‌گیرد.

### محدودیت مهم v0.1

`SecurityInternal` و تست H2 آن پیاده‌سازی و validate شده‌اند، اما هنوز به data path اصلی `node.Runtime` متصل نشده‌اند. بنابراین این مرحله «لایه امنیت داخلی validated» است، نه ادعای اینکه تمام BAFT production tunnel هم‌اکنون Noise-enabled شده است.

این عبارت وضعیت تاریخی R2 را ثبت می‌کرد؛ وضعیت فعلی ECRL در بخش Step 5.7 همین سند ملاک است.

### Stealth

random packet-size/timing morphing با هدف شکست سامانه‌های تحلیل یا فیلترینگ در این implementation وجود ندارد. R2 فعلی روی نصب‌پذیری، pairing، هویت رمزنگاری‌شده و confidentiality داخلی تمرکز دارد.


## R3 safe — Bounded Record Shaping

این بخش وضعیت تاریخی R3 safe را ثبت می‌کند؛ وضعیت فعلی ECRL در بخش Step 5.7 پایین سند ملاک است.

این شاخه یک لایه‌ی `internal/recordshape` بعد از Noise transport encryption و قبل از write روی wire اضافه می‌کند. رفتار آن:

- deterministic؛
- bounded؛
- بدون timing jitter؛
- بدون توزیع تصادفی packet-size؛
- با bucketهای ثابت برای کاهش افشای exact ciphertext length؛
- قابل خاموش/روشن شدن از Pairing descriptor.

<div dir="ltr" align="left">

```text
Noise ciphertext
    -> recordshape bucket
    -> length-prefixed H2 stream write
```

</div>

این قابلیت **anti-GFW / anti-DPI / traffic-analysis evasion** نامیده نمی‌شود و چنین ادعایی ندارد.

Installer گزینه‌ی زیر را دارد:

<div dir="ltr" align="left">

```text
--enable-record-shaping
```

</div>

در EX این flag داخل `BAFTPAIR1` منتقل می‌شود و IR آن را در pairing state دریافت می‌کند.

گیت‌های R3 safe:

- recordshape round-trip؛
- bounded bucket set؛
- Noise IK + record shaping روی TLS-terminating HTTP/2 intermediary؛
- race detector؛
- installer syntax؛
- Persian RTL docs.



## R3.1 — نامزد بررسی v0.2-Pro

Padding احتمالی نرمال/لاپلاس، jitter قابل تنظیم و پاسخ HTML پیش از ورود به Session پیاده شد. حالت صریح Noise با peer pin‌شده اکنون به Runtime متصل است. آزمون‌های محلی و Differential مدل/موتور ECRL در آن مقطع پاس شدند؛ توقف Stage D/recovery مربوط به همان مقطع تاریخی است و وضعیت فعلی در بخش Step 5.7 پایین سند ثبت شده است.

[گزارش سه‌بخشی هوشا و شواهد](reports/HOOSHA-R3.1.md). این بخش وضعیت فعلی شاخه R3.1 است و توضیحات R2/R3 بالا سوابق تاریخی‌اند. نسخه نهایی v0.2-Pro، نصب VM تازه، حفظ سرعت با jitter و اثبات مقاومت فیلترینگ هنوز تأیید نشده‌اند.

## وضعیت Steps 5.1 تا 5.7 روی شاخه Release — 2026-09-28

روی `release-v1-goldapp`، گیت‌های regression مربوط به Steps 5.1 تا 5.6 برای telemetry امضاشده و idempotent، monitoring مسیرها، گزارش مالی، سخت‌سازی BCC، backup/audit anchoring و persistent telemetry reliability در CI پاس شده‌اند.

در Step 5.7، ECRL به Session واقعی Runtime برای **تعویض Carrier فقط در همان process** متصل شده است. محدوده تست‌شده شامل حفظ Flow فعال، epoch fencing در همان process، bounded replay، رد fail-closed تغییر BootID سمت peer، competing candidates، بازیابی FIN/FIN_ACK، multi-flow، جداسازی هویت شش route و پیوستگی telemetry/finance است. Commit safety نیز validation/materialization قبل از commit، Plan Digest canonical، barrier دوطرفه prepared/commit، نتیجه صریح committed/uncommitted، هویت commit idempotent و accounting جداگانه خطاهای post-commit را دارد.

P0 baseline-recovery regression مربوط به generation readiness نیز اصلاح و با lifecycle صریح تثبیت شده است: readiness فقط بعد از distributed finalization، activation، replay/ACK/FIN reconciliation و آماده‌شدن data-plane برای همان generation منتشر می‌شود؛ stale generation نمی‌تواند ready frontier را عقب ببرد یا sender نسل جدید را متوقف کند.

شواهد code-head پیش از commit مستندات:
- Full CI push run `36519987982` روی `6fcf41631dc963af6f9c124f245a3ce7a47bc2fe`: **PASS**.
- Full CI PR run `36519992111`: **PASS**.
- Recovery-specific soak run `36519987974`: **PASS**؛ شامل 20× active-flow، 10× 8-flow، 5× six-route، 10× P0 uncertainty/finalization/replay/FIN matrix، 10× consecutive replacement و race sample.
- Stage-C soak run `36519987991`: **PASS**.

این وضعیت به معنی production-ready بودن BAFT نیست. process-restart resume، machine-reboot resume و snapshot پایدار ECRL پیاده‌سازی یا ادعا نشده‌اند. Subscription Engine نیز بخشی از Step 5.7 نیست.

</div>