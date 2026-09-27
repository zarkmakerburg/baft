<div dir="rtl" align="right" lang="fa">

# 15D — ECRL: گیت طراحی، prior-art، مدل تهدید و invariant رسمی

> **وضعیت:** سند طراحی اجباری پیش از ادامه پیاده‌سازی Stage D  
> **نام:** Epoch-Fenced Correlated Resume Ledger (ECRL)  
> **قاعده:** تا زمانی که این سند و تست‌های ابطال آن مبنا قرار نگیرند، هیچ توسعه‌ی جدیدی در wire/session resume انجام نمی‌شود.

## 1. وضعیت ادعا در سه سطح

| سطح | وضعیت فعلی | معنی |
|---|---|---|
| **فرضیه پژوهشی** | **فعال و محدود** | گزاره‌ی قابل‌ابطال: **«commit اتمیکِ جفتِ (epoch، A-vector) می‌تواند Carrier قدیمی را fence کند و هم‌زمان replay frontier هر Flow را دقیقاً روی A تثبیت کند، بدون اینکه model و engine روی trace معتبر اختلاف داشته باشند.»** |
| **نتیجه مهندسی پشتیبانی‌شده** | **هنوز محقق نشده** | وجود prototype یا unit test اولیه به‌تنهایی کافی نیست. تا وقتی carrier replacement واقعی، zombie-carrier، lost-ACK، tombstone و exact-once gate پاس نشوند، این سطح ادعا نمی‌شود. |
| **Patentability / novelty حقوقی** | **بررسی‌نشده** | این prior-art review فقط مهندسی است. هیچ ادعای ثبت‌پذیری یا novelty حقوقی بدون جست‌وجوی patent/literature تخصصی مجاز نیست. |

### نتیجه‌ی صریح prior-art review

**خودِ epoch fencing نوآوری ECRL نیست.**  
**خودِ resume یا anti-replay نیز نوآوری ECRL نیست.**

هسته‌ی ادعا فقط یک مکانیزم مشخص است: **atomic epoch + A-vector commit**. اگر engine واقعی در هر trace deterministic با reference model اختلاف داشته باشد، یا commit بتواند epoch را بدون همان plan هم‌بسته جلو ببرد، همین فرضیه رد می‌شود. FIN/tombstone، PADL و durable snapshot اجزای مستقل‌اند و بخشی از ادعای novelty این جمله نیستند.

---

## 2. Prior-art review اجباری

### جدول مقایسه

| prior art | مسئله‌ای که حل می‌کند | مکانیزم کلیدی | شباهت به ECRL | تفاوت دقیق ECRL | نتیجه novelty |
|---|---|---|---|---|---|
| **Raft term/epoch fencing** | جلوگیری از ادامه authority یک leader قدیمی پس از انتخاب leader جدید؛ جلوگیری از stale RPC | term به‌عنوان logical clock؛ term صعودی؛ RPC با term قدیمی رد می‌شود؛ در هر term حداکثر یک leader | ECRL نیز generation صعودی دارد و Carrier با epoch قدیمی را بعد از commit غیرمجاز می‌کند | ECRL **consensus، quorum، leader election یا replicated log ندارد**. epoch فقط مالکیت یک Carrier برای یک Session نقطه‌به‌نقطه را fence می‌کند. بخش ledger باید علاوه بر ownership، state بایتی هر Flow را با peer receive state correlate کند تا replay frontier، FIN و tombstone تعیین شوند. | **fencing به‌تنهایی prior art است.** تفاوت احتمالی فقط در coupling آن با per-Flow correlated resume ledger است. |
| **QUIC connection migration + Connection ID rotation** | حفظ همان connection هنگام تغییر IP/port و جلوگیری از وابستگی connection به 5-tuple؛ مدیریت CIDهای فعال/بازنشسته؛ duplicate suppression با packet number | Connection ID، path validation، NEW_CONNECTION_ID / RETIRE_CONNECTION_ID، packet-number space و duplicate suppression | هر دو می‌خواهند path/transport قدیمی نتواند state جدید را خراب کند و continuity حفظ شود | QUIC **همان transport connection** را با state داخلی QUIC زنده نگه می‌دارد. ECRL روی H2/TCP بعد از مرگ Carrier، یک **Carrier احرازشده‌ی جدید** می‌سازد و باید state application-relay شامل Flow offset، replay، FIN، receive ring و tombstone را دوباره reconcile کند. ECRL packet-number migration یا CID routing نیست. | **migration و retirement prior art هستند.** تفاوت احتمالی، application-layer reconstruction روی Carrier جدید با byte-ledger هم‌بسته است. |
| **TLS 1.3 session resumption / 0-RTT anti-replay** | کاهش هزینه handshake و محدودکردن replay داده‌ی 0-RTT | PSK/session ticket، single-use ticket یا ClientHello recording؛ application باید 0-RTT unsafe را مدیریت کند | هر دو با replay پس از برقراری مجدد ارتباط سروکار دارند | TLS درباره‌ی **cryptographic session establishment و early-data replay** است؛ نمی‌داند یک BAFT Flow تا چه offsetی accepted/delivered شده، کدام FIN رخ داده یا Flow tombstone شده است. ECRL بعد از احراز هویت Carrier جدید عمل می‌کند و trust را از ledger نمی‌گیرد. هدف آن جلوگیری از duplicate/loss در **application byte stream** است، نه جایگزینی anti-replay TLS. | **resumption و anti-replay prior art هستند.** تفاوت احتمالی، exact byte-state reconciliation بعد از Carrier loss است. |

### مرز با Raft دقیق‌تر

