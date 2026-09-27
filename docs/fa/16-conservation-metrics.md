# 16 — Metrics حفاظتی و Conservation Metrics

## تصمیم

فرمت metrics را اختراع نمی‌کنیم. endpoint روی loopback از Prometheus text exposition `0.0.4` استفاده می‌کند تا ابزارهای موجود بتوانند آن را scrape کنند.

نوآوری در **semantic surface** است: به‌جای انتشار peer identity، Route، target یا stream labelهای پرکاردینالیتی، BAFT رابطه‌های conservation را به‌صورت aggregate گزارش می‌کند.

## endpoint

```text
GET http://127.0.0.1:<port>/metrics
Content-Type: text/plain; version=0.0.4; charset=utf-8
```

config از قبل metrics listener را به loopback محدود می‌کند.

## metricهای اصلی

- `baft_active_flows`
- `baft_resource_receive_bytes`
- `baft_resource_replay_bytes`
- `baft_resource_total_bytes`
- `baft_conservation_accepted_backlog_bytes` = مجموع A-D
- `baft_conservation_credit_exposure_bytes` = مجموع C-D
- `baft_conservation_replay_outstanding_bytes` = مجموع txNext-txAcked
- `baft_conservation_invariant_violations`

## خلأ

metrics رایج اغلب counterهای مستقل را نشان می‌دهند. در BAFT، خطر اصلی می‌تواند ناسازگاری رابطه بین stateها باشد. بنابراین `invariant_violations` و conservation gaps داده درجه‌اول هستند.

## حریم خصوصی و cardinality

عمداً هیچ label شامل:
- peer identity؛
- target address؛
- Route ID؛
- stream ID

در endpoint پایه وجود ندارد.

## ریسک

aggregation می‌تواند محل دقیق Flow خراب را پنهان کند. برای debugging عمیق، admin/doctor آینده باید snapshot داخلی را روی Unix socket محدود نمایش دهد، نه اینکه label حساس را روی metrics عمومی‌تر کند.
