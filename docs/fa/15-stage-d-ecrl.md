<div dir="rtl" align="right" lang="fa">

# 15D — ECRL: گیت طراحی، prior-art، مدل تهدید و invariant رسمی

> **وضعیت:** سند طراحی اجباری پیش از ادامه پیاده‌سازی Stage D  
> **نام:** Epoch-Fenced Correlated Resume Ledger (ECRL)  
> **قاعده:** تا زمانی که این سند و تست‌های ابطال آن مبنا قرار نگیرند، هیچ توسعه‌ی جدیدی در wire/session resume انجام نمی‌شود.

## 1. وضعیت ادعا در سه سطح

| سطح | وضعیت فعلی | معنی |
|---|---|---|
| **فرضیه پژوهشی** | **فعال** | ECRL یک فرضیه‌ی طراحی قابل‌ابطال است و باید با invariant و fault-injection سنجیده شود. |
| **نتیجه مهندسی پشتیبانی‌شده** | **هنوز محقق نشده** | وجود prototype یا unit test اولیه به‌تنهایی کافی نیست. تا وقتی carrier replacement واقعی، zombie-carrier، lost-ACK، tombstone و exact-once gate پاس نشوند، این سطح ادعا نمی‌شود. |
| **Patentability / novelty حقوقی** | **بررسی‌نشده** | این prior-art review فقط مهندسی است. هیچ ادعای ثبت‌پذیری یا novelty حقوقی بدون جست‌وجوی patent/literature تخصصی مجاز نیست. |

### نتیجه‌ی صریح prior-art review

**خودِ epoch fencing نوآوری ECRL نیست.**  
**خودِ resume یا anti-replay نیز نوآوری ECRL نیست.**

اگر ECRL تفاوت پژوهشی قابل‌دفاعی داشته باشد، محل آن فقط می‌تواند در **پیوند اتمیک مالکیت Carrier با ledger بایتی هر Flow، watermarkهای TWRL، replay frontier، FIN و tombstone** باشد.

اگر این پیوند نتواند exact-once delivery را در fault-injection ثابت کند، فرضیه ECRL رد می‌شود.

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
- `K` = `tx_acked` که sender محلی دیده است؛
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
K <= A
D <= A <= S <= C
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