Raft termها برای تشخیص leader قدیمی به‌کار می‌روند و درخواست با term قدیمی رد می‌شود. Raft همچنین صریحاً می‌گوید fencing/consensus به‌تنهایی اجرای دقیقاً یک‌باره‌ی command را تضمین نمی‌کند؛ برای retryهای client از serial number و deduplication جداگانه استفاده می‌کند.

نتیجه برای BAFT:

> **Epoch فقط authority را تعیین می‌کند؛ exact-once byte continuity مسئله‌ای جداست.**

پس اگر ECRL فقط `current_epoch + 1` و reject کردن Carrier قدیمی باشد، **ECRL نوآوری نیست و طراحی ناقص است**.

### مرز با QUIC دقیق‌تر

QUIC با Connection ID اجازه می‌دهد connection از تغییر آدرس شبکه جان سالم ببرد. packet number نیز duplicate packet را در همان connection تشخیص می‌دهد.

ECRL در مسئله‌ای متفاوت کار می‌کند:

- TCP/H2 قبلی ممکن است کاملاً مرده باشد؛
- Carrier جدید TLS/H2 جدید دارد؛
- BAFT Session و Flowها باید در همان Process باقی مانده باشند؛
- replay buffer و receive ring باید با Carrier جدید ادامه یابند؛
- state برنامه مقصد نباید دوباره اجرا شود یا بایت تکراری بگیرد.

بنابراین ECRL نباید QUIC را بازسازی کند؛ باید **handoff بین دو Carrier مستقل را در لایه BAFT** مدل کند.

### مرز با TLS 1.3 دقیق‌تر

TLS 1.3 صریحاً هشدار می‌دهد که anti-replay لایه TLS حفاظت کامل در برابر چند نسخه‌ی application data ایجاد نمی‌کند و application باید رفتار retry را ایمن طراحی کند.

در BAFT:

- mTLS همچنان منبع authentication است؛
- ECRL مجاز نیست verification را دور بزند؛
- ECRL مجاز نیست از resume snapshot به‌عنوان credential استفاده کند؛
- حتی اگر TLS resumption در آینده فعال شود، ledger بایتی BAFT همچنان لازم است.

---

## 3. مدل تهدید ECRL

### فرض‌های مدل

ECRL در برابر peer کاملاً Byzantine طراحی نمی‌شود. فرض می‌کنیم دو Node متعلق به یک دامنه مدیریتی‌اند و mTLS سالم است، ولی شبکه و timing می‌توانند خصمانه باشند.

شبکه می‌تواند:

- packet/frame را drop کند؛
- اتصال را قطع و بعداً restore کند؛
- latency شدید و reordering ایجاد کند؛
- باعث شود Carrier قدیمی دیرتر دوباره readable/writable شود؛
- دو تلاش reconnect را هم‌زمان کند.

Node می‌تواند:

- crash/restart کند؛
- ACK یا FIN_ACK را پیش از قطع‌شدن از دست بدهد؛
- snapshot محلی stale یا ناسازگار داشته باشد؛
- به‌دلیل bug state متناقض ارائه دهد.

مهاجم on-path نباید بتواند mTLS جدید جعل کند. replay رمزنگاری‌شده‌ی TLS خارج از مدل ECRL است و باید توسط TLS مدیریت شود.

### چیزی که Epoch موجود از قبل پوشش می‌دهد

تعریف فعلی glossary:

> **Epoch = نسل Carrier یک Session برای fencing**

پس Epoch به‌تنهایی فقط این سؤال را جواب می‌دهد:

> «کدام نسل Carrier حق دارد بعد از commit به‌عنوان Carrier جاری شناخته شود؟»

Epoch به‌تنهایی جواب این سؤال‌ها را نمی‌دهد:

- replay باید از کدام byte offset شروع شود؟
- اگر ACK گم شده باشد چه مقدار state واقعاً در peer پذیرفته شده است؟
- اگر target فقط بخشی از accepted data را دریافت کرده باشد چه می‌شود؟
- اگر Flow قبل از قطعی بسته شده ولی peer آن را active تصور کند چه می‌شود؟
- اگر FIN_ACK گم شده باشد آیا FIN دوباره side effect ایجاد می‌کند؟
- اگر Process restart شده و replay/ring از دست رفته باشد آیا resume هنوز امن است؟

این‌ها **وظیفه ledger** هستند، نه Epoch.

### سناریوهای تهدید/خرابی مخصوص ECRL

#### T1 — Zombie Carrier

Carrier نسل `e` به‌خاطر partition عملاً مرده فرض می‌شود. Carrier جدید `e+1` reconcile و commit می‌شود. سپس path قدیمی دوباره writable می‌شود و DATA/ACK/WINDOW/FIN دیررس می‌فرستد.

**خطر:** تغییر state یا تحویل duplicate بعد از handoff.

**حفاظت مورد انتظار:** پس از commit نسل جدید، هیچ application frame از Carrier با epoch قدیمی نباید state قابل مشاهده را تغییر دهد.

#### T2 — Dual Candidate

دو reconnect هم‌زمان برای `e+1` ساخته می‌شوند.

**خطر:** هر دو خود را مالک بدانند و DATA را موازی replay کنند.

**حفاظت مورد انتظار:** قبل از commit فقط یک candidate می‌تواند lease معتبر برای epoch بعدی داشته باشد؛ application DATA روی candidate غیر-committed پذیرفته نمی‌شود.

#### T3 — Lost ACK

Sender تا offset `S` داده فرستاده است. آخرین ACK محلی `K` است، ولی peer واقعاً تا `A` داده را پذیرفته و ACK مربوط به `A` در قطع Carrier گم شده است، بنابراین:

<div dir="ltr" align="left">

```text
K < A <= S
```

</div>

**خطر:** resume ساده از `K` بایت‌هایی را replay می‌کند که peer از قبل پذیرفته است؛ اگر receiver state درست منتقل نشود، duplicate ممکن است به target برسد.

