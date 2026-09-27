# 04 — پروتکل BAFT/1 و state machine

## هدف

BAFT/1 یک framing protocol باینری است که **فقط داخل Carrier رمزگذاری‌شده و احرازشده** استفاده می‌شود. parser آن نباید مستقیماً روی یک socket خام عمومی قرار گیرد. امنیت محرمانگی و integrity را TLS فراهم می‌کند؛ BAFT checksum امنیتی اختصاصی اضافه نمی‌کند.

## قالب فریم

تمام اعداد باینری big-endian هستند.

| Offset | اندازه | فیلد | توضیح |
|---:|---:|---|---|
| 0 | 4 | `frame_len` | طول کامل فریم، شامل header و payload |
| 4 | 1 | `type` | نوع فریم |
| 5 | 1 | `flags` | در نسخه 1 باید صفر باشد |
| 6 | 2 | `reserved` | در نسخه 1 باید صفر باشد |
| 8 | 8 | `stream_id` | صفر برای کنترل Session؛ غیرصفر برای Flow |
| 16 | 8 | `offset` | معنای آن به نوع فریم بستگی دارد |
| 24 | متغیر | `payload` | حداکثر 65536 بایت |

قواعد پایه:

- header دقیقاً 24 بایت است؛
- `frame_len` بین 24 و 65560 است؛
- سقف parser برای payload برابر 65536 بایت است؛
- DATA baseline معمولاً در chunkهای 32768 بایتی تولید می‌شود؛
- طول و type قبل از allocation payload بررسی می‌شوند؛
- overflow در `offset + payload_len` خطاست؛
- یک فریم می‌تواند میان چند `Read` شکسته شود؛
- یک `Read` نیز می‌تواند بخشی از چند لایه transport را برگرداند، پس مرز socket/HTTP2 مرز BAFT نیست.

## انواع فریم

| Hex | نام | stream_id | offset | کاربرد |
|---:|---|---|---|---|
| `01` | HELLO | 0 | 0 | معرفی نسخه و state Session |
| `02` | HELLO_ACK | 0 | 0 | انتخاب نسخه و limits |
| `10` | OPEN | Flow | 0 | درخواست Route با nonce |
| `11` | OPEN_OK | همان Flow | 0 | پذیرش Flow |
| `12` | OPEN_ERR | همان Flow | 0 | خطای ثابت و استاندارد |
| `20` | DATA | Flow | offset شروع | بایت کاربردی |
| `21` | ACK | Flow | next_expected | پذیرش پیوسته |
| `22` | WINDOW | Flow | max_offset | سقف مطلق ارسال |
| `23` | FIN | Flow | final_offset | پایان یک جهت |
| `24` | FIN_ACK | Flow | final_offset | تأیید پایان جهت |
| `25` | RESET | Flow | 0 | توقف Flow با code ثابت |
| `30` | RESUME_STATE | 0 | 0 | برای Stage D |
| `31` | RESUME_DONE | 0 | 0 | برای Stage D |
| `32` | READY | 0 | 0 | آماده‌شدن Session |
| `40` | PING | 0 | 0 | heartbeat با nonce |
| `41` | PONG | 0 | 0 | پاسخ nonce |
| `42` | GOAWAY | 0 | 0 | drain/retry در طراحی کامل |
| `50` | PROFILE_PROPOSE | 0 | 0 | transition آینده Profile |
| `51` | PROFILE_ACCEPT | 0 | 0 | پذیرش transition |
| `52` | PROFILE_COMMIT | 0 | 0 | commit transition |
| `60` | PADDING | 0 | 0 | فقط با budget محدود در پژوهش |

وجود type ناشناخته، flags/reserved غیرصفر یا stream placement نادرست protocol error است.

## stream_id

در طراحی مرجع:

- IR شناسه‌های فرد ایجاد می‌کند: 1، 3، 5، ...
- EX شناسه‌های زوج ایجاد می‌کند: 2، 4، 6، ...
- شناسه در یک Session تکرار نمی‌شود.
- نزدیک سرریز باید Session جدید برای Flowهای جدید ایجاد و Session قدیمی drain شود.

