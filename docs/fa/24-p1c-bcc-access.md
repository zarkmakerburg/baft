<div dir="rtl" align="right" lang="fa">

# 24 — P1-C (بخش اول): دسترسی وب به BCC

قدم P1-C از [22-launch-1-roadmap.md](22-launch-1-roadmap.md). این بخش دربارهٔ راه رسیدن اپراتور به داشبورد BCC است. انتقال state از فایل JSON به SQLite نسخه‌دار بخش دوم است. جزئیات کامل در [نسخهٔ انگلیسی](../en/24-p1c-bcc-access.md).

## مدل

سه credential مستقل که با هم ساخته و با هم عوض می‌شوند:

- **مسیر مخفی:** ۳۲ کاراکتر تصادفی، مثل `https://bcc.example.com/<secret>/`. فقط نشانی است، نه رمز.
- **نام کاربری:** `op-` به‌اضافهٔ ۱۰ کاراکتر تصادفی.
- **رمز:** ۲۴ کاراکتر تصادفی (حدود ۱۴۳ بیت). فقط hash آن ذخیره می‌شود (PBKDF2-SHA256 با ۶۰۰ هزار تکرار و salt تصادفی).

فایل دسترسی (پیش‌فرض `<state-file>.access.json`) باید فقط برای مالک قابل دسترس باشد (0600)، وگرنه BCC بالا نمی‌آید.

مسیر مخفی یک لایهٔ اضافه است، نه جایگزین احراز هویت: هر درخواست زیر آن هم session لازم دارد. مسیر ریشه و هر مسیر دیگر 404 برمی‌گرداند.

## فقط از کنسول

<div dir="ltr" align="left">

```bash
sudo baft-bcc access init        # first time: prints path, username and password once
sudo baft-bcc access regenerate  # replaces all three; old ones stop working; every session ends
sudo baft-bcc access show        # path and username (the password is not stored)
```

</div>

بازیابی رمز از وب وجود ندارد و هیچ endpoint وبی دسترسی را عوض نمی‌کند. BCC در حال اجرا ظرف یک ثانیه فایل جدید را می‌بیند: مسیر قدیمی 404 می‌شود و همهٔ sessionها بدون restart قطع می‌شوند.

## Session

- ورود از `/<secret>/`. نام کاربری اشتباه همان هزینهٔ رمز اشتباه را دارد؛ هر دو در audit ثبت می‌شوند و بعد از ۵ تلاش ناموفق، IP پنج دقیقه مسدود می‌شود.
- cookie با `HttpOnly`، `SameSite=Strict`، `Path=/<secret>/` و در HTTPS با `Secure`. BCC فقط SHA-256 توکن را در حافظه نگه می‌دارد (restart یعنی خروج همه).
- مهلت بیکاری ۳۰ دقیقه، عمر کل ۱۲ ساعت.
- درخواست‌های تغییردهنده باید توکن CSRF را در `X-BAFT-CSRF` بفرستند؛ داشبورد این کار را می‌کند.
- session فقط برای درخواست‌هایی که از `/<secret>/api/...` می‌آیند معتبر است. API با bearer token در `/api/...` و API agent تغییری نکرده‌اند.

## HTTPS

- با `--tls-cert` و `--tls-key`. گواهی بعد از تمدید (مثلاً با certbot) بدون restart خوانده می‌شود و تمدید خراب، گواهی سالم قبلی را نگه می‌دارد.
- آدرس غیر loopback بدون TLS رد می‌شود، مگر با `--allow-insecure-http`. روی loopback (از راه SSH tunnel یا reverse proxy محلی) HTTP ساده مجاز است.

## تست‌ها

`internal/bcc/access_test.go` و `cmd/baft-bcc/access_test.go`: ذخیرهٔ فقط hash، دسترسی فایل، داشبورد فقط زیر مسیر مخفی، ورود، پرچم‌های cookie، CSRF، رد session بیرون از مسیر مخفی، قطع sessionها و credentialهای قدیمی بعد از regenerate، خروج، مهلت بیکاری، مسدودسازی، audit، فرمان‌های کنسول و بارگذاری دوبارهٔ گواهی.

</div>