**حفاظت مورد انتظار:** replay frontier از peer-authenticated correlated state یعنی `A` تعیین شود، نه صرفاً از `K`.

#### T4 — Accepted ولی هنوز Delivered نشده

طبق TWRL ممکن است هنگام قطع:

<div dir="ltr" align="left">

```text
D < A
```

</div>

یعنی bytes بازه `[D,A)` داخل receive ring پذیرفته شده‌اند ولی هنوز به target نوشته نشده‌اند.

**خطر:** resume دوباره این bytes را از sender بگیرد و هم‌زمان ring قدیمی نیز آن‌ها را تحویل دهد.

**حفاظت مورد انتظار:** بازه `[D,A)` فقط از ring موجود تحویل شود و sender فقط از `A` به بعد replay کند.

#### T5 — Tombstone Resurrection

Flow کامل بسته شده ولی snapshot دیررس یا Carrier قدیمی هنوز آن را active می‌بیند.

**خطر:** target دوباره dial شود یا DATA پس از پایان Flow تحویل شود.

**حفاظت مورد انتظار:** tombstone retained باید monotonic باشد و resume اجازه بازگشت Flow بسته‌شده به OPEN را ندهد.

#### T6 — Lost FIN / FIN_ACK

FIN یا FIN_ACK در لحظه قطع Carrier گم می‌شود.

**خطر:** half-close دوباره side effect بدهد، tail داده حذف شود، یا Flow زودتر از موعد آزاد شود.

**حفاظت مورد انتظار:** FIN facts در ledger monotonic و correlated باشند؛ state terminal فقط جلو می‌رود، نه عقب.

#### T7 — Peer Restart

peer با `boot_id` جدید برمی‌گردد.

**خطر:** طرف مقابل تصور کند receive ring، replay buffer و socketهای قدیمی هنوز موجودند.

**حفاظت مورد انتظار:** resume رد شود؛ restart مسیر Session جدید است، نه resume.

#### T8 — Inconsistent Authenticated Snapshot

peer معتبر ولی buggy/stale مقدار `rx_accepted` بزرگ‌تر از bytes تولیدشده‌ی sender یا کوچک‌تر از ACKی که sender قبلاً مشاهده کرده اعلام می‌کند.

**خطر:** data invention، data loss یا rollback state.

**حفاظت مورد انتظار:** `STATE_MISMATCH` و عدم commit.

---

## 4. Invariant رسمی ECRL

### متغیرها

برای Session:

- `E` = epoch committed فعلی؛
- `Owner(E)` = تنها Carrier مجاز برای application frames در epoch جاری.

برای هر Flow `f` و هر جهت `d`:

- `S` = `tx_next` sender؛
- `K` = آخرین acceptance ACK مشاهده‌شده توسط sender؛
- `F` = کف معتبر snapshot/tombstone؛
- `A` = `rx_accepted` متناظر در peer؛
- `D` = `rx_delivered` متناظر در peer؛
- `C` = receive credit متناظر در peer؛
- `Ring` = bytes پذیرفته‌شده ولی تحویل‌نشده‌ی peer؛
- `Replay` = bytesی که sender باید بعد از handoff دوباره در دسترس داشته باشد؛
- `T(f)` = tombstone در صورت پایان Flow.

### رابطه‌ی K با نمادگذاری TWRL

#### تعریف رسمی K

برای یک Flow و یک جهت مشخص:

`K` = **بیشترین offset تجمعی ACK که sender در همان `session_id`، همان `boot_id` peer و همان epoch معتبر واقعاً دریافت کرده و در snapshot حافظه‌ای جاری خودش ثبت کرده است.**

پس K:

- watermark دانسته‌شده‌ی sender درباره‌ی پذیرش peer است؛
- از روی `ACK(offset)` معتبر به‌دست می‌آید؛
- به معنی تحویل به target نیست؛
- به معنی tombstone یا terminal offset نیست؛
- state پایدار روی دیسک نیست و بعد از Process restart قابل اتکا نیست؛
- فقط در صورت continuity همان Session/Boot/Epoch معتبر است.

از آنجا که peer فقط بعد از پذیرش پیوسته‌ی bytes در حافظه‌ی bounded خود ACK می‌دهد، K یک **lower bound دانسته‌شده برای A** است:

<div dir="ltr" align="left">

```text
K <= A
```

</div>

اما ACK قبل از target delivery صادر می‌شود؛ بنابراین K و D ترتیب ثابت ندارند.

#### مدل یکپارچه‌ی K/D/A/S/C

چهار رابطه‌ی ثابت طراحی:

<div dir="ltr" align="left">

```text
D <= A <= S <= C
K <= A
```

</div>

در این مدل:

- `D` = peer bytes delivered to target؛
- `A` = peer bytes accepted into bounded BAFT receive state؛
- `S` = sender bytes already produced/sent into the BAFT stream (`tx_next`)؛
- `C` = peer absolute receive-credit limit؛
- `K` = sender's last observed cumulative ACK.

**بین K و D partial order وجود دارد، نه total order.**

دیاگرام واحد صحیح:

<div dir="ltr" align="left">

```text
0 ─────── [ K and D may appear in either order ] ─────── A ─────── S ─────── C
            │                                │
            ├─ valid case 1: K <= D          │
            └─ valid case 2: D <  K          │

Always:
    K <= A
    D <= A <= S <= C
```

</div>

پس دو linearization معتبر ممکن است:

<div dir="ltr" align="left">

```text
case 1 — ACK lags target delivery:
0 ── K ── D ── A ── S ── C

case 2 — ACK leads target delivery:
0 ── D ── K ── A ── S ── C
```

</div>

