<div dir="rtl" align="right" lang="fa">

# موانع و مسائل باز

> English: [BLOCKERS.en.md](BLOCKERS.en.md)

## موارد رفع‌شده

### ایجاد مخزن GitHub
مخزن `zarkmakerburg/baft` ایجاد شده و مسیر commit/CI فعال است.

### Go 1.27.1
GitHub Actions نسخه pin‌شده Go 1.27.1 را نصب و تست‌ها را روی همان toolchain اجرا کرده است.

### dependency YAML
`go.yaml.in/yaml/v3 v3.0.5` pin شده و checksumهای `go.sum` ثبت شده‌اند.

## مسئله مهندسی باز Stage C

slow-receiver liveness gate هنوز سبز پایدار نیست. این مورد «مانع خارجی» نیست؛ یک correctness/backpressure issue در مسیر توسعه Stage C است و باید با تست و تحلیل حل شود.

تا حل آن، Stage C نباید complete علامت بخورد.

## اصل

BLOCKER به معنی کمبود دسترسی، dependency یا پیش‌نیاز است. failure تست correctness باید به‌عنوان مسئله فنی ثبت و اصلاح شود، نه با تغییر برچسب پنهان شود.

</div>
