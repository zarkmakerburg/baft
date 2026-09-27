<div dir="rtl" align="right" lang="fa">

# 02 — معماری، نقش‌ها و جریان داده

## نمای کلان

<div dir="ltr" align="left">

```text
┌──────────────────── IR ────────────────────┐
│ برنامه محلی                               │
│     │ TCP                                  │
│     ▼                                      │
│ Route listener                             │
│     │                                      │
│     ▼                                      │
│ Flow ──► Session/Shard ──► H2 Carrier =====╪══════╗
└────────────────────────────────────────────┘      ║ TLS 1.3 + mTLS
                                                    ║
┌──────────────────── EX ────────────────────┐      ║
│ H2 Carrier ◄════════════════════════════════╪══════╝
│     │                                      │
│ Session/Shard                              │
│     │                                      │
│ Route table: service-main                  │
│     │                                      │
│     ▼ TCP                                  │
│ مقصد ثابت                                  │
└────────────────────────────────────────────┘
```

</div>

## مرزهای مسئولیت

### carrier
Carrier فقط byte stream احرازشده، cancellation و اطلاعات transport را فراهم می‌کند. Route logic یا معنای payload متعلق به Carrier نیست.

### protocol
مسئول encode/decode BAFT/1، validation طول و نوع و parsing تدریجی است. این لایه نباید شبکه مقصد را dial کند.

### session
مالک handshake، state فریم‌ها، Flow map و در آینده epoch/resume است.

### routes
Route ID را به مقصد ثابت و مجاز resolve می‌کند. مقصد ورودی آزاد از peer نمی‌پذیرد.

### identity
TLS trust، استخراج هویت، allowlist و revocation را نگهداری می‌کند.

### resources
یک منبع واحد برای reservation حافظه receive و replay است.

### scheduler
DATA را برحسب بایت زمان‌بندی می‌کند. محتوای payload را تفسیر نمی‌کند.

### node
پیکربندی را به runtime واقعی IR یا EX تبدیل می‌کند، listenerها را بالا می‌آورد و Shardها را می‌سازد.

## Carrier پایه

در baseline:

- IR به EX روی TCP متصل می‌شود.
- TLS 1.3 با mTLS برقرار می‌شود.
- ALPN باید `h2` باشد.
- هر Shard یک Transport مستقل دارد.
- هر Shard یک POST طولانی دوطرفه روی مسیر ثابت `/baft/v1/carrier` دارد.
- redirect، proxy محیطی و HTTP/1 fallback غیرفعال‌اند.
- پاسخ server قبل از کامل‌شدن request body flush می‌شود تا duplex واقعی ممکن باشد.

## Shard

Shard برای محدودکردن blast radius صف و state به‌کار می‌رود. پیش‌فرض Blueprint چهار Shard است و schema فعلی 1 تا 8 را می‌پذیرد. Shardها نباید صرفاً streamهای منطقی روی یک TCP واحد باشند؛ baseline آن‌ها را با Transport مستقل پیاده می‌کند.

## Flow

هر Flow یک اتصال TCP کاربردی است. دو جهت Flow offset مستقل دارند. در یک جهت:

<div dir="ltr" align="left">

```text
socket read
   │
   ▼
reserve replay capacity
   │
   ▼
DATA(offset)
   │
   ▼
peer validates / accepts bytes
   │
   ├─ ACK(next_expected)
   └─ WINDOW(max_offset)
```

</div>

Half-close با `FIN(final_offset)` و `FIN_ACK(final_offset)` مدل می‌شود؛ بستن یک جهت به معنی حذف فوری جهت دیگر نیست.

## Session handshake

برای Session جدید:

<div dir="ltr" align="left">

```text
IR                     EX
│                      │
├──── HELLO ──────────► │
│ ◄── HELLO_ACK ────────┤
├──── READY ──────────► │
│ ◄──── READY ──────────┤
│                      │
│  application frames  │
```

</div>

OPEN یا DATA قبل از READY دوطرفه protocol error است.

## مالکیت writer

برای جلوگیری از interleave شدن bytes فریم‌ها، نوشتن روی هر Carrier از مسیر writer/sender مالک انجام می‌شود. Control و DATA صف‌های متفاوت دارند اما در نهایت serialization روی wire یک نقطه مالک دارد.

## shutdown و cancellation

Cancellation بخشی از قرارداد Carrier است. با لغو context، read/writeهای Carrier و Flowها باید آزاد شوند. ابطال اضطراری peer نیز Carrier فعال را cancel می‌کند، نه اینکه فقط اتصال بعدی را رد کند.

</div>