برابری هر دو watermark نیز ممکن است.

#### آیا همیشه `K ≤ D ≤ A ≤ S ≤ C` برقرار است؟

**خیر.**

بخش زیر همیشه برقرار است:

<div dir="ltr" align="left">

```text
D <= A <= S <= C
K <= A
```

</div>

ولی `K <= D` تضمین نمی‌شود.

دو حالت نقض‌کننده‌ی ترتیب کامل:

1. **ACK جلوتر از delivery:** peer bytes را تا A پذیرفته و ACK کرده، اما target کند است. ممکن است `D < K <= A`.
2. **ACK گم‌شده یا دیررس:** target جلو رفته ولی sender آخرین ACK را ندیده است. ممکن است `K < D <= A`.

بنابراین هر implementation یا test که `K<=D` را invariant بگیرد، بخشی از رفتار صحیح TWRL را اشتباه رد خواهد کرد.

#### نتیجه‌ی بازبینی K_release و شکاف [D,K)

پس از ساخت trace اجباری، نتیجه این است:

**در threat model فعلی BAFT، با state فقط در حافظه و با شرط RESET پس از تغییر `boot_id`، هیچ trace معتبرِ بدون تغییر `boot_id` پیدا نشد که آزادسازی replay تا K به‌جای K_release باعث data loss شود.**

برهان:

<div dir="ltr" align="left">

```text
D < K <= A

ReceiverRing = [D,A)
therefore:
[D,K) subset-of [D,A)
```

</div>

اگر Carrier قطع شود ولی `boot_id` ثابت بماند، همان Process و همان receive ring پابرجاست. پس حتی اگر sender نسخه‌ی replay بازه `[D,K)` را آزاد کرده باشد، receiver هنوز نسخه‌ی authoritative آن را در ring دارد.

برای از دست‌رفتن این بازه باید state حافظه‌ای receiver از بین برود. در مدل فعلی این یعنی Process restart و تغییر `boot_id`؛ در این حالت ECRL resume را رد می‌کند و Flow/Session باید RESET/recreate شود.

تست deterministic این استدلال:

<div dir="ltr" align="left">

```text
TestECRLKReleaseNotRequiredForCurrentInMemoryResume
```

</div>

بنابراین ادعای قبلی که «K_release شکاف [D,K) را در ECRL فعلی می‌بندد» **پس گرفته می‌شود**.

### K_release فقط برای durable snapshot آینده

`K_release` فعلاً جزو wire/current ECRL نیست. آن را فقط به‌عنوان پیش‌نیاز یک mode آینده نگه می‌داریم که در آن receiver بتواند پس از restart از snapshot بادوام resume کند، یعنی حالتی که ring حافظه‌ای دیگر تضمین بقا ندارد.

در آن mode آینده:

- `K` = آخرین acceptance watermark؛
- `K_release` = آخرین delivery watermark بادوام/قابل‌اثبات که sender اجازه دارد replay را تا آن آزاد کند.

invariantهای آینده:

<div dir="ltr" align="left">

```text
K_release <= D
S - K_release <= R_replay
Credit_eff = min(C, K_release + R_replay)
```

</div>

`R_replay` سقف bytesی است که sender باید برای بازیابی durable نگه دارد.

### target کند، replay cap و نبود deadlock

تست:

<div dir="ltr" align="left">

```text
TestECRLFutureDurableSlowTargetReplayBoundNoDeadlock
```

</div>

یک target کند را مدل می‌کند که acceptance سریع‌تر از delivery جلو می‌رود. Sender فقط تا:

<div dir="ltr" align="left">

```text
Credit_eff = min(C, K_release + R_replay)
```

</div>

حق پیشروی دارد. وقتی replay به سقف می‌رسد، DATA جدید block می‌شود؛ اما delivery/ACK control خارج از PADL DATA eligibility ادامه دارد، `K_release` جلو می‌رود و ظرفیت دوباره باز می‌شود. در تمام trace:

<div dir="ltr" align="left">

```text
S - K_release <= R_replay
```

</div>

حفظ می‌شود و progress متوقف نمی‌شود.

### تعامل با PADL

در **ECRL فعلی in-memory**، PADL همان replay pressure واقعی current implementation را می‌بیند و این سند semantics آن را تغییر نمی‌دهد.

در **durable mode آینده**:

1. eligibility قبل از PADL محدود می‌شود:

<div dir="ltr" align="left">

```text
next_data_end <= Credit_eff
```

</div>

2. pressure مناسب Flow برابر retained replay خواهد بود:

<div dir="ltr" align="left">

```text
ReplayPressure = S - K_release
```

</div>

3. PADL فقط بین Flowهای eligible انتخاب می‌کند؛ اجازه ندارد `Credit_eff` یا reservation را دور بزند.
4. delivery ACK / control frameها در صف control جدا می‌مانند، بنابراین Flowی که به cap replay رسیده می‌تواند با پیشروی target دوباره unblock شود و circular deadlock ساخته نمی‌شود.

#### نماد F — کف معتبر snapshot/tombstone

`F` = **بزرگ‌ترین offsetی که یک snapshot/tombstone معتبر و retained تضمین می‌کند prefix `[0,F)` دیگر برای بازسازی به state ناپایدار قدیمی وابسته نیست.**

برای Flow فعال در مدل فعلی:

<div dir="ltr" align="left">

```text
F <= D <= A <= S <= C
K <= A
```

</div>

بین `F` و `K` ترتیب ثابت وجود ندارد.

در durable mode آینده، شرط اضافه می‌شود:

<div dir="ltr" align="left">

```text
K_release <= D
```

</div>

برای tombstone پایانی که همه‌ی bytes تا `final_offset` تحویل شده‌اند:

<div dir="ltr" align="left">

