<div dir="rtl" align="right" lang="fa">

# نتایج آزمون

> English: [TEST-RESULTS.en.md](TEST-RESULTS.en.md)  
> تاریخ: 2026-09-27

## محیط CI

- GitHub Actions
- Linux amd64
- Go 1.27.1
- dependency lock با `go.mod` و `go.sum`

## gate استاندارد

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

## slow receiver — بسته‌شده با TWRL

failure تاریخی run `36338439633` باعث بازطراحی receive path شد. پس از TWRL، slow-receiver integration و تست deterministic credit replenishment در run `36340860552` **PASS** شدند و COR-01 run `36340860568` نیز PASS ماند.

## آزمون‌هایی که هنوز لازم‌اند

- slow-receiver پایدار و soak طولانی؛
- multi-Shard shared-budget stress؛
- resume/epoch/replay/tombstone؛
- state-machine fuzz طولانی؛
- benchmark رسمی 60s × 5؛
- عملیات certificate rotation/rollback؛
- real-path pilot.


## TWRL — slow receiver / three-watermark receive ledger
- Commit: `66c4d06d021f7d77d740017cd5e2f8262c158e9e`
- Standard CI run: `36340860552`
- Result: **PASS**
- `go test ./...`: PASS
- `go test -race ./...`: PASS
- `go vet ./...`: PASS
- protocol fuzz smoke: PASS
- slow-receiver integration: PASS
- deterministic two-credit replenishment test using `net.Pipe`: PASS

The receive path now distinguishes protocol acceptance (A), target delivery (D), and advertised credit (C). Target socket writes run in a per-Flow delivery pump backed by a fixed-capacity receive ring rather than blocking the Carrier read loop.

### COR-01 after TWRL
- Workflow run: `36340860568`
- Result: **PASS**
- bytes_each_direction: `1073741824`
- SHA-256: `1efd9d3aab21f9e312a2a0b5a6886b2a640c810ecb1fbe33f64614b26cfb27e3`
- test duration: `11.43s`

This remains a correctness result on GitHub-hosted local networking, not a public-network throughput benchmark.

</div>


## PADL و Stage-C scheduler
- Commit code: `eb89ea5d...`
- Consolidated gate commit: `fc64ef6b...`
- Standard CI run: `36341912070` — **PASS**
- COR-01 run: `36341912044` — **PASS**
- high-volume PADL liveness: 64 Flow × 256 item — PASS
- pressure preference / aging starvation bound / equal-pressure fairness — PASS

PADL هنوز performance claim ندارد؛ Stage E باید هزینه انتخاب prototype را با DRR baseline مقایسه کند.

## shared multi-Shard budget / conservation telemetry
- Commit: `efe837fb...`
- CI run: `36341666646` — **PASS**
- shared receive pool exhaustion: PASS
- capacity reuse after Flow release: PASS
- conservation snapshot invariant checks: PASS

## conservation metrics
- Commit: `692ec4e3...`
- CI run: `36341810504` — **PASS**
- loopback-only configuration contract: retained
- no peer/route/target/stream labels in baseline metrics: verified
- A-D / C-D / replay outstanding / invariant violation gauges: exported

## Stage-C soak
- Workflow: `.github/workflows/stagec-soak.yml`
- Current first run: `36342169299`
- Status at this report revision: **in progress**
- Gate: 25 repeated real-path integration cycles + 5 race-detector cycles

Stage C remains open until this workflow produces a clean PASS.
