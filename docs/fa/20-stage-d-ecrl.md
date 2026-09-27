<div dir="rtl" align="right" lang="fa">

# 20 — ECRL: دفتر هم‌بسته‌ی بازیابی با حصار Epoch

## وضعیت

**فرضیه پژوهشی Stage D — هنوز وارد data path نشده است.**

## خلأ prior art

راه‌حل‌های شناخته‌شده هرکدام بخشی از مسئله را پوشش می‌دهند:

- TLS 1.3 resumption اتصال رمزنگاری جدید را به context قبلی پیوند می‌دهد، اما state کاربردی Flow را بازسازی نمی‌کند.
- HTTP/2 در قطع اتصال نمی‌تواند streamهای درحال‌پردازش را به‌صورت عمومی و خودکار ادامه دهد.
- QUIC connection migration state transport را با Connection ID و packet/stream state حفظ می‌کند.
- MPTCP یک data-level sequence space مستقل از subflow دارد و داده را روی subflow جایگزین retransmit می‌کند.

BAFT روی H2/TCP به یک state لایه بالاتر نیاز دارد که هم Flow، هم replay، هم FIN و هم مالکیت Carrier را reconcile کند.

## فرضیه ECRL

**Epoch-Fenced Correlated Resume Ledger** دو مفهوم را ترکیب می‌کند:

1. **Epoch Lease:** Carrier جدید ابتدا فقط candidate است. Carrier قدیمی تا commit همچنان مالک است. commit به‌صورت اتمیک epoch را جلو می‌برد و Carrier قدیمی را fence می‌کند.
2. **Correlated Ledger:** state ارسال یک طرف با state دریافت authoritative طرف مقابل correlate می‌شود.

برای هر جهت:

<div dir="ltr" align="left">

```text
sender.tx_acked <= peer.rx_accepted <= sender.tx_next
```

</div>

اگر `peer.rx_accepted` جلوتر از ACK محلی باشد، ACK روی Carrier قبلی گم شده ولی peer واقعاً بایت‌ها را در حافظه bounded پذیرفته است؛ بنابراین sender می‌تواند replay را دقیقاً از همان watermark شروع کند.

اگر peer ادعا کند بیشتر از `tx_next` دریافت کرده، یا state از ACK قبلاً مشاهده‌شده عقب‌تر باشد، resume با `STATE_MISMATCH` رد می‌شود.

## fencing

candidate باید دقیقاً `current_epoch + 1` را درخواست کند. دو Carrier رقیب با candidate ID متفاوت نمی‌توانند هم‌زمان همان epoch را prepare کنند. تا پیش از commit، candidate اجازه application DATA ندارد. بعد از commit، epoch قبلی فوراً غیرمجاز است.

## restart

Resume فقط زمانی معتبر است که `boot_id` peer تغییر نکرده باشد. restart Process به‌معنی ازبین‌رفتن socket/ring/replay in-memory است و با `PEER_RESTARTED` رد می‌شود.

## FIN correlation

FIN/FIN_ACK facts monotonic هستند. snapshot نمی‌تواند ادعا کند FIN دریافت یا ACK شده در حالی که طرف دیگر اصل ارسال متناظر را ثبت نکرده باشد. ACK گم‌شده‌ی FIN می‌تواند از state هم‌بسته بازیابی شود.

## چرا این ترکیب ارزش پژوهشی دارد؟

مسئله BAFT صرفاً transport migration نیست: ACK به «پذیرش در حافظه محدود BAFT» اشاره می‌کند، در حالی که TWRL تحویل به target را جدا نگه می‌دارد. ECRL تلاش می‌کند migration ownership و همین semantic سه‌نشانگری را در یک reconciliation قابل‌ابطال ترکیب کند.

## ریسک و محدودیت

- نسخه فعلی فقط یک مدل خالص و unit-tested است؛ هنوز wire integration ندارد.
- active Flow set فعلاً باید در دو snapshot یکسان باشد؛ tombstone reconciliation مرحله بعد است.
- snapshot هنوز payload replay را حمل نمی‌کند؛ فقط plan امن را محاسبه می‌کند.
- هیچ ادعای patentability یا novelty حقوقی بدون prior-art review اختصاصی وجود ندارد.
- هیچ custom crypto اضافه نمی‌شود؛ authentication همچنان از TLS 1.3 + mTLS می‌آید.

## prior art اولیه

- RFC 8446 — TLS 1.3
- RFC 9113 — HTTP/2
- RFC 9000 — QUIC
- RFC 8684 — Multipath TCP

</div>
