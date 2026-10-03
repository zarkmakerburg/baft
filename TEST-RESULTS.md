<div dir="rtl" align="right" lang="fa">

# نتایج آزمون

> English: [TEST-RESULTS.en.md](TEST-RESULTS.en.md)  
> تاریخ: 2026-10-03

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


## P0-I / Step 5.7 freeze-candidate evidence — 2026-10-03

Baseline: `29125392566021a286af20ff8f3a4907c28dbee8` روی `main`.

- CI run `37125867545`: **PASS**؛ همه jobهای استاندارد سبز، شامل test/race/vet/fuzz و e2eهای نصب/اجرا.
- Stage-C soak run `37125867530`: **PASS**.
- Step 5.7 Recovery Soak run `37125867525`: **PASS در 5 run attempt مستقل روی همان SHA**.
- در هر attempt، Topology × ECRL authority isolation و گیت‌های Class-A/Class-B/replay/finalization/lifetime/resource/flow-churn اجرا و پاس شدند.
- PR #60 روی merge SHA فوق، instrumentation cancellation cause را برای تشخیص failureهای بعدی حفظ می‌کند.

این شواهد qualification فعلی را سبز می‌کند و Step 5.7 را به وضعیت **freeze candidate pending HQ acceptance** می‌رساند. Scope همان same-process recovery است؛ restart/reboot resume همچنان خارج از scope است.

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
- Failure تاریخی: run `36342169299` — **FAIL**؛ `TestConcurrentMultiFlowTransfer` با `unexpected EOF`.
- Pass تاریخی بعدی: run `36342627897` — **PASS**.
- شواهد code-head پیش از commit مستندات: run `36519987991` روی `6fcf41631dc963af6f9c124f245a3ce7a47bc2fe` — **PASS**.
- Gate فعلی: 25 repeated real-path integration cycles + 5 race-detector cycles.

این نتیجه Stage-C gate فعلی را سبز می‌کند؛ performance benchmark عمومی یا production readiness از آن نتیجه‌گیری نمی‌شود.


## R3.1 — شواهد محلی

[گزارش هوشا](reports/HOOSHA-R3.1.md) و لاگ‌های همان پوشه شامل آزمون‌های آماری، Differential، race و هزینه واقعی jitter است. نتایج این مرحله نباید با CI یا پایلوت واقعی ایران اشتباه گرفته شود.


## Step 5.7 P0 — baseline recovery generation readiness

Baseline مأموریت: `c7237c09a673bc4442ad6e2c3a58dc8d222ce551`.

Root cause تأیید شد: در initial finalize، `carrierGeneration` از 1 به 2 می‌رفت اما wake channel بدون advance شدن `replacementReadyGeneration` منتشر می‌شد. در نتیجه waiter نسل 1 بیدار می‌شد ولی شرط `readyGeneration > oldGeneration` برقرار نبود و دوباره تا retention timeout منتظر می‌ماند.

اصلاح:
- یک frontier واحد و monotonic برای application readiness؛
- publish readiness با generation صریح؛
- stale generation بدون wake/progress؛
- initial finalize و rebind از semantics یکسان؛
- readiness فقط بعد از replay/ACK/FIN reconciliation و pump restoration منتشر می‌شود.

شواهد code-head پیش از commit مستندات روی `6fcf41631dc963af6f9c124f245a3ce7a47bc2fe`:
- Full CI push `36519987982`: **PASS**
- Full CI PR `36519992111`: **PASS**
- Recovery-specific soak `36519987974`: **PASS**
  - active-flow carrier replacement: 20× PASS
  - 8-flow replacement: 10× PASS
  - six-route identity isolation: 5× PASS
  - distributed uncertainty/finalization/replay/FIN matrix: 10× PASS
  - consecutive replacement lifecycle: 10× PASS
  - recovery race sample: PASS
- Stage-C soak `36519987991`: **PASS**
- `go test ./...`: PASS
- `go test -race ./...`: PASS
- `go vet ./...`: PASS
- protocol fuzz smoke: PASS

این evidence فقط same-process recovery را پوشش می‌دهد. process-restart/machine-reboot resume و durable ECRL session snapshots همچنان خارج از scope و unsupported هستند.

</div>
