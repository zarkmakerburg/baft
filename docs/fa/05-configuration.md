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

## recovery

<div dir="ltr" align="left">

```yaml
recovery:
  enabled: false
  retention_seconds: 30
```

</div>

تا پیش از کامل‌شدن Stage D، `enabled: true` باید با خطای واضح رد شود؛ silently ignoring ممنوع است.

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
