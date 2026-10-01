<div dir="rtl" align="right" lang="fa">

# 06 — ساخت و اجرای IR و EX

## هشدار وضعیت

این بخش مسیر اجرای فعلی کد را توضیح می‌دهد، نه دستور production deployment نهایی. systemd، rotation کامل، admin transaction و packaging نهایی در مراحل بعدی تکمیل می‌شوند.

## ساخت

<div dir="ltr" align="left">

```bash
git clone https://github.com/zarkmakerburg/baft.git
cd baft
go build -o baft ./cmd/baft
./baft version
./baft version --json
```

</div>

## آماده‌سازی فایل‌ها

روی هر Node به‌صورت مفهومی نیاز است:

<div dir="ltr" align="left">

```text
/etc/baft/
├── ir.yaml یا ex.yaml
└── pki/
    ├── ca.pem
    ├── node.pem
    └── node.key
```

</div>

کلید private را محدود کنید؛ برای نمونه روی Linux:

<div dir="ltr" align="left">

```bash
chmod 600 /etc/baft/pki/node.key
```

</div>

BAFT فایل کلیدی با permission باز برای group/other را رد می‌کند.

## ترتیب راه‌اندازی پیشنهادی آزمایشگاهی

### 1. EX را configure کنید

- `role: listener`
- `server.listen` را تعیین کنید.
- `server_name` با SAN گواهی server هماهنگ باشد.
- identityهای مجاز IR را در `allowed_peer_identities` ثبت کنید.
- Routeهای inbound و target ثابت را تعریف کنید.

### 2. IR را configure کنید

- `role: dialer`
- `peer.address` را به EX تنظیم کنید.
- `peer.server_name` را با certificate hostname هماهنگ کنید.
- `allowed_identity` را identity گواهی EX بگذارید.
- Route محلی outbound را روی loopback تعریف کنید.

### 3. هر دو config را validate کنید

<div dir="ltr" align="left">

```bash
./baft config validate --file /etc/baft/ex.yaml
./baft config validate --file /etc/baft/ir.yaml
```

</div>

### 4. ابتدا EX را اجرا کنید

<div dir="ltr" align="left">

```bash
./baft run --file /etc/baft/ex.yaml
```

</div>

### 5. سپس IR را اجرا کنید

<div dir="ltr" align="left">

```bash
./baft run --file /etc/baft/ir.yaml
```

</div>

### 6. برنامه محلی را به listener IR وصل کنید

اگر Route نمونه روی `127.0.0.1:1443` است، برنامه فقط به همان port وصل می‌شود. BAFT این Flow را به Route نام‌گذاری‌شده در EX می‌فرستد.

## چه اتفاقی هنگام اتصال می‌افتد؟

1. IR Shard Carrier را به EX dial می‌کند.
2. TLS 1.3 و mTLS اعتبارسنجی می‌شوند.
3. H2 full-duplex روی مسیر ثابت Carrier شکل می‌گیرد.
4. HELLO/HELLO_ACK مبادله می‌شود.
5. دو طرف READY می‌شوند.
6. وقتی local TCP روی IR پذیرفته شد، OPEN برای Route ارسال می‌شود.
7. EX peer و Route را authorize می‌کند.
8. EX target ثابت را dial می‌کند.
9. OPEN_OK و WINDOW مبادله می‌شوند.
10. DATA دوطرفه منتقل می‌شود.
11. ACK/WINDOW backpressure را هدایت می‌کنند.
12. EOF یک سمت با FIN/FIN_ACK به half-close تبدیل می‌شود.

## shutdown

CLI از signalهای `SIGINT` و `SIGTERM` context cancellation می‌سازد. Runtime باید listenerها، Carrierها و Flowها را بدون باقی‌گذاشتن goroutine دائمی آزاد کند.

## خطاهای متداول

### invalid config
ابتدا `config validate` را اجرا کنید. unknown field و YAML ambiguity عمداً خطا هستند.

### TLS hostname mismatch
`peer.server_name` باید با SAN گواهی EX تطبیق داشته باشد؛ IP اتصال جای آن را نمی‌گیرد.

### unauthorized peer
CA-valid بودن گواهی کافی نیست. identity باید در peer allowlist و در Route مربوط نیز مجاز باشد.

### route not found / denied
IR فقط Route ID می‌فرستد. وجود Route و مجوز peer را در EX بررسی کنید.

### private key permission
permission فایل key را محدود کنید. هدف این check جلوگیری از شروع Node با secret file بیش‌ازحد قابل‌دسترسی است.

## آنچه هنوز عملیات production محسوب نمی‌شود

در وضعیت فعلی، این بخش‌ها کامل اعلام نشده‌اند:

- installer و package نهایی؛
- systemd hardening نهایی؛
- admin Unix-socket API کامل؛
- atomic config transaction/rollback؛
- certificate rotation workflow کامل؛
- support bundle؛
- real-path pilot و rollback عملیاتی.

## مسیر نصب: pairing هر دو config را می‌سازد

