# PLAN

## Stage A — contract & spike
- [x] ثبت مراجع و hash مورد انتظار Blueprint
- [x] ایجاد ساختار repository و فایل‌های وضعیت
- [x] pin نسخه هدف Go از منبع رسمی
- [x] ایجاد skeleton باینری و parser اولیه BAFT/1
- [x] golden vectors protocol
- [x] config schema سخت‌گیرانه
- [x] PKI آزمایشی ephemeral در تست‌ها
- [x] prototype واقعی full-duplex HTTP/2 + mTLS + cancellation
- [x] اثبات 4 TCP مستقل برای 4 shard در smoke test
- [x] اجرای gate نهایی با Go 1.27.1

## Stage B — secure vertical slice
- [x] BAFT/1 HELLO / HELLO_ACK / READY برای نشست جدید
- [x] یک route ثابت allowlisted به‌صورت end-to-end
- [x] OPEN/DATA/ACK/WINDOW/FIN/FIN_ACK پایه
- [x] انتقال دوطرفه واقعی با half-close و hash انتهایی برابر
- [x] negative certificate tests: allowlist / expired / SAN / CA
- [x] route target injection و duplicate JSON key rejection
- [x] duplicate DATA و invalid ACK/WINDOW unit tests
- [x] idempotent OPEN بدون target redial دوم
- [x] deny/revocation روی carrier فعال
- [x] RESET و error mapping پایه/فهرست ثابت
- [ ] آزمون 1GiB دوطرفه COR-01
- [x] config YAML loader سخت‌گیرانه + dependency/checksum pin
- [ ] production node CLI برای اجرای IR/EX خارج از integration harness

## Stage C — resources & multi-flow
- [ ] global allocator و reservation واحد
- [ ] DRR scheduler
- [ ] control queue bounded
- [ ] multi-Flow / multi-Shard production path
- [ ] bounded receive/replay memory budgets
- [ ] slow receiver / backpressure soak tests

## Stage D — resume
- [ ] session/boot/epoch fencing کامل
- [ ] snapshots/replay/tombstones
- [ ] duplicate-free carrier replacement

## Stage D2 — endpoint pool / relay
- [ ] direct + relay endpoint pool
- [ ] fixed-upstream baft relay

## Stage E/F/G/H
- [ ] correlated-state + digest experiments
- [ ] benchmark
- [ ] research lab
- [ ] packaging/operations
