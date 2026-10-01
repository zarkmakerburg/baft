<div dir="rtl" align="right" lang="fa">

# برنامه توسعه

> نسخه انگلیسی: [PLAN.en.md](PLAN.en.md)

## Stage A — قرارداد و spike

- [x] ثبت مرجع Blueprint/Master Prompt
- [x] ساخت repository و فایل‌های وضعیت
- [x] pin نسخه Go
- [x] BAFT/1 parser و golden vectors
- [x] schema پیکربندی سخت‌گیرانه
- [x] test PKI
- [x] H2 full-duplex + mTLS + cancellation
- [x] اثبات چهار اتصال مستقل برای چهار Shard
- [x] CI روی Go 1.27.1

## Stage B — secure vertical slice

- [x] HELLO / HELLO_ACK / READY
- [x] Route ثابت allowlisted
- [x] OPEN / DATA / ACK / WINDOW / FIN
- [x] half-close و hash end-to-end
- [x] negative certificate tests
- [x] duplicate key / target injection rejection
- [x] duplicate DATA و invalid ACK/WINDOW tests
- [x] idempotent OPEN
- [x] active peer/certificate revocation
- [x] RESET و error code ثابت
- [x] COR-01 یک GiB دوطرفه
- [x] strict YAML loader و dependency lock
- [x] CLI واقعی IR/EX

## Stage C — منابع، fairness و چند Flow

- [x] allocator سراسری data memory
- [x] pool مستقل receive/replay
- [x] per-flow reservation
- [x] آزادسازی replay با ACK
- [x] backpressure قبل از read اضافی source
- [x] DRR برحسب byte در data path
- [x] bounded control queue
- [x] control priority + data anti-starvation burst cap
- [x] multi-Flow integration test
- [x] تثبیت slow-receiver liveness gate با TWRL و تست deterministic
- [ ] تست طولانی slow receiver / soak
- [x] تست فشار چند Shard با memory budget مشترک و reuse بدون leak
- [x] conservation metrics برای allocator/Flow بدون label حساس و پرکاردینالیتی
- [ ] بستن Stage C بعد از PASS شدن workflow مستقل Stage-C soak بدون OOM/race/deadlock

## Stage D — resume دقیق

- [x] prior-art review اجباری Raft / QUIC / TLS 1.3
- [x] threat model مجزا از Epoch پایه
- [x] invariant رسمی ECRL (I0–I6)
- [x] falsification criteria (F01–F10)
- [ ] property/fault-injection suite برای I0–I6 و F01–F09
- [ ] boot/session/epoch fencing
- [ ] ownership دقیق Carrier
- [ ] snapshot state
- [ ] replay روی Carrier جایگزین
- [ ] tombstone
- [ ] duplicate-free replacement
- [ ] state-machine fuzz
- [ ] COR-04 تا COR-11

## Stage D2 — endpoint pool / relay

- [ ] endpoint model با dial address جدا از identity
- [ ] health / retry / hysteresis / cooldown محدود
- [ ] fixed-upstream relay
- [ ] عدم پذیرش arbitrary destination
- [ ] Worker path فقط experimental و جدا از core

## Stage E — performance

- [ ] benchmark manifest
- [ ] 60 ثانیه × 5 تکرار
- [ ] profiler
- [ ] baseline comparison
- [ ] raw results
- [ ] گزارش صریح pass/fail

## Stage F — operations

- [ ] systemd hardening
- [ ] installer idempotent
- [ ] config transaction + rollback
- [ ] drain/stop semantics
- [ ] certificate rotation
- [ ] support bundle بدون payload/secret
- [ ] packaging

## Stage G — research

- [ ] H3 تحت flag
- [ ] A/B experiments
- [ ] resource-bounded research policy
- [ ] detectability study با ادعای محدود و قابل‌اندازه‌گیری

## Stage H — پایلوت واقعی

- [ ] نصب کنار مسیر موجود
- [ ] یک Route آزمایشی
- [ ] health واقعی
- [ ] چند بازه آزمایش
- [ ] rollback
- [ ] گزارش محدودیت و نتیجه


## R2 v0.1 — وضعیت اجرا

- [x] تشخیص Debian/Ubuntu در installer
- [x] تشخیص amd64/arm64
- [x] نصب/تطبیق Go 1.27.1 با checksum manifest
- [x] build با `-trimpath -ldflags="-s -w"`
- [x] نصب `/usr/local/bin/baft`
- [x] ساخت system user `baft`
- [x] systemd hardening شامل `NoNewPrivileges=true` و `PrivateTmp=true`
- [x] ابزار `baft-pair`
- [x] descriptor `BAFTPAIR1`
- [x] apply اتمیک pairing state در IR
- [x] Noise Pattern IK با `github.com/flynn/noise v1.1.0`
- [x] enrollment اولیه با `IKpsk0`
- [x] تست Noise روی HTTP/2 در حضور TLS-terminating intermediary
- [x] race test
- [ ] اتصال SecurityInternal به `node.Runtime` production data path
- [ ] حذف اتمیک PSK یک‌بارمصرف پس از pin موفق در runtime واقعی
- [ ] تست نصب کامل روی VM تازه Debian و Ubuntu
- [ ] Stage D Replay Engine — **PAUSED**

### Non-goal فعلی

probabilistic timing/packet morphing برای دورزدن traffic analysis یا فیلترینگ در R2 v0.1 پیاده‌سازی نشده است.


## R3 safe — برنامه

- [x] `internal/recordshape` با bucket padding deterministic
- [x] اعمال shaping بعد از Noise encryption و قبل از wire
- [x] propagation گزینه از EX داخل Pairing descriptor
- [x] `--enable-record-shaping` در installer
- [x] تست bounded wire sizes و round-trip
- [x] Integration Noise/H2 intermediary با shaping روشن
- [ ] production wiring به `node.Runtime`
- [ ] VM install test روی Debian/Ubuntu
- [ ] R4 public distribution — **BLOCKED تا بعد از review**
- [ ] Stage D Replay — **PAUSED**

### خارج از scope

random packet-size morphing و timing jitter برای شکست تحلیل آماری یا سامانه‌های فیلترینگ در این شاخه پیاده‌سازی نمی‌شوند.


## R3.1 — نامزد بررسی v0.2-Pro

Padding احتمالی نرمال/لاپلاس، jitter قابل تنظیم و پاسخ HTML پیش از ورود به Session پیاده شد. حالت صریح Noise با peer pin‌شده اکنون به Runtime متصل است. آزمون‌های محلی و Differential مدل/موتور ECRL پاس شدند؛ Stage D و recovery همچنان متوقف‌اند.

[گزارش سه‌بخشی هوشا و شواهد](reports/HOOSHA-R3.1.md). این بخش وضعیت فعلی شاخه R3.1 است و توضیحات R2/R3 بالا سوابق تاریخی‌اند. نسخه نهایی v0.2-Pro، نصب VM تازه، حفظ سرعت با jitter و اثبات مقاومت فیلترینگ هنوز تأیید نشده‌اند.

</div>
