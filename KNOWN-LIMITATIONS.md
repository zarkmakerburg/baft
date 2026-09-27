# محدودیت‌های شناخته‌شده

> English: [KNOWN-LIMITATIONS.en.md](KNOWN-LIMITATIONS.en.md)

- BAFT هنوز research software است و production-ready اعلام نشده است.
- Stage C کامل نیست؛ slow-receiver liveness gate هنوز failure دارد.
- COR-01 پاس‌شده correctness روی runner/local networking است و benchmark اینترنت عمومی نیست.
- allocator، DRR و control scheduling وارد data path شده‌اند، اما soak طولانی و فشار چند Shard هنوز کامل نیست.
- resume، epoch fencing، snapshot، replay روی Carrier جایگزین و tombstone پیاده‌سازی کامل ندارند.
- resume بعد از Process restart وعده داده نشده است.
- endpoint pool/relay production path کامل نشده است.
- H3 و Worker جزو مسیر پایه فعال نیستند.
- benchmark رسمی 60s × 5 و profiler campaign کامل نشده است.
- systemd/installer/config rollback/certificate rotation/support bundle کامل نشده‌اند.
- real Iran↔EX pilot اجرا نشده است.
- هیچ ادعای عمومی درباره سرعت تضمینی، تشخیص‌ناپذیری یا دسترسی در همه شرایط اثبات نشده است.
- listener عمومی IR به‌صورت خودکار کاربر نهایی را authenticate نمی‌کند؛ اگر از loopback خارج شود، امنیت لایه سرویس جداگانه لازم است.
