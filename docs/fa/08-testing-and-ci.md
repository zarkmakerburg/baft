<div dir="rtl" align="right" lang="fa">

# 08 — آزمون‌ها، CI و معیار پذیرش

## اصل شواهد

در BAFT وجود کد معادل «قابلیت تأییدشده» نیست. نتیجه فقط وقتی در STATUS یا TEST-RESULTS موفق ثبت می‌شود که فرمان واقعی اجرا شده و نتیجه آن موجود باشد.

## CI استاندارد

workflow اصلی روی نسخه pin‌شده Go اجرا می‌کند:

<div dir="ltr" align="left">

```bash
go mod tidy
git diff --exit-code -- go.mod go.sum

go test ./...
go test -race ./...
go vet ./...
go test ./internal/protocol -run '^$' -fuzz '^FuzzDecode$' -fuzztime 10s
```

</div>

`go mod tidy` همراه با `git diff` از drift وابستگی جلوگیری می‌کند.

## دسته‌های تست

### unit
برای parser، config، identity، route table، allocator، scheduler و stateهای Flow.

### integration
مسیر واقعی componentها را کنار هم قرار می‌دهد:

- TCP محلی؛
- BAFT Session؛
- H2+mTLS Carrier؛
- target TCP؛
- half-close؛
- چند Flow؛
- backpressure.

### fuzz
ورودی parser و state boundaryها را با داده متغیر می‌آزماید. smoke کوتاه CI جایگزین campaign طولانی نیست.

### correctness
تست‌هایی مانند COR-01 حجم داده و hash انتهایی را بررسی می‌کنند.

### benchmark
هنوز gate رسمی 60 ثانیه × 5 تکرار کامل نشده است. نتایج correctness نباید به‌عنوان throughput عمومی معرفی شوند.

## COR-01

COR-01 جریان 1 GiB deterministic را در هر جهت از مسیر کامل عبور می‌دهد و SHA-256 را مقایسه می‌کند.

شواهد ثبت‌شده Stage B:

- 1,073,741,824 بایت در هر جهت؛
- SHA-256 برابر در دو سمت؛
- اجرا روی GitHub-hosted runner و loopback/local networking.

این تست **صحت** را نشان می‌دهد، نه سرعت اینترنت عمومی.

پس از اتصال allocator و DRR به Session، workflow COR-01 مجدداً اجرا شده و همچنان پاس شده است. این regression gate مهم است چون تغییر flow-control ممکن است deadlock یا corruption ایجاد کند.

## تست گواهی و امنیت

مجموعه تست‌ها مواردی مانند این را پوشش می‌دهد:

- peer خارج allowlist؛
- certificate منقضی؛
- server-name mismatch؛
- CA نامعتبر؛
- active revocation Carrier موجود؛
- Route denied/not found؛
- duplicate JSON/YAML key؛
- error code ناشناخته؛
- frame type/length نامعتبر.

## تست چند Flow

integration test چند اتصال هم‌زمان ایجاد می‌کند، payload متفاوت می‌فرستد و hash/length هر Flow را جدا بررسی می‌کند. هدف این است که multiplexing باعث cross-flow corruption نشود.

## slow receiver

این gate باید ثابت کند:

- RAM از budget عبور نمی‌کند؛
- source قبل از drain واقعاً backpressured می‌شود؛
- بعد از drain forward progress برمی‌گردد؛
- cancellation flow را آزاد می‌کند.

در زمان این بازنویسی مستندات، همین gate هنوز یک failure Stage C دارد؛ بنابراین وضعیت main باید از GitHub Actions بررسی شود و Stage C complete نیست.

## race detector

`go test -race ./...` بخشی از gate استاندارد است، چون Session و scheduler چند goroutine هم‌زمان دارند.

## چه چیزی هنوز لازم است؟

- fuzz campaign طولانی؛
- soak چندساعته؛
- chaos مربوط به carrier replacement؛
- Stage D state-machine fuzz؛
- benchmark رسمی تکرارشونده؛
- تست real-path میان سرورهای واقعی؛
- عملیات rotation/rollback.

</div>