```text
F = D = A = S = final_offset
```

</div>

F مجوز آزادسازی replay در current in-memory mode نیست؛ نقش آن جلوگیری از rollback/resurrection هنگام reconciliation است.

#### اثر بر ECRL

ECRL هنگام resume نباید K را جای D یا A استفاده کند:

- `K` فقط می‌گوید sender **می‌داند** peer حداقل تا کجا accepted کرده است؛
- `A` از snapshot معتبر peer، authoritative receive frontier برای تعیین replay است؛
- `D` مرز exact-once target delivery است؛
- `S` انتهای bytes تولیدشده‌ی sender است؛
- `C` سقف credit است.

بنابراین replay frontier همچنان `A` است، نه K و نه D.

### Invariant صفر — یکتایی مالک Carrier

<div dir="ltr" align="left">

```text
AcceptApplicationFrame(c)  =>  epoch(c) = E  AND  c = Owner(E)

|CommittedOwners(E)| = 1
```

</div>

candidate پیش از commit فقط control مربوط به resume دارد و حق application DATA ندارد.

### Invariant یک — هم‌بستگی ACK/Receive

<div dir="ltr" align="left">

```text
F <= D <= A <= S <= C
K <= A
```

</div>

تفسیر:

- peer نمی‌تواند bytesی بیشتر از sender تولیدشده ادعا کند؛
- peer نمی‌تواند state را عقب‌تر از ACKی که sender قبلاً مشاهده کرده برگرداند؛
- TWRL نیز همچنان برقرار است.

### Invariant دو — Conservation در لحظه handoff

در لحظه commit Carrier جدید:

<div dir="ltr" align="left">

```text
Ring   = [D, A)
Replay = [A, S)

Ring ∩ Replay = ∅
Ring ∪ Replay = [D, S)
```

</div>

این invariant قلب ECRL است.

معنی آن:

- bytes تحویل‌نشده ولی پذیرفته‌شده فقط از receive ring ادامه پیدا می‌کنند؛
- bytes پذیرفته‌نشده فقط از sender replay می‌شوند؛
- هیچ gap بین `D` و `S` وجود ندارد؛
- هیچ overlap میان ring و replay وجود ندارد.

### Invariant سه — Exact-once target prefix

اگر قبل از قطعی target تا `D` دریافت کرده باشد:

<div dir="ltr" align="left">

```text
TargetDeliveredBefore = [0, D)

AfterResume:
  next_target_offset = D
  bytes < D are never delivered again
  [D, A) comes only from Ring
  [A, S) comes only from Replay/new DATA
```

</div>

duplicate DATA با offset کمتر از `A` ممکن است روی wire دیده شود، اما **نباید دوباره به target نوشته شود**.

### Invariant چهار — Tombstone monotonicity

برای Flow tombstone‌شده:

<div dir="ltr" align="left">

```text
T(f).epoch <= E  AND  TombstoneRetained(f)
    =>
NOT Reopen(f, E')
for every E' >= E during retention
```

</div>

Flow پایان‌یافته در بازه retention حق بازگشت به OPEN ندارد.

### Invariant پنج — Boot continuity

<div dir="ltr" align="left">

```text
ResumeCommit => peer.boot_id == expected_peer_boot_id
             AND session_id unchanged
```

</div>

تغییر `boot_id` یعنی resume ممنوع است.

### Invariant شش — Terminal monotonicity

<div dir="ltr" align="left">

```text
FIN_ACKED  => FIN_SENT
FIN_ACK_SENT => FIN_RECEIVED

terminal state may advance; it may never regress
```

</div>

### Invariant ترکیبی ECRL

نسخه فشرده‌ی شرط ایمنی:

<div dir="ltr" align="left">

```text
Commit(E+1)
=>
UniqueOwner(E+1)
AND
for every active Flow/direction:
    K <= A
    D <= A <= S <= C
    Ring   = [D,A)
    Replay = [A,S)
AND
retained tombstone => no reopen
AND
same boot_id
```

</div>

اگر implementation نتواند این invariant را حفظ کند، ECRL نباید وارد core شود.

---

## 5. معیار شکست و ابطال فرضیه

ECRL با «کارکردن معمولی» تأیید نمی‌شود. تست‌های زیر **falsification gates** هستند.

| شناسه | fault injection | انتظار | شرط رد فرضیه |
|---|---|---|---|
| **ECRL-F01 Zombie Carrier** | بعد از commit `e+1` روی Carrier `e` DATA/ACK/WINDOW/FIN معتبر و دیررس تزریق شود | هیچ state کاربردی یا target byte تغییر نکند | حتی یک frame قدیمی state جدید را تغییر دهد |
| **ECRL-F02 Dual Candidate** | دو Carrier هم‌زمان برای `e+1` prepare شوند | فقط یکی commit کند؛ دیگری هیچ application DATA مؤثر نداشته باشد | دو owner یا دو مسیر replay مؤثر ایجاد شود |
| **ECRL-F03 Lost ACK** | peer تا `A` accepted کند ولی ACK `A` drop شود؛ local فقط `K<A` بداند | replay frontier دقیقاً `A` باشد | replay plan از کمتر یا بیشتر از `A` شروع شود |
| **ECRL-F04 Accepted-not-delivered** | هنگام قطع `D<A` باشد | `[D,A)` فقط از ring و `[A,S)` فقط از replay تحویل شود | overlap، gap یا duplicate target byte |
| **ECRL-F05 Tombstone Resurrection** | Flow بسته شود، سپس snapshot stale آن را active نشان دهد | target دوباره dial نشود و Flow reopen نشود | هر reopen یا DATA پس از tombstone |
| **ECRL-F06 Lost FIN_ACK** | FIN_ACK drop و سپس resume | terminal state بدون tail loss یا duplicate side effect reconcile شود | tail حذف، FIN side effect تکراری یا premature close |
| **ECRL-F07 Peer Restart** | peer با boot_id جدید reconnect کند | resume رد شود | resume با state قبلی ادامه پیدا کند |
| **ECRL-F08 State Mismatch** | `A>S` یا `A<K` تزریق شود | `STATE_MISMATCH` و عدم commit | snapshot متناقض commit شود |
| **ECRL-F09 Property/Fuzz** | drop/reorder/duplicate/control-race در هزاران sequence | invariantها همیشه برقرار بمانند | هر invariant violation |
| **ECRL-F10 Exact Byte Stream** | جریان تصادفی با قطع در offsetهای مختلف و resumeهای تکراری | hash و byte-count مقصد دقیقاً با source برابر باشد | **حتی یک بایت duplicate یا missing به target برسد** |

