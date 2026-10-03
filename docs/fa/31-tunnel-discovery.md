<div dir="rtl" align="right" lang="fa">

# 31 — کشف تونل‌های موجود (فقط گزارش)

HQ A2. BCC می‌تواند از یک نود بپرسد چه unit، config و ownership markerی از BAFT دارد و گزارش کند. **کشف یعنی adopt نه.** هیچ adopt، overwrite، حذف، restart، migration، ساخت marker یا تبدیل config در این مسیر نیست؛ همهٔ وضعیت‌های زیر فقط اطلاعاتند. adopt در حالت hold می‌ماند و وقتی بیاید باید عملیات جدا، صریح، قابل پیش‌نمایش و دارای تأیید باشد.

<div dir="ltr" align="left">

```
BCC -> signed read-only job (tunnel_discover, no parameters) -> agent -> inspect -> report -> BCC classifies and stores
```

</div>

## agent چه می‌خواند

فقط پوشهٔ unitهای systemd نود و فایل‌هایی که unitها به آن‌ها اشاره می‌کنند، فقط‌خواندنی:

- فایل‌های unit با نام `baft*.service` (نه `baft-agent*` که خود agent است و نه چیز دیگر)، حداکثر ۱۶ تا، فقط فایل معمولی (symlink یا فایل بزرگ‌تر از ۶۴ KiB به‌عنوان مشکل گزارش می‌شود و دنبال نمی‌شود)؛
- برای unitی که `ExecStart` آن دقیقاً `baft run --file <مسیر مطلق .yaml/.yml/.json>` است: همان config (فایل معمولی، حداکثر ۱ MiB، با loader سخت‌گیر خود BAFT) و `baft.managed.json` کنار آن (حداکثر ۱۶ KiB)؛
- `systemctl is-active` برای هر unit و هیچ فرمان دیگری.

واقعیت می‌دهد نه محتوا: SHA-256 مربوط به unit و config، وضعیت سرویس، header `# baft-tunnel:` در unit، نقش، listen، peer، route و target کانفیگ و فیلدهای خود marker. محتوای فایل، خط‌های `Environment=`، کلیدها و رازها هرگز نود را ترک نمی‌کنند. گزارش محدود است (۴۸ KiB، مقدارهای بلند بریده می‌شوند، تعداد unit سقف دارد)، timestamp ندارد و برای نود بدون تغییر بایت‌به‌بایت یکسان است.

## وضعیت‌ها (BCC از روی موجودی خودش طبقه‌بندی می‌کند)

| وضعیت | معنی |
|---|---|
| `MANAGED` | marker unit اصلی مال BAFT است، تونل **active** همین نود را با نقش درست نام می‌برد و فایل‌های زنده برابر digestهایی است که **خود BCC تأیید کرده** (همان قواعد تشخیص drift) |
| `DRIFTED` | مالک دو طرف یکی است ولی فایل‌ها، سرویس یا واقعیت‌های route بعداً عوض شده |
| `MISSING` | BCC تونل active روی نود ثبت کرده ولی فایل unit سرویس خود نود نیست |
| `DISCOVERED_UNMANAGED` | unit BAFT با config معتبر و **بدون** ownership marker: BAFT آن را نساخته یا ادعا نکرده |
| `OWNERSHIP_CONFLICT` | ادعاهای متناقض: markerی که دیگری ادعا کرده؛ marker که تونلی را نام می‌برد که BCC نمی‌شناسد، تونل نودهای دیگر، یا تونلی که BCC آن را superseded یا rolled back ثبت کرده؛ نقشی که با BCC نمی‌خواند؛ header unit که تونل دیگری جز marker را نام می‌برد؛ unitی که می‌گوید BAFT-managed است ولی marker ندارد؛ BCC انتظار دارد unit اصلی مال BAFT باشد ولی فایل‌ها marker ندارند؛ unit دومی که marker را شریک است |
| `UNKNOWN` | فهمیده نشد، خوانده نشد، قابل اثبات نیست، یا BCC همین حالا در حال تغییر تونل است (تا آرام نشود نتیجه‌ای گرفته نمی‌شود)؛ unit اصلی‌ای که هست ولی خوانده نمی‌شود UNKNOWN است، هرگز MISSING نیست؛ نبودن digest تأییدشده در BCC یعنی هرگز MANAGED |

marker فقط یک ادعاست؛ `MANAGED` یعنی سوابق خود BCC هم موافق است. در هر وضعیتی کشف هیچ کاری روی artifact نمی‌کند: `DISCOVERED_UNMANAGED`، `OWNERSHIP_CONFLICT` و `UNKNOWN` هرگز لمس نمی‌شوند و uninstall (وقتی بیاید) فقط اجازه دارد چیزی را حذف کند که BAFT-owned بودنش اثبات شده.

## کجا دیده می‌شود

- `POST /api/discovery?node=<id>` یا `?all=1` (ادمین) job فقط‌خواندنی را صف می‌کند؛ این فراخوانی مسیر یا ورودی دیگری نمی‌گیرد، هر نود در هر لحظه یک کشف دارد و نودهای revoke‌شده پرسیده نمی‌شوند. `GET /api/discovery[?node=<id>]` (ادمین) موجودی ذخیره‌شده را برای هر نود می‌دهد: instanceها با وضعیت و دلیل، شمارش خلاصه، مشکل‌ها.
- کارت **Existing tunnels (discovery)** در داشبورد.
- audit: `discovery.start` برای هر درخواست و `discovery.completed` برای هر کشف تمام‌شده، با خلاصه، وضعیت هر unit، اینکه نسبت به کشف قبلی عوض شده یا نه و هر مشکل (job ناموفق، گزارش ناخوانا یا نودی که تا ۱۰ دقیقه جواب ندهد مشکل ثبت‌شده است، نه یافته).
- موجودی بخشی از state در BCC (migration شمارهٔ ۴، جدول `node_discovery`) و پشتیبان‌های آن است.

## محدودیت‌ها

فقط unitهای BAFT با نام `baft*.service` در پوشهٔ unit نود پیدا می‌شوند. transportی که به شکل دیگری اجرا شده (container، اسکریپت، unit با نام دیگر) کشف نمی‌شود و این نبودن دربارهٔ آن چیزی نمی‌گوید. ownership marker کنار config است، پس دو unit که پوشهٔ config را شریک‌اند marker را شریک‌اند؛ دومی conflict گزارش می‌شود.

</div>
