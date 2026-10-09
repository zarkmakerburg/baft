<div dir="rtl" align="right" lang="fa">

# BAFT Welcome + Login V3 — 50-step implementation ledger

**Scope:** isolated static UI preview, not production login. Branch `ui/bcc-command-center-v3`; PR #159 remains Draft.

**Evidence standard:** `Implemented` means source code added, NOT browser-tested, security-accepted or deployed. The logo file must be supplied/verified before claiming official-logo integration.

## هویت و تجربه ورود
1. **Implemented** — حفظ تم سرمه‌ای/طلایی
2. **Implemented** — چیدمان مینیمال سوئیسی
3. **Implemented** — تیتر خوشامدگویی
4. **Implemented** — معرفی مرکز فرماندهی
5. **Implemented** — دکمه ورود واضح
6. **Implemented** — لینک امکانات
7. **Implemented** — سه کارت قابلیت
8. **Implemented** — گرافیک انتزاعی شبکه
9. **Implemented** — فضای تنفس بصری
10. **Implemented** — نسخه موبایل

## زبان و دسترس‌پذیری
11. **Implemented** — فارسی پیش‌فرض
12. **Implemented** — انگلیسی کامل
13. **Implemented** — RTL فارسی
14. **Implemented** — LTR انگلیسی
15. **Implemented** — تغییر زبان بدون بارگذاری
16. **Implemented** — حالت تاریک
17. **Implemented** — حالت روشن
18. **Pending WCAG measurement** — رنگ‌ها تعیین شده‌اند؛ کنتراست باید اندازه‌گیری شود
19. **Implemented** — نمای فوکوس کیبورد
20. **Implemented** — لینک پرش به محتوا

## امنیت و صداقت محصول
21. **Implemented** — برچسب صریح پیش‌نمایش
22. **Implemented** — غیرفعال بودن فرم ورود
23. **Implemented** — غیرفعال بودن فیلد کاربری
24. **Implemented** — غیرفعال بودن فیلد رمز
25. **Implemented** — عدم ارسال درخواست شبکه توسط فرم
26. **Implemented** — عدم ذخیره اعتبارنامه
27. **Implemented** — عدم ادعای احراز هویت
28. **Implemented** — متن راهنمای ورود رسمی
29. **Implemented** — noindex/nofollow
30. **Implemented** — عدم ارسال referrer

## جزئیات رابط
31. **Pending asset verification** — مسیر لوگوی رسمی مشخص است؛ فایل اصلی هنوز تأیید نشده
32. **Implemented** — fallback نوشتاری BAFT
33. **Implemented** — سایز واکنش‌گرای لوگو
34. **Implemented** — دکمه تغییر تم
35. **Implemented** — دکمه تغییر زبان
36. **Implemented** — برچسب‌گذاری فرم
37. **Implemented** — ناحیه پیام وضعیت
38. **Implemented** — ریسپانسیو تبلت
39. **Implemented** — ریسپانسیو موبایل کوچک
40. **Implemented** — کاهش حرکت طبق ترجیح کاربر

## آمادگی اتصال واقعی
41. **Implemented (preview-only)** — جداسازی کامل از backend
42. **Implemented** — عدم تغییر مسیرهای API
43. **Implemented** — عدم تغییر middleware امنیتی
44. **Implemented** — عدم تغییر کوکی یا نشست
45. **Implemented** — عدم تغییر login عملیاتی
46. **Implemented** — عدم تغییر پورت سرورها
47. **Implemented** — عدم تغییر تونل‌ها
48. **Implemented** — مستندسازی نیاز به asset اصلی
49. **Planned** — برنامه بررسی ورود واقعی در مرحله بعد
50. **Pending verification** — نیاز به تست مرورگر، بازبینی امنیت و تأیید قبل از انتشار

## Acceptance gates

- Verify original BAFT logo asset at `web/assets/baft-official-logo.png` (not included; text BAFT is fallback).
- Run accessibility checks (keyboard, screen reader, WCAG contrast), mobile browser smoke tests, light/dark and RTL/LTR checks.
- Review backend authentication routes, session management, CSRF, rate limiting and security headers separately before enabling a live form.
- Do not connect a preview password form to an API or invent a login endpoint.
- Do not merge or deploy without explicit acceptance and test evidence.
## Execution evidence — iteration 1

- CI workflow updated to trigger on welcome/login HTML and this ledger, in addition to JS and test files.
- Logo fallback now handles cached images; the actual original BAFT asset remains missing and must not be fabricated.
- Anchor scroll margin and image dimensions improved.
- These steps are **source-level implementation**, not 50 independent QA passes. Never mark contrast, browser QA, asset verification or live auth as accepted without evidence.


## CI verification checkpoint — 2026-10-09 (evidence only)

- Branch HEAD before this documentation update: `a43505ec417fba45315279634bbbea48a2a68eb8`.
- GitHub Actions results on that exact SHA: `ci` **success** (run `37904600007`); `static-analysis` **success** (run `37904599968`); `BCC V3 UI contract tests` **success** (run `37904600025`); `r3.1-probabilistic-morphing` **success** (run `37904599939`).
- Workflow `.github/workflows/bcc-v3-ui-tests.yml` now pins `actions/checkout` and `actions/setup-node` to full commit SHAs. The earlier unpinned-action CI failure is not overwritten or reclassified.
- The contract test measures a defined set of palette-token foreground/background ratios; it is **not** a full computed-style, keyboard, screen-reader, or WCAG browser acceptance. Step 18 remains pending full measurement.
- The official transparent logo asset remains unverified (step 31); live authentication and final acceptance (steps 49–50) remain open. This documentation-only checkpoint does not approve the 50-step program or BCC audit 150/150.
- No production tunnel/server, merge, deployment, release or tag is authorized by this checkpoint.

## CI verification checkpoint — 2026-10-09, follow-up (evidence only)

- Verified branch tip before this update: `f7ca400f21f9b8ffb753df743f1c280864ba5602` (`compare_commits` reported `identical`, ahead=0, behind=0).
- All four GitHub Actions runs associated with **that exact SHA** completed successfully: `ci` run `37910985804`, `static-analysis` run `37910985738`, `BCC V3 UI contract tests` run `37910985794`, and `r3.1-probabilistic-morphing` run `37910985733`.
- These are workflow results for the preceding documentation checkpoint; they do **not** establish a full WCAG computed-style/screen-reader acceptance, an owner-verified transparent logo, or approved real login operation. Welcome/Login steps **18, 31, 49 and 50** remain open for their respective acceptance evidence.
- BCC 150-step final gate remains **BLOCKED**: approved isolated enrolled IR/EX agents, real link-scoped telemetry, visual fidelity and remaining operational localization are not accepted. No production tunnel, deployment, merge, tag or release is authorized.

</div>