### معیار ابطال نهایی

> اگر پس از قطع و resume، در هر اجرای معتبر fault-injection حتی **یک بایت duplicate یا missing** به target برسد، یا Carrier قدیمی بعد از commit state جدید را تغییر دهد، **فرضیه‌ی ECRL در شکل فعلی رد می‌شود**.

در آن حالت حق نداریم صرفاً timeout یا buffer را تغییر دهیم و طراحی را «موفق» بنامیم؛ باید invariant یا معماری handoff بازطراحی شود.

---

## 5A. تطبیق یک‌به‌یک F01 تا F10 با harness

هیچ معیار F حذف نشده است.

| معیار | تست deterministic | پوشش دقیق |
|---|---|---|
| F01 | `TestECRLF01ZombieCarrier` | Carrier قدیمی بعد از commit هیچ اثر کاربردی ندارد |
| F02 | `TestECRLF02DualCandidate` | فقط یک candidate برای epoch بعدی مالک مؤثر می‌شود |
| F03 | `TestECRLF03LostACK` | replay frontier از A است، نه K |
| F04 | `TestECRLF04AcceptedNotDeliveredPartition` + `TestECRLF04DltKReplayReleaseSafety` | partition بدون gap/overlap و ایمنی شکاف `[D,K)` |
| F05 | `TestECRLF05TombstoneResurrection` | OPEN روی tombstone رد می‌شود |
| F06 | `TestECRLF06LostFIN` + `TestECRLF06LostFINACK` | Lost FIN و Lost FIN_ACK مستقل آزموده می‌شوند |
| F07 | `TestECRLF07PeerRestart` | تغییر boot_id resume را رد می‌کند |
| F08 | `TestECRLF08InconsistentSnapshot` | snapshot متناقض از جمله A>S رد می‌شود |
| F09 | `TestECRLF09DeterministicPropertySweep` | روابط F/K/K_release/D/A/S/C روی فضای کوچک exhaustively sweep می‌شوند |
| F10 | `TestECRLF10ExactByteStream` | byte-count/hash exact؛ هیچ duplicate/missing مجاز نیست |

**چرا F06 دو تست دارد؟** سناریویی حذف یا ادغام مفهومی نشده است. Lost FIN و Lost FIN_ACK دو جهت متفاوتِ از دست‌رفتن control در invariant terminal monotonicity (I6) هستند؛ به همین دلیل هر دو زیر شناسه F06 باقی مانده‌اند ولی با دو تست مستقل اجرا می‌شوند.

## 5B. Mutation gate — اجرای واقعی mutant داخل reference model

Mutationها با متغیر تستی `ECRL_MUTANT` **داخل همان reference model** فعال می‌شوند. Workflow سپس **خود تست F اصلی** را با mutant اجرا می‌کند و فقط وقتی mutant کشته‌شده محسوب می‌شود که همان تست واقعاً با `--- FAIL:` خارج شود.

| mutant | تست F که باید در حضور mutant FAIL شود | اثر عمدی |
|---|---|---|
| `f01_accept_old_epoch` | F01 / `TestECRLF01ZombieCarrier` | authorize کردن Carrier قدیمی |
| `f02_accept_both_candidates` | F02 / `TestECRLF02DualCandidate` | پذیرش هم‌زمان دو candidate |
| `f03_replay_from_k` | F03 / `TestECRLF03LostACK` | replay frontier = K به‌جای A |
| `f04_overlap_ring_replay` | F04 / `TestECRLF04AcceptedNotDeliveredPartition` | شروع replay از D و overlap با Ring |
| `f05_accept_tombstone_open` | F05 / `TestECRLF05TombstoneResurrection` | OPEN روی tombstone |
| `f06_lost_fin_never_closes` | F06 / `TestECRLF06LostFIN` | FIN گم‌شده هرگز replay/close نشود |
| `f06_apply_fin_twice` | F06 / `TestECRLF06DuplicateFINIdempotence` | side effect FIN دو بار اعمال شود |
| `f07_resume_after_boot_change` | F07 / `TestECRLF07PeerRestart` | resume بعد از تغییر Boot ID |
| `f08_accept_a_gt_s` | F08 / `TestECRLF08InconsistentSnapshot` | حذف reject برای A>S |
| `f09_skip_k_le_a` | F09 / `TestECRLF09DeterministicPropertySweep` | حذف بررسی K<=A |
| `f10_replay_from_a_minus_1` | F10 / `TestECRLF10ExactByteStream` | off-by-one replay از A-1؛ duplicate |
| `f10_replay_from_a_plus_1` | F10 / `TestECRLF10ExactByteStream` | off-by-one replay از A+1؛ missing byte |

