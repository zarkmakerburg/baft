<div dir="rtl" align="right" lang="fa">

# 11 — فرهنگ واژگان

| اصطلاح | تعریف دقیق در BAFT |
|---|---|
| **Node** | یک Process/عامل BAFT با نقش dialer یا listener |
| **IR** | نام نقش/سمت آغازکننده Carrier در معماری پایه؛ لزوماً تعریف جغرافیایی در خود protocol نیست |
| **EX** | سمت پذیرنده Carrier در baseline |
| **Carrier** | byte stream دوطرفه و احرازشده که BAFT frameها را حمل می‌کند |
| **Shard** | واحد Carrier مستقل با Session و scheduling خودش |
| **Session** | state حافظه‌ای مربوط به یک Shard، شامل شناسه‌ها و Flowها |
| **Flow** | یک اتصال TCP کاربردی دوطرفه که stream_id دارد |
| **Route** | نام ازپیش‌تعریف‌شده برای listener یا target ثابت و مجاز |
| **stream_id** | شناسه عددی Flow داخل Session |
| **offset** | موقعیت بایت در یک جهت Flow یا مقدار state طبق نوع frame |
| **ACK** | `next_expected` گیرنده؛ تأیید پذیرش پیوسته در BAFT |
| **target written** | میزان داده‌ای که واقعاً به socket مقصد نوشته شده؛ با ACK یکی نیست |
| **WINDOW** | اعلان `max_offset` مجاز؛ credit مطلق |
| **Credit** | اجازه ارسال تا offset مشخص |
| **Replay** | داده تأییدنشده‌ای که برای semantics بازیابی نگه داشته می‌شود |
| **Half-close** | پایان یک جهت TCP بدون بستن فوری جهت مقابل |
| **FIN** | اعلام final_offset یک جهت |
| **FIN_ACK** | تأیید final_offset همان جهت |
| **RESET** | پایان فوری Flow با code ثابت |
| **Boot ID** | شناسه اجرای Process/Node برای تشخیص restart |
| **Epoch** | نسل Carrier یک Session برای fencing |
| **Snapshot** | تصویر state لازم برای resume آینده |
| **Tombstone** | state کوتاه‌عمر برای جلوگیری از احیای Flow پایان‌یافته در resume |
| **Profile** | مجموعه versioned از تنظیمات scheduling/resources |
| **Goodput** | بایت مفید تحویل‌شده در زمان، بدون احتساب overhead/retransmission |
| **Backpressure** | انتقال فشار ظرفیت از receiver کند به sender/source به‌جای queue نامحدود |
| **DRR** | Deficit Round Robin؛ scheduler fairness برحسب byte budget |
| **mTLS** | TLS با احراز گواهی دو طرف |
| **URI SAN** | URI داخل Subject Alternative Name گواهی که برای identity Node استفاده می‌شود |
| **Allowlist** | مجموعه صریح identity/Route مجاز |
| **COR-01** | correctness gate یک GiB دوطرفه با hash انتهایی |

</div>
