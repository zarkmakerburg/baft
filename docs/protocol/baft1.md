<div dir="rtl" align="right" lang="fa">

# یادداشت پروتکل BAFT/1

> English: [baft1.en.md](baft1.en.md)  
> راهنمای کامل فارسی: [../fa/04-protocol-baft1.md](../fa/04-protocol-baft1.md)

## header

BAFT/1 header دقیقاً 24 بایت و big-endian است:

| Offset | Size | Field |
|---:|---:|---|
| 0 | 4 | frame_len |
| 4 | 1 | type |
| 5 | 1 | flags |
| 6 | 2 | reserved |
| 8 | 8 | stream_id |
| 16 | 8 | offset |

payload حداکثر 65536 بایت و کل frame حداکثر 65560 بایت است. DATA baseline در chunkهای 32768 بایتی تولید می‌شود.

Golden vectors در [golden-vectors.json](golden-vectors.json) نگهداری می‌شوند.

## قرارداد

این فایل خلاصه implementation است. توضیح state machine، OPEN/DATA/ACK/WINDOW/FIN/RESET و تفکیک «قابلیت فعلی» از «frame رزروشده برای Stage D» در راهنمای کامل پروتکل آمده است.

</div>