## handshake Session جدید

### HELLO

اطلاعاتی مانند این‌ها را حمل می‌کند:

- حداقل و حداکثر نسخه protocol؛
- `node_id`؛
- `boot_id`؛
- `session_id`؛
- `shard_id`؛
- `epoch`؛
- mode؛
- profile و version؛
- config revision؛
- capabilityها.

`node_id` باید با هویت احرازشده transport سازگار باشد.

### HELLO_ACK

نسخه منتخب، Session/Epoch، Boot ID سمت مقابل، Profile پذیرفته‌شده و limits مذاکره‌شده را برمی‌گرداند.

### READY

فقط وقتی دو طرف READY شده‌اند application frame مجاز است.

## OPEN

Payload شامل مقصد شبکه نیست؛ شامل Route ID و `open_nonce` است.

`open_nonce` برای idempotency مهم است. OPEN تکراری با همان stream و همان identity نباید target دوم را دوباره dial کند. OPEN تکراری متناقض protocol error است.

## DATA و offset

هر جهت Flow فضای offset مستقل دارد. DATA با offset شروع payload فرستاده می‌شود.

گیرنده باید این حالت‌ها را تشخیص دهد:

- **offset == rx_next:** داده جدید پیوسته؛
- **offset < rx_next و کل payload قبلاً دیده شده:** duplicate؛ ACK شود ولی دوباره به target نوشته نشود؛
- **overlap جزئی:** فقط suffix جدید نوشته شود؛
- **offset > rx_next:** gap؛ baseline آن را نمی‌پذیرد؛
- **end > rx_max:** FLOW_CONTROL_ERROR.

## ACK

ACK یعنی «تمام بایت‌های کمتر از این offset توسط BAFT گیرنده پذیرفته شده‌اند». ACK به‌معنی این نیست که برنامه نهایی آن داده را پردازش کرده است.

ACKهای قدیمی state را عقب نمی‌برند. ACK بزرگ‌تر از `tx_next` نامعتبر است.

## WINDOW

WINDOW یک credit مطلق است، نه delta. `WINDOW(max_offset)` یعنی sender مجاز است تا قبل از آن offset ارسال کند.

WINDOW نباید به عقب حرکت کند. افزایش WINDOW باید با ظرفیت receive واقعی و reservation حافظه هماهنگ باشد.

## FIN و half-close

`FIN(final_offset)` اعلام می‌کند در آن جهت بعد از `final_offset` داده جدیدی وجود ندارد.

گیرنده فقط وقتی FIN را معتبر می‌داند که `final_offset == rx_next` باشد. سپس در صورت امکان `CloseWrite` را روی socket مقصد اجرا می‌کند و `FIN_ACK` می‌فرستد.

Flow زمانی کاملاً تمام‌شده است که state دو جهت اجازه آزادسازی امن resources را بدهد.

## RESET و خطاها

RESET و OPEN_ERR از codeهای ثابت استفاده می‌کنند تا مسیر فایل، متن OS یا داده حساس روی wire نرود.

نمونه codeهای استاندارد:

- `AUTH_FAILED`
- `VERSION_UNSUPPORTED`
- `INVALID_FRAME`
- `FLOW_CONTROL_ERROR`
- `ROUTE_DENIED`
- `ROUTE_NOT_FOUND`
- `TARGET_UNREACHABLE`
- `TARGET_TIMEOUT`
- `RESOURCE_EXHAUSTED`
- `SESSION_EXPIRED`
- `PEER_RESTARTED`
- `STALE_EPOCH`
- `STATE_MISMATCH`
- `ADMIN_DRAIN`
- `PROTOCOL_ERROR`

## Resume: قرارداد آینده، نه قابلیت فعلی

فریم‌های RESUME_* در جدول protocol رزرو شده‌اند، اما resume کامل متعلق به Stage D است. وجود type در parser به معنی پیاده‌سازی semantics نیست. تا وقتی Stage D کامل نشده، مستندات و CLI نباید اتصال جایگزین را «resume موفق» گزارش کنند.