**نکته درباره mutant قدیمی release-to-K:** بعد از پاسخ بند [D,K)، «release تا K در current in-memory mode» دیگر mutant معتبر محسوب نمی‌شود، چون ring زنده نسخه‌ی `[D,K)` را نگه می‌دارد. آن رفتار فقط در durable-snapshot mode آینده، که receiver می‌تواند ring را در restart از دست بدهد ولی resume ادامه یابد، خطرناک است. بنابراین آن mutant از gate فعلی حذف شده تا تست، فرض غلط را enforce نکند.

### مدرک kill شدن mutantها — run 36345328811

خطوط زیر **عیناً از لاگ GitHub Actions run 36345328811** نقل شده‌اند:

<div dir="ltr" align="left">

```text
2026-09-27T19:43:19.2146603Z --- FAIL: TestECRLF01ZombieCarrier (0.00s)
2026-09-27T19:43:19.2160451Z MUTANT_KILLED f01_accept_old_epoch by TestECRLF01ZombieCarrier
2026-09-27T19:43:19.3654854Z --- FAIL: TestECRLF02DualCandidate (0.00s)
2026-09-27T19:43:19.3669840Z MUTANT_KILLED f02_accept_both_candidates by TestECRLF02DualCandidate
2026-09-27T19:43:19.5190547Z --- FAIL: TestECRLF03LostACK (0.00s)
2026-09-27T19:43:19.5205342Z MUTANT_KILLED f03_replay_from_k by TestECRLF03LostACK
2026-09-27T19:43:19.6649169Z --- FAIL: TestECRLF04AcceptedNotDeliveredPartition (0.00s)
2026-09-27T19:43:19.6663888Z MUTANT_KILLED f04_overlap_ring_replay by TestECRLF04AcceptedNotDeliveredPartition
2026-09-27T19:43:19.8092160Z --- FAIL: TestECRLF05TombstoneResurrection (0.00s)
2026-09-27T19:43:19.8105904Z MUTANT_KILLED f05_accept_tombstone_open by TestECRLF05TombstoneResurrection
2026-09-27T19:43:19.9749012Z --- FAIL: TestECRLF06LostFIN (0.00s)
2026-09-27T19:43:19.9762847Z MUTANT_KILLED f06_lost_fin_never_closes by TestECRLF06LostFIN
2026-09-27T19:43:20.1184848Z --- FAIL: TestECRLF06DuplicateFINIdempotence (0.00s)
2026-09-27T19:43:20.1199356Z MUTANT_KILLED f06_apply_fin_twice by TestECRLF06DuplicateFINIdempotence
2026-09-27T19:43:20.2655614Z --- FAIL: TestECRLF07PeerRestart (0.00s)
2026-09-27T19:43:20.2672270Z MUTANT_KILLED f07_resume_after_boot_change by TestECRLF07PeerRestart
2026-09-27T19:43:20.4135801Z --- FAIL: TestECRLF08InconsistentSnapshot (0.00s)
2026-09-27T19:43:20.4149022Z MUTANT_KILLED f08_accept_a_gt_s by TestECRLF08InconsistentSnapshot
2026-09-27T19:43:20.5636618Z --- FAIL: TestECRLF09DeterministicPropertySweep (0.00s)
2026-09-27T19:43:20.5649957Z MUTANT_KILLED f09_skip_k_le_a by TestECRLF09DeterministicPropertySweep
2026-09-27T19:43:20.7136727Z --- FAIL: TestECRLF10ExactByteStream (0.00s)
2026-09-27T19:43:20.7150276Z MUTANT_KILLED f10_replay_from_a_minus_1 by TestECRLF10ExactByteStream
2026-09-27T19:43:20.8597287Z --- FAIL: TestECRLF10ExactByteStream (0.00s)
2026-09-27T19:43:20.8611174Z MUTANT_KILLED f10_replay_from_a_plus_1 by TestECRLF10ExactByteStream
```

</div>

## 5C. Prior art برای ACK/settlement چندمرحله‌ای

| سیستم | مکانیزم موجود | شباهت با ایده‌ی K/K_release | تفاوت BAFT | نتیجه |
|---|---|---|---|---|
| **MQTT QoS 2** | چرخه PUBLISH → PUBREC → PUBREL → PUBCOMP؛ receiver پس از PUBREC ownership پیام را می‌پذیرد و duplicate PUBLISH نباید دوباره به onward recipient تحویل شود | جداسازی «دریافت/ownership» از «تکمیل exchange» | message/Packet-Identifier محور است، نه cumulative byte stream با TWRL و target-socket watermark | ACK چندمرحله‌ای به‌خودی‌خود جدید نیست |
| **AMQP 1.0 settlement** | delivery می‌تواند unsettled بماند؛ receiver terminal outcome می‌دهد؛ settlement دوطرفه و link recovery/unsettled state وجود دارد؛ AMQP حتی `received(section,offset)` برای resume partial message دارد | بسیار نزدیک به جداسازی receive state از terminal processing/settlement و recovery | BAFT Flow یک byte stream پیوسته روی target socket است و state را با epoch fencing، TWRL `D/A/C` و replay cap ترکیب می‌کند | prior art قوی؛ ادعای novelty برای K_release به‌تنهایی قابل دفاع نیست |
| **MPTCP DATA_ACK** | ACK تجمعی در data-sequence space کل connection، مستقل از ACKهای subflow | شبیه K/A: acknowledgement در یک sequence space بالاتر از transport path | DATA_ACK موفقیت دریافت data-level را نشان می‌دهد، نه تحویل به application target؛ معادل K_release نیست | cumulative higher-layer ACK prior art است |
| **Kafka producer acks/idempotence** | `acks=1/all` سطح durability در broker/replica را تعیین می‌کند؛ idempotent producer از duplicate write در log جلوگیری می‌کند | نشان می‌دهد acknowledgement می‌تواند stage/durability semantics متفاوت داشته باشد | Kafka record/log/replication محور است، نه per-Flow target-delivery byte watermark | چندسطحی بودن «acknowledged» جدید نیست |

