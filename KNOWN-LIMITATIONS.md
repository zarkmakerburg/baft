<div dir="rtl" align="right" lang="fa">

# محدودیت‌های شناخته‌شده

> English: [KNOWN-LIMITATIONS.en.md](KNOWN-LIMITATIONS.en.md)

- BAFT هنوز research software است و production-ready اعلام نشده است.
- Stage C هنوز رسماً بسته نشده؛ slow-receiver، PADL، shared-budget و metrics سبزند، اما workflow مستقل soak باید PASS شود.
- COR-01 پاس‌شده correctness روی runner/local networking است و benchmark اینترنت عمومی نیست.
- allocator، TWRL، PADL و control scheduling وارد data path شده‌اند؛ shared-budget چند Shard تست شده، اما soak تکرارشونده هنوز گیت باز است.
- resume، epoch fencing، snapshot، replay روی Carrier جایگزین و tombstone پیاده‌سازی کامل ندارند.
- resume بعد از Process restart وعده داده نشده است.
- endpoint pool/relay production path کامل نشده است.
- H3 و Worker جزو مسیر پایه فعال نیستند.
- benchmark رسمی 60s × 5 و profiler campaign کامل نشده است.
- systemd/installer/config rollback/certificate rotation/support bundle کامل نشده‌اند.
- real Iran↔EX pilot اجرا نشده است.
- هیچ ادعای عمومی درباره سرعت تضمینی، تشخیص‌ناپذیری یا دسترسی در همه شرایط اثبات نشده است.
- listener عمومی IR به‌صورت خودکار کاربر نهایی را authenticate نمی‌کند؛ اگر از loopback خارج شود، امنیت لایه سرویس جداگانه لازم است.

</div>


## محدوده Recovery در Step 5.7
اتصال ECRL به Runtime در این مرحله فقط برای **تعویض Carrier در همان process و همان Session زنده** است. snapshot بازیابی بین restart پردازه یا reboot ماشین پایدار نمی‌شود و هیچ ادعایی برای process-restart resume وجود ندارد. تغییر BootID سمت peer هنگام recovery به‌صورت fail-closed رد می‌شود. Subscription Engine خارج از scope است و Record Shaping / Morphing / Stealth در Step 5.7 تغییری نکرده‌اند.
