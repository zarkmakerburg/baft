<div dir="rtl" align="right" lang="fa">

# BAFT BCC Visual System

این فایل مرجع هویت بصری رابط BAFT است و از تم واقعی BCC استخراج شده است.

## رنگ‌های پایه

<div dir="ltr" align="left">

```text
Background        #090b10
Radial surface    #182238
Card              #111722
Input / inset     #0b1019
Border            #263147
Primary text      #eef1f6
Muted text        #8e9aaf
Gold accent       #e8b54a
Healthy           #92f0bf
Healthy surface   #123e2c
Error             #ffabb6
Error surface     #4b2026
Neutral surface   #37333a
```

</div>

## اصول رابط

- پس‌زمینهٔ بسیار تیره با radial navy در بالای صفحه.
- کارت‌های مستقل با border کم‌کنتراست و radius حدود 16px.
- طلایی فقط برای action اصلی، highlight و state مهم استفاده شود.
- متن اصلی سفید مایل به آبی و متن ثانویه خاکستری-آبی باشد.
- success سبز و failure قرمز فقط برای status استفاده شوند.
- UI متراکم اما خلوت؛ از gradient و glow فقط برای hero یا signal مهم استفاده شود.
- فارسی همیشه RTL و داده‌های فنی، کد، hash، IP و commandها LTR باشند.

## توکن‌های CSS مرجع

<div dir="ltr" align="left">

```css
:root {
  --bcc-bg: #090b10;
  --bcc-bg-radial: #182238;
  --bcc-card: #111722;
  --bcc-inset: #0b1019;
  --bcc-border: #263147;
  --bcc-text: #eef1f6;
  --bcc-muted: #8e9aaf;
  --bcc-gold: #e8b54a;
  --bcc-success: #92f0bf;
  --bcc-success-bg: #123e2c;
  --bcc-error: #ffabb6;
  --bcc-error-bg: #4b2026;
  --bcc-neutral-bg: #37333a;
  --bcc-radius-card: 16px;
  --bcc-radius-control: 10px;
}
```

</div>

## منبع

این توکن‌ها از stylesheet فعلی `internal/bcc/dashboard.go` استخراج شده‌اند و مرجع یکپارچه‌سازی UI/Docs پروژه هستند.

</div>