منابع اولیه و بخش دقیق:

- **MQTT 5.0 OASIS Standard، §4.3.3 — QoS 2 delivery protocol**؛ ownership در PUBREC و جلوگیری از duplicate onward delivery در MQTT-4.3.3-8 تا MQTT-4.3.3-10: https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html
- **AMQP 1.0 OASIS، Part 2 §2.6.13 — Resuming Deliveries** و **Part 3 §3.4.6 — Resuming Deliveries Using Delivery States**: https://docs.oasis-open.org/amqp/core/v1.0/os/amqp-core-complete-v1.0-os.pdf
- **MPTCP RFC 8684، §3.3.2 — Data Acknowledgments**: https://www.rfc-editor.org/rfc/rfc8684.html#section-3.3.2
- **Apache Kafka مستند رسمی، §4.6 — Message Delivery Semantics**، همراه با Producer Configs → `acks` و `enable.idempotence`: https://kafka.apache.org/090/documentation/#semantics و https://kafka.apache.org/40/configuration/producer-configs/

### نتیجه‌ی novelty پس از این review

- **K_release / ACK دوسطحی به‌خودی‌خود نوآوری نیست.**
- در مدل فعلی in-memory حتی requirement فعال ECRL هم نیست.
- اگر BAFT تفاوت پژوهشی قابل‌بررسی داشته باشد، در **ترکیب** epoch-fenced Carrier handoff + TWRL `D/A/C` + exact byte partition + tombstone/FIN + bounded replay/PADL coupling است، نه در داشتن دو ACK.
- این تفاوت هنوز فقط **فرضیه پژوهشی** است.
- **نتیجه مهندسی پشتیبانی‌شده:** برای ECRL engine هنوز ادعا نمی‌شود.
- **Patentability:** همچنان بررسی‌نشده و نیازمند patent/literature search تخصصی است.

## 5D. Refinement gate — Stage D / مرحله ۱

interface مشترک reference model و engine واقعی:

<div dir="ltr" align="left">

```go
type ReconcileCommitEngine interface {
    Prepare(next uint64, candidateID string) error
    Reconcile(candidateID string, local, peer Snapshot, expectedPeerBootID string) (Plan, error)
    Commit(next uint64, candidateID string, plan Plan) error
    Abort(next uint64, candidateID string)
    Authorize(epoch uint64, carrierID string) bool
    CurrentEpoch() uint64
    Owner() string
}
```

</div>

در مرحله ۱ فقط F01/F02/F03/F07/F08/F09 مستقیماً روی engine واقعی قابل اعمال‌اند. F04/F10 متعلق به replay execution و F05/F06 متعلق به FIN/tombstone execution هستند و تا مراحل بعد **عمداً engine-PASS اعلام نمی‌شوند**.

`TestStage1DifferentialReferenceVsEngine` روی traceهای deterministic، نتیجه Prepare/Reconcile/Commit/Authorize مدل و engine را مقایسه می‌کند. هر اختلاف gate را قرمز می‌کند.

در current in-memory mode، `FlowPlan.LocalReleaseThrough = K` و `PeerReleaseThrough = K_peer` است. `durable_snapshot` در `EngineOptions{}` به‌صورت پیش‌فرض خاموش است؛ `RReplay` بدون فعال‌بودن آن config نامعتبر است. `K_release`/replay cap فقط در policy آزمایشی durable آینده فعال می‌شوند و هنوز engine replay را تغییر نمی‌دهند.

## 6. تصمیم فعلی درباره novelty

### چیزی که **نوآوری نیست**

- monotonic epoch؛
- stale-owner rejection؛
- session resumption؛
- connection migration؛
- replay protection؛
- sequence/offset-based deduplication؛
- tombstone به‌عنوان مفهوم عمومی.

### چیزی که **فرضیه‌ی تفاوت ECRL** است

ترکیب زیر، آن هم فقط اگر در تست‌ها invariantها را حفظ کند:

1. Carrier ownership با epoch committed؛
2. correlation بین sender `K/S` و peer `D/A/C`؛
3. تقسیم بدون هم‌پوشانی handoff به:
   - `Ring=[D,A)`
   - `Replay=[A,S)`
4. tombstone monotonic در همان resume decision؛
5. FIN/half-close correlation؛
6. رد resume در تغییر Boot ID؛
7. اجرای همه‌ی این‌ها در application relay روی Carrier H2/TCP جدید، بدون custom crypto.

**این هنوز اثبات novelty نیست.** این فقط Gap/Hypothesis دقیق‌تری است که ارزش prior-art search تخصصی‌تر و آزمایش دارد.

---

## 7. منابع اصلی این review

- Diego Ongaro, John Ousterhout — *In Search of an Understandable Consensus Algorithm (Raft)*: https://raft.github.io/raft.pdf
- RFC 9000 — *QUIC: A UDP-Based Multiplexed and Secure Transport*: https://www.rfc-editor.org/rfc/rfc9000
- RFC 8446 — *The Transport Layer Security (TLS) Protocol Version 1.3*: https://www.rfc-editor.org/rfc/rfc8446

## 8. گیت ادامه کار

پس از ثبت این سند، ادامه‌ی پیاده‌سازی ECRL فقط باید به ترتیب زیر انجام شود:

1. formal/property tests برای invariantها؛
2. fault-injectionهای F01 تا F09؛
3. carrier-replacement integration؛
4. F10 exact-byte-stream؛
5. سپس و فقط سپس ارتقای وضعیت از «فرضیه پژوهشی» به «نتیجه مهندسی پشتیبانی‌شده».

</div>
