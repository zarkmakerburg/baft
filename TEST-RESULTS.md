# نتایج آزمون

> English: [TEST-RESULTS.en.md](TEST-RESULTS.en.md)  
> تاریخ: 2026-09-27

## محیط CI

- GitHub Actions
- Linux amd64
- Go 1.27.1
- dependency lock با `go.mod` و `go.sum`

## gate استاندارد

```bash
go mod tidy
git diff --exit-code -- go.mod go.sum
go test ./...
go test -race ./...
go vet ./...
go test ./internal/protocol -run '^$' -fuzz '^FuzzDecode$' -fuzztime 10s
```

## Stage B — شواهد اصلی

مواردی که قبلاً روی CI پاس شده‌اند شامل:

- H2+mTLS full-duplex؛
- certificate negative tests؛
- active revocation؛
- strict YAML؛
- CLI؛
- Route/Flow integration؛
- protocol fuzz smoke.

## COR-01 اولیه Stage B

Run `36316645627`:

- 1 GiB در هر جهت؛
- SHA-256:
  `1efd9d3aab21f9e312a2a0b5a6886b2a640c810ecb1fbe33f64614b26cfb27e3`
- PASS.

## COR-01 بعد از Stage C wiring

Run `36338439622` روی `b1ddb445...`:

- Go: 1.27.1
- bytes_each_direction: `1073741824`
- SHA-256:
  `1efd9d3aab21f9e312a2a0b5a6886b2a640c810ecb1fbe33f64614b26cfb27e3`
- duration: `10.56s`
- **PASS**

این regression نشان می‌دهد مسیر حجیم بعد از ورود allocator/DRR هنوز corruption یا deadlock کامل ندارد؛ اما به‌تنهایی Stage C را اثبات نمی‌کند.

## multi-Flow

commit `0d70f1f...` integration test چند Flow را اضافه کرد و CI run `36317438771` **PASS** شد.

## control scheduling

commit `eaefc310...` bounded control scheduling را اضافه کرد. CI عادی run `36317760598` PASS شد، اما COR-01 همان commit timeout شد؛ بنابراین implementation بعداً تغییر کرد و regression دوباره اجرا شد.

## slow receiver — مسئله باز

آخرین CI ثبت‌شده روی commit `b1ddb445...`:

- run: `36338439633`
- test: `TestSlowReceiverCreatesBackpressureWithoutGrowingBAFTMemory`
- result: **FAIL**

مشاهده ثبت‌شده:

- source قبل از drain: `65536` بایت؛
- پس از drain در مهلت تست: `98304` بایت؛
- forward progress رخ داد، ولی شرط liveness تست برآورده نشد.

تا حل و تکرارپذیری این gate، Stage C کامل نیست.

## آزمون‌هایی که هنوز لازم‌اند

- slow-receiver پایدار و soak طولانی؛
- multi-Shard shared-budget stress؛
- resume/epoch/replay/tombstone؛
- state-machine fuzz طولانی؛
- benchmark رسمی 60s × 5؛
- عملیات certificate rotation/rollback؛
- real-path pilot.
