<div dir="rtl" align="right" lang="fa">

# 05 — پیکربندی و Routeها

## فرمت

پیکربندی schema version 1 است. نمونه‌های اصلی:

- `configs/example-ir.yaml`
- `configs/example-ex.yaml`
- `configs/schema-v1.json`

Runtime فایل‌های `.yaml`، `.yml` و `.json` را می‌پذیرد.

## parser سخت‌گیرانه YAML

Loader فعلی برای کاهش ambiguity این موارد را رد می‌کند:

- duplicate mapping key؛
- anchor و alias؛
- merge key با `<<`؛
- custom tag؛
- چند YAML document در یک فایل؛
- nesting بیش از حد؛
- ورودی بزرگ‌تر از 1 MiB؛
- فیلد ناشناخته در مدل typed.

پس از parse YAML، همان مسیر validation typed که JSON استفاده می‌کند اجرا می‌شود.

## بخش node

IR:

<div dir="ltr" align="left">

```yaml
node:
  id: ir-01
  role: dialer
```

</div>

EX:

<div dir="ltr" align="left">

```yaml
node:
  id: ex-01
  role: listener
```

</div>

`id` شناسه محلی Node است و باید با قرارداد هویت نصب هم‌خوان باشد.

## بخش peer برای dialer

<div dir="ltr" align="left">

```yaml
peer:
  address: 192.0.2.20:443
  server_name: ex.example
  allowed_identity: urn:baft:node:ex-01
```

</div>

- `address`: آدرس واقعی dial؛
- `server_name`: نامی که TLS hostname verification بر اساس آن انجام می‌شود؛
- `allowed_identity`: URI identity مورد انتظار از گواهی peer.

این سه مفهوم عمداً از هم جدا هستند. تغییر IP نباید باعث حذف hostname verification شود.

## بخش server برای listener

<div dir="ltr" align="left">

```yaml
server:
  listen: 0.0.0.0:443
  server_name: ex.example
  allowed_peer_identities:
    - urn:baft:node:ir-01
```

</div>

`allowed_peer_identities` فقط peer-level admission است؛ Route همچنان allowlist مستقل خود را دارد.

## TLS

<div dir="ltr" align="left">

```yaml
tls:
  min_version: "1.3"
  ca_file: /etc/baft/pki/ca.pem
  cert_file: /etc/baft/pki/ir.pem
  key_file: /etc/baft/pki/ir.key
  session_tickets: false
```

</div>

در baseline:

- نسخه کمتر از 1.3 رد می‌شود؛
- pathها باید مشخص باشند؛
- session ticket غیرفعال است؛
- کلید private هنگام run نباید برای group/other قابل خواندن/نوشتن باشد.

## transport

<div dir="ltr" align="left">

```yaml
transport:
  primary: h2
  h3_enabled: false
  shards: 4
  profile: secure-fast
```

</div>

در baseline فقط `h2` پذیرفته می‌شود و `h3_enabled: true` تا عبور گیت مربوط پشتیبانی نمی‌شود. تعداد Shard بین 1 و 8 است.

## limits

<div dir="ltr" align="left">

```yaml
limits:
  max_flows: 256
  data_memory_mib: 256
  receive_initial_kib: 64
  receive_max_mib: 16
  replay_max_mib: 16
```

</div>

هدف این فیلدها جلوگیری از resource growth نامحدود است.

Runtime Stage C فعلی budget داده را به poolهای receive و replay غیرقابل‌قرض‌دادن تقسیم می‌کند. per-flow cap نباید از pool مربوط بزرگ‌تر باشد.

`max_flows` سقف Flowهای هم‌زمان کل نود است و بین همه peerها و Shardها مشترک است. سمت EX، OPEN بعد از رسیدن به سقف بدون dial به target با `OPEN_ERR RESOURCE_EXHAUSTED` رد می‌شود؛ سمت IR اتصال محلی جدید بدون ارسال OPEN بسته می‌شود. جدا از آن، هر Shard حداکثر ۶۴ Flow می‌پذیرد (`max_flows_per_shard` در HELLO_ACK)؛ مقدار نمونه `256` برابر ۴ Shard × ۶۴ است.

## recovery

