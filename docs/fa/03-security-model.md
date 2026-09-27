<div dir="rtl" align="right" lang="fa">

# 03 — مدل امنیت، هویت و مرز اعتماد

## فرض اعتماد

BAFT برای دو Node متعلق به یک اپراتور یا یک دامنه مدیریتی طراحی شده است. امنیت آن بر «مخفی بودن مسیر HTTP» تکیه نمی‌کند؛ مسیر Carrier عمومی و ثابت است و راز محسوب نمی‌شود.

## PKI

پیشنهاد طراحی:

- یک CA خصوصی برای نصب یا جفت Nodeها؛
- نگهداری کلید CA به‌صورت آفلاین؛
- کلید خصوصی جدا برای هر Node؛
- گواهی EX با `serverAuth` و SAN مطابق `server_name`؛
- گواهی IR با `clientAuth` و یک URI SAN مانند `urn:baft:node:ir-01`.

Runtime فعلی chain، hostname، validity و EKU را بررسی می‌کند و هویت peer را از URI SAN می‌گیرد.

## اعتماد با authorization یکی نیست

وجود گواهی صادرشده توسط CA به معنی مجوز استفاده از هر Route نیست.

سه لایه مستقل وجود دارد:

1. **TLS trust:** آیا chain و نقش گواهی معتبر است؟
2. **Peer allowlist:** آیا این identity اجازه اتصال به Node را دارد؟
3. **Route allowlist:** آیا همان identity اجازه Route موردنظر را دارد؟

این تفکیک عمداً حفظ می‌شود.

## HELLO هویت‌ساز نیست

`node_id` داخل HELLO برای تطبیق state است. اگر با هویت گواهی تطبیق نکند اتصال پذیرفته نمی‌شود. یک peer نمی‌تواند صرفاً با نوشتن نام Node دیگر در JSON هویت عوض کند.

## Active revocation

Revocation فقط در handshake کافی نیست. Runtime دارای deny/revocation بر اساس:

- peer identity؛
- serial گواهی؛
- SHA-256 fingerprint گواهی

است. watcherهای Carrier فعال ثبت می‌شوند و revocation جدید context Carrier مربوط را cancel می‌کند.

## امنیت Route

OPEN فقط `route_id` و `open_nonce` حمل می‌کند. EX مقصد را از جدول محلی ثابت resolve می‌کند. ورودی peer نمی‌تواند host، port، shell command یا path عملیاتی دلخواه تعیین کند.

## parser

- header ثابت 24 بایت؛
- سقف payload parser برابر 65536 بایت؛
- length پیش از allocation بررسی می‌شود؛
- flags/reserved ناشناخته رد می‌شوند؛
- overflow در offset + payload length خطاست؛
- type ناشناخته protocol error است؛
- مرز read یا HTTP/2 DATA مرز فریم BAFT فرض نمی‌شود.

## log و داده حساس

در baseline:

- `logging.payload` باید false باشد؛
- secret واقعی نباید در fixture یا example commit شود؛
- key خصوصی باید permission محدود داشته باشد؛
- متن خطای OS یا مسیر فایل نباید به peer روی wire نشت کند؛
- support bundle آینده نباید payload، secret یا آدرس کاربر را شامل شود.

## ممنوعیت downgrade

این رفتارها در مسیر اصلی ممنوع‌اند:

- `InsecureSkipVerify`;
- plaintext fallback؛
- HTTP/1 fallback خودکار؛
- redirect خودکار Carrier؛
- proxy محیطی برای Carrier؛
- مقصد arbitrary؛
- queue یا retry نامحدود.

## چیزهایی که امنیت BAFT تضمین نمی‌کند

mTLS بین دو Node کاربران یک listener عمومی IR را احراز هویت نمی‌کند. اگر اپراتور listener را از loopback عمومی‌تر کند، باید ACL یا احراز هویت مناسب لایه سرویس را جداگانه اعمال کند.

</div>