حالا `install.sh` بدون نوشتن دستی YAML یک جفت قابل اجرا می‌سازد:

1. روی EX دستور زیر را اجرا کنید. باینری‌ها نصب می‌شوند (بخش «باینری‌ها از کجا می‌آیند» را ببینید)، کلید Noise و PKI بیرونی TLS ساخته می‌شود (با `baft-pair pki`، بدون نیاز به OpenSSL) و یک کد یک‌بارمصرف `BAFTPAIR1:` چاپ می‌شود. بعد installer منتظر کد پاسخ IR می‌ماند (با `BAFT_NONINTERACTIVE=1` دستور `baft-pair ex-accept` را برای اجرای بعدی چاپ می‌کند).

<div dir="ltr" align="left">

```bash
sudo bash install.sh --role ex --public-address HOST
```

</div>

2. روی IR دستور زیر را با همان کد اجرا کنید. `baft-pair ir-apply --config-out` فایل `/etc/baft/baft.yaml` را می‌نویسد (dialer در حالت Noise، pin‌شده به کلید EX، بدون گواهی کلاینت)، سرویس بالا می‌آید و یک کد `BAFTREPLY1:` چاپ می‌شود.

<div dir="ltr" align="left">

```bash
sudo bash install.sh --role ir --pairing-code BAFTPAIR1:...
```

</div>

3. کد پاسخ را در EX بچسبانید. `baft-pair ex-accept` آن را با HMAC و کلید PSK یک‌بارمصرفِ کد pairing بررسی می‌کند (پاسخ کسی که کد را ندارد رد می‌شود)، config سمت listener را pin‌شده به کلید IR می‌نویسد، PSK را پاک می‌کند و سرویس بالا می‌آید.

بعد از آن برنامه‌های محلی به `BAFT_ROUTE_LISTEN` روی IR وصل می‌شوند (پیش‌فرض `127.0.0.1:1443`) و EX ترافیک را به `BAFT_TARGET` می‌فرستد (پیش‌فرض `127.0.0.1:2443`؛ باید IP ثابت باشد). تا وقتی EX پاسخ را نپذیرفته، dialer روی IR خارج می‌شود و systemd هر ۲ ثانیه دوباره اجرایش می‌کند.

اسکریپت `tests/e2e/pair_and_run.sh` همین pairing را با باینری واقعی اجرا می‌کند و داده رد می‌کند؛ `tests/e2e/install_two_roles.sh` خود `install.sh` را برای هر دو نقش روی یک ماشین اجرا می‌کند. CI آن را دو بار اجرا می‌کند: یک بار از سورس (`e2e-install`) و یک بار از release امضاشده با کلیدهای یک‌بارمصرف (`e2e-install-release`).

### باینری‌ها از کجا می‌آیند

در حالت پیش‌فرض (`BAFT_INSTALL_FROM=release`) installer فقط `curl`، `openssl` و `python3` لازم دارد؛ Go، git و کامپایلر روی سرور نصب نمی‌شود. مراحل:

1. فایل‌های `manifest.json`، `release-key.cert.json`، `SHA256SUMS` و باینری‌های `baft` و `baft-pair` همین معماری را از آخرین release گیت‌هاب دانلود می‌کند (`--version vX.Y.Z` نسخهٔ مشخص، و `BAFT_RELEASE_URL` محل دیگر). فهرست ابطال فعلی را هم از `BAFT_REVOCATIONS_URL` می‌گیرد (پیش‌فرض `release/keys/revocations.json` روی `main`).
2. قبل از اجرای هر چیز دانلودشده، طبق قواعد [23-p1a-signed-releases.md](23-p1a-signed-releases.md) verify می‌کند: کلید Root که در `install.sh` pin شده (`BAFT_PINNED_ROOT_PUB`)، گواهی، فهرست ابطالِ اجباری و منقضی‌نشده، امضای manifest، `SHA256SUMS` و hash هر باینری. فقط فایل‌های دانلودشده لازم‌اند و هر فایل دیگری رد می‌شود.
3. نسخهٔ قدیمی‌تر از نسخهٔ نصب‌شده را رد می‌کند (مگر با `--allow-downgrade`)، و همان نسخه از commit دیگر را هم رد می‌کند. این کار با فایل `$BAFT_PREFIX/release-state.json` انجام می‌شود (پیش‌فرض `/opt/baft/release-state.json`) که مالکش root است.
4. باینری‌ها را نصب می‌کند و فقط بعد از آن release را در همان فایل ثبت می‌کند.

اگر verify شکست بخورد چیزی نصب نمی‌شود. تا وقتی صاحب پروژه کلید Root را نساخته و در installer pin نکرده، نصب از release با پیام روشن متوقف می‌شود؛ `--from-source` (`BAFT_INSTALL_FROM=source`) مسیر قدیمی clone و build را برای توسعه نگه می‌دارد. `tests/installer` verifierِ installer را با releaseهایی که `internal/release` امضا کرده بررسی می‌کند، شامل دستکاری، ابطال، فهرست تکراری قدیمی، downgrade و re-tag.

</div>