<div dir="ltr" align="left">

```yaml
recovery:
  enabled: false
  retention_seconds: 30
```

</div>

`recovery.enabled: true` تعویض Carrier در همان process با ECRL را فعال می‌کند (Step 5.7): وقتی Carrier یک Shard از کار بیفتد، Session زنده به‌جای پایان یافتن، با epoch fencing و bounded replay به Carrier جدید متصل می‌شود. در این حالت `retention_seconds` باید بین 1 و 300 باشد؛ `mode` می‌تواند حذف شود یا `same_process` باشد.

`durable: true` و هر `mode` دیگر با خطای واضح رد می‌شوند و silently ignoring ممنوع است: وضعیت recovery پایدار نمی‌شود، پس resume بعد از restart پردازه یا reboot ماشین پشتیبانی نمی‌شود. محدوده دقیق در [محدودیت‌های شناخته‌شده](../../KNOWN-LIMITATIONS.md) آمده است.

## Route خروجی روی IR

<div dir="ltr" align="left">

```yaml
routes:
  - id: service-main
    listen: 127.0.0.1:1443
    remote_route: service-main
    direction: outbound
    traffic_class: interactive
```

</div>

قواعد مهم:

- listener baseline روی loopback است؛
- `remote_route` نام Route روی peer است؛
- target در Route خروجی IR وجود ندارد.

## Route ورودی روی EX

<div dir="ltr" align="left">

```yaml
routes:
  - id: service-main
    direction: inbound
    target: 127.0.0.1:2443
    allowed_peers:
      - urn:baft:node:ir-01
```

</div>

در implementation baseline، target باید IP ثابت + port باشد؛ hostname آزاد، wildcard و مقصد peer-supplied پذیرفته نمی‌شود.

## revocation روی EX

<div dir="ltr" align="left">

```yaml
revocation:
  file: /etc/baft/revoked.yaml
```

</div>

بخش اختیاری `revocation` فقط برای listener مجاز است، چون فقط listener هویت peer هر Carrier را احراز می‌کند؛ مسیر باید مطلق باشد. فایل فهرست با همان parser سخت‌گیرانه YAML (یا JSON) خوانده می‌شود و فایل خالی یعنی فهرست خالی:

<div dir="ltr" align="left">

```yaml
identities:
  - urn:baft:node:ir-02
serials:
  - "0A:1B:2C"
fingerprints:
  - "<SHA-256 گواهی، 64 رقم hex>"
```

</div>

- اگر `revocation.file` تنظیم شده ولی فایل نیست یا نامعتبر است، node شروع نمی‌شود (fail-closed).
- `systemctl reload baft` (سیگنال SIGHUP) فایل را دوباره می‌خواند؛ Carrierهای فعال peer تازه revoke‌شده فوراً قطع می‌شوند. فایل نامعتبر در reload رد می‌شود و فهرست فعلی حفظ می‌شود.
- revocation فقط افزایشی است: حذف یک مورد از فایل تا restart بعدی اثر ندارد.
- serialها مستقل از `:` و صفرهای ابتدایی و بزرگی/کوچکی حروف مقایسه می‌شوند. در حالت Noise فقط `identities` اثر دارد، چون آنجا گواهی کلاینت در TLS بیرونی وجود ندارد.

## management و metrics

<div dir="ltr" align="left">

```yaml
management:
  unix_socket: /run/baft/admin.sock
  metrics_listen: 127.0.0.1:9191
```

</div>

طراحی مرجع admin را روی Unix socket محدود و metrics را روی loopback نگه می‌دارد. کامل‌شدن API مدیریتی متعلق به مراحل عملیات است و وجود فیلد config به معنی کامل‌بودن تمام commandهای admin نیست.

## logging

<div dir="ltr" align="left">

```yaml
logging:
  level: info
  payload: false
```

</div>

`payload: true` در baseline رد می‌شود.

## اعتبارسنجی

<div dir="ltr" align="left">

```bash
baft config validate --file /etc/baft/ir.yaml
```

</div>

خروجی `valid` فقط به معنی معتبر بودن schema/semantic config است؛ اتصال شبکه، certificate files و دسترس‌پذیری target را تضمین نمی‌کند.

</div>
