# BAFT BCC — 150-step UI audit

Status: IN PROGRESS; unchecked items are not verified. Draft PR #159 only; no production deployment.

## Navigation and design foundation
1. [x] SOURCE INVENTORY — 16 static dashboard cards in `internal/bcc/dashboard.go`; dynamic section verification remains open
2. [x] SOURCE INVENTORY — 31 static `<button>` elements in `internal/bcc/dashboard.go` (dynamic buttons remain in step 8)
3. [x] SOURCE INVENTORY — 29 static `<input>` elements in `internal/bcc/dashboard.go`
4. [x] SOURCE INVENTORY — 10 static `<select>` elements in `internal/bcc/dashboard.go`
5. [ ] TODO — inventory links
6. [ ] TODO — inventory badges
7. [ ] TODO — inventory dialogs
8. [x] SOURCE INVENTORY — 8 dynamic `<button>` templates with `onclick` in `internal/bcc/dashboard.go`; verify generated DOM and permissions separately
9. [ ] SOURCE STATIC CHECK — 30 distinct onclick expressions across 31 static button tags (including 8 dynamic templates); all named functions have declarations in dashboard.go. No browser, API, authz, disabled-state, or generated-DOM PASS yet.
10. [ ] SOURCE AUDIT — backend dashboard.go contains zero <nav> elements and zero <a> links; API-backed functional sections exist but no explicit navigation landmark or link map. Needs browser/keyboard/RTL/mobile acceptance and V3 prototype comparison.
11. [ ] SOURCE AUDIT — operational dashboard declares 8 --bcc-* color tokens but retains multiple hard-coded legacy colors and dark-only color-scheme; V3 preview defines 8 shared-named theme variables in each of light/dark themes. Shared semantic token system and browser contrast checks remain pending.
12. [ ] SOURCE AUDIT — dashboard and V3 preview use divergent hard-coded spacing values (dashboard 24/18/12/10/6px; preview 26/25/22/20/18/15/14/13/12/8/7px). Breakpoints differ: dashboard 820/520px, preview 950/550px. Shared spacing scale and responsive acceptance pending.
13. [ ] SOURCE AUDIT — backend dashboard uses Inter/system-ui with explicit 11/13/20/24px sizes, line-height 1.55, html lang=en; V3 preview uses Inter/Tahoma/system-ui with explicit 9–26px sizes, line-height 1.8, initial fa/RTL and language-direction switching. Shared typography scale, Persian fallback and browser zoom/readability acceptance pending.
14. [ ] SOURCE AUDIT — backend dashboard: card 16px, button/form 10px, KPI/canvas 12px, badge pill 999px; V3 preview: card 15px, button/nav 10px. No shared semantic radius tokens; form/dialog/badge browser verification pending.
15. [ ] SOURCE AUDIT — backend dashboard defines box-shadow:0 8px 28px #0002 and mixes hard-coded borders with --bcc-line; V3 preview has no explicit box-shadow, uses --line borders. Focus outlines differ (--bcc-focus vs --gold); no explicit z-index in either inspected CSS. Shared elevation tokens and overlay/browser acceptance pending.

## Welcome and login
16. [ ] IMPLEMENTED IN V3 PREVIEW — separate bilingual Welcome view, explicit demo-only entry CTA, focus handoff and responsive styling added in commit 7d75ff9a; browser/mobile/keyboard acceptance still pending. This is NOT BCC authentication.
17. [ ] PARTIAL IMPLEMENTATION — responsive accessible BAFT monogram placeholder implemented in V3 Welcome (commit b16407b3); official owner-approved GoldApp logo asset still missing and must not be invented. Browser acceptance pending.
18. [ ] IMPLEMENTED IN V3 PREVIEW — `setApprovedWelcomeLogo(assetUrl)` validates a relative image path, loads owner-approved logo, swaps on success and preserves accessible BAFT monogram on image error (commit 88e2110a). Needs actual approved asset and browser tests before PASS.
19. [ ] IMPLEMENTED IN V3 PREVIEW — welcome heading balanced line wrapping and line-height, constrained readable text width, mobile typography/padding, CTA width, forced-colors borders (commit 1647e6ad); browser contrast, viewport and keyboard acceptance pending.
20. [ ] IMPLEMENTED + CHROME DOM SMOKE — responsive inline SVG network illustration implemented (commit 885b0345); Chrome headless rendered DOM confirmed welcome, CTA, SVG, RTL and initially hidden dashboard. Full interaction, visual viewport and JS console acceptance pending.
21. [ ] IMPLEMENTED IN V3 PREVIEW — three bilingual responsive feature cards (Topology, Status/sample data, Management/preview) added to Welcome in commit 6d3aaebe. No false live status; browser/keyboard/mobile acceptance pending at step 25.
22. [ ] IMPLEMENTED IN V3 PREVIEW — guarded entry CTA against duplicate activation, visible focus style, dashboard heading focus handoff and scroll-to-start (commit cd6d39f5). Browser keyboard/click acceptance pending at step 25.
23. [ ] IMPLEMENTED IN V3 PREVIEW — entry CTA targets `main h1` explicitly, guards absent heading, and adds visible focus outline (cb605572). Browser keyboard/mobile acceptance pending step 25.
24. [ ] IMPLEMENTED IN V3 PREVIEW — added bilingual welcome heading context clarifying demo entry is not real sign-in (160289e7). Browser visual/FA-EN acceptance pending step 25.
25. [x] PREVIEW BROWSER QA PASS (not production auth acceptance) — Real Puppeteer Chrome on authorized Mac, 1440x900 and 390x844. Initially FAIL: invalid JavaScript token in logo path validator; repaired in ca4505e8. Retest PASS: welcome hidden/app shown after click, heading focus, FA/EN RTL/LTR toggle, light/dark toggle, no horizontal overflow, zero pageerror/console errors. Caveats: file:// local preview, no authenticated BCC API, CSRF/server rate-limit/session tests; mobile is emulated viewport, not physical handset.
26. [ ] PREVIEW ONLY — explicitly associated demo entry button with its no-authentication disclaimer via aria-describedby (6138f099). No actual BCC auth route implemented or accepted; production auth route review remains pending.
27. [ ] PREVIEW ONLY — one-time in-memory demo entry guard prevents repeated transition (56a5ede1). This is NOT authentication/session security; real BCC session lifecycle and logout/security checks remain pending.
28. [ ] PREVIEW-ONLY BOUNDARY — read-only BCC adapter uses GET, same-origin credentials, no body, no-referrer, no token persistence (039d0992). This is NOT a CSRF security test or proof of server-side enforcement; audit all real mutating BCC routes, anti-CSRF tokens and Origin checks before acceptance.
29. [ ] PREVIEW ONLY — client-side 3-second minimum interval between BCC snapshot attempts, with existing in-flight button disable (99ca4a10). Not a security rate limit: server-side auth/IP/user throttling, 429 responses and distributed abuse handling still require audit and tests.
30. [ ] BLOCKED / NOT ACCEPTED — login integration gate: preview has no real BCC authentication, server-side CSRF and rate-limit checks not verified, session lifecycle not tested, and step 25 interactive/mobile/browser gate remains partial. Do not label PASS or deploy. Evidence required: real auth route inventory, unauthorized/expired session tests, CSRF negative tests, 429/rate-limit tests, keyboard/FA-EN/light-dark/mobile browser matrix.

## Localization and RTL
31. [x] PREVIEW-ONLY — localized user-facing BCC read-only adapter messages (throttle, token prompt/required, snapshot summary, load error) according to document lang fa/en. Commit e5c9e993. Static source reviewed; browser re-test of adapter paths not performed, production API/auth NOT accepted.
32. [x] PREVIEW-ONLY — added FA/EN translations for previously hardcoded dashboard snapshot import label, initial snapshot status, read-only BCC API heading/notice, load button, initial BCC status and globe selection text. Commit 655c1843. Static change; browser re-test pending. Runtime-updated messages remain owned by adapter/snapshot/globe scripts; not a production localization acceptance.
33. [x] PREVIEW-ONLY — RTL bidi hardening: Persian-friendly Tahoma first, LTR coordinate JSON textarea, isolated brand and dynamic metric identifiers, wrapping language/theme controls, localized aria-labels. Commit 9f1b0805. Source review only; actual RTL browser regression and mobile matrix pending at step 35.
34. [x] PREVIEW-ONLY — LTR alignment hardening: English-mode card/detail alignment, minimum 44px toggle targets, explicit LTR topology-file control, and localized document title. Commit 91fa33b1. Source reviewed; actual browser matrix scheduled for step 35, not yet PASS.
35. [x] PREVIEW CHROME QA PASS — authorized Mac Chrome/Puppeteer 8/8 viewport×language×theme matrix (1440x900, 390x844 emulation; FA/EN; dark/light), welcome→dashboard interaction, H1 focus, localized title/button/aria, no horizontal overflow, zero captured JS page/console errors. No physical mobile, real BCC auth, server CSRF, API integration, or production acceptance. Logical-properties deep audit remains open for subsequent step.
36. [x] PREVIEW-ONLY — selected-link updatedAt timestamps now render via Intl.DateTimeFormat fa-IR/en-GB, explicit UTC and stable date/time parts; missing/invalid values show em dash. Commit 0e0b4610. Static review only; runtime timestamp browser regression pending next browser gate. No BCC API production acceptance.
37. [x] PREVIEW-ONLY — added locale-aware Intl.NumberFormat fa-IR/en-GB helper and opt-in data-number formatting in render(), with finite-number guard, up to 2 fractional digits, auto direction. Commit f09ba6fd. Source-only check; numeric runtime regression pending Step 40 browser gate. No real BCC data.
38. [x] PREVIEW-ONLY — CSS [data-ip]/.ip-address explicit LTR isolation, overflow-wrap and selected link identifier dir=auto. Commit c2dca310. Browser matrix at Step40 verified IPv6 LTR.
39. [x] PREVIEW-ONLY — bilingual UP/DOWN/UNKNOWN/STALE status mapping, localized aria-label and status class. Commit 54e66a80. Browser Step40 verified UNKNOWN labels FA/EN.
40. [x] PREVIEW CHROME QA PASS — safe bilingual INVALID_FILE/NETWORK_ERROR/UNAUTHORIZED/UNKNOWN error mapping with generic fallback and alert role. Commit 5c90ae07. Real Mac Chrome/Puppeteer Step40 8/8 combinations desktop 1440, mobile 390 emulated × FA/EN × dark/light: numbers fa-IR/en-GB, UNKNOWN status, INVALID_FILE error, IPv6 LTR, H1 focus, no overflow, zero JS console/page errors. No real BCC auth/API, no physical mobile or production acceptance.
41. [x] PREVIEW-ONLY — button labels — explicit button type and bilingual aria-pressed state on language/theme controls. Code commit 6983d0ed.
42. [x] PREVIEW-ONLY — tooltips — localized title tooltips for language/theme controls. Code commit 6983d0ed.
43. [x] PREVIEW-ONLY — screen reader language — document lang/dir and localized nav span lang; removed mixed-language nav aria suffix. Code commit 6983d0ed.
44. [x] PREVIEW-ONLY — language preference — validated persisted fa/en and dark/light values, browser-locale default, safe localStorage fallback. Code commit 6983d0ed.
45. [x] PREVIEW-ONLY — missing translations — guarded dictionary lookup and explicit fallback instead of undefined text; Mac Chrome 8/8 regression matrix PASS (1440/390 × FA/EN × dark/light, zero captured JS errors/overflow), focused H1; deeper accessibility tests remain open. Code commit 6983d0ed.

## Theme and accessibility
46. [x] PREVIEW-ONLY — consolidated gold-soft/focus-ring navy-gold token usage. Commit b3b1da45.
47. [x] PREVIEW-ONLY — light theme palette preserved, success/warning contrast adjusted. Commit b3b1da45.
48. [x] PREVIEW-ONLY — dark theme palette retained with explicit warning token. Commit b3b1da45.
49. [x] PREVIEW-ONLY — semantic success color token and .status.good/[data-severity=success]. Commit b3b1da45.
50. [x] PREVIEW-ONLY — semantic warning token and .status.warning/[data-severity=warning]; Mac Chrome 8/8 regression PASS; contrast audit pending. Commit b3b1da45.
51. [x] PREVIEW-ONLY — semantic error severity color. Code commit 79fbf45a.
52. [x] PREVIEW-ONLY — semantic unknown color per theme. Code commit 79fbf45a.
53. [x] PREVIEW-ONLY — base text palette retained; WCAG numerical audit pending. Code commit 79fbf45a.
54. [x] PREVIEW-ONLY — note/logo-note min font 12px; WCAG numerical audit pending. Code commit 79fbf45a.
55. [x] PREVIEW-ONLY — gold border on interactive hover; contrast audit pending. Code commit 79fbf45a.
56. [x] PREVIEW-ONLY — 3px keyboard focus rings for buttons/inputs/textarea/select/globe. Code commit 79fbf45a.
57. [x] PREVIEW-ONLY — disabled opacity and non-interactive cursor. Code commit 79fbf45a.
58. [x] PREVIEW-ONLY — hover border/filter state. Code commit 79fbf45a.
59. [x] PREVIEW-ONLY — active press transform/background. Code commit 79fbf45a.
60. [x] PREVIEW-ONLY — prefers-reduced-motion disables animations/transitions and smooth scrolling; Mac Chrome 8/8 smoke PASS. Code commit 79fbf45a.

## Monitoring and history
61. [x] PREVIEW-ONLY — bilingual monitoring section and explanation. Commit fbe983f1.
62. [x] PREVIEW-ONLY — snapshot node count or dash without data. Commit fbe983f1.
63. [x] PREVIEW-ONLY — healthy-link count. Commit fbe983f1.
64. [x] PREVIEW-ONLY — down-link count. Commit fbe983f1.
65. [x] PREVIEW-ONLY — unknown/degraded/stale-link count. Commit fbe983f1.
66. [x] PREVIEW-ONLY — responsive table, textContent-only rows. Commit fbe983f1.
67. [x] PREVIEW-ONLY — dedicated column; dash until metric exists. Commit fbe983f1.
68. [x] PREVIEW-ONLY — available snapshot RTT shown in ms, not independently measured health latency. Commit fbe983f1.
69. [x] PREVIEW-ONLY — dedicated column; dash until metric exists. Commit fbe983f1.
70. [x] PREVIEW-ONLY — snapshot updatedAt formatted per language; Step70 Chrome 8/8 smoke PASS. Commit fbe983f1.
71. [x] PREVIEW-ONLY — sessions KPI placeholder (no source metric). Commit 679a2541.
72. [x] PREVIEW-ONLY — routes KPI placeholder (no source metric). Commit 679a2541.
73. [x] PREVIEW-ONLY — 1h/24h/7d range selector. Commit 679a2541.
74. [x] PREVIEW-ONLY — accessible SVG empty-state chart; no invented history. Commit 679a2541.
75. [x] PREVIEW-ONLY — bilingual no-data legend. Commit 679a2541.

## Nodes and SSH
76. [x] PREVIEW-ONLY — safe textContent node ID from validated snapshot. Commit 679a2541.
77. [x] PREVIEW-ONLY — safe textContent node name/alias from validated snapshot. Commit 679a2541.
78. [x] PREVIEW-ONLY — management address column; dash until trusted source. Commit 679a2541.
79. [x] PREVIEW-ONLY — IPv4 column; dash until trusted source. Commit 679a2541.
80. [x] PREVIEW-ONLY — IPv6 column; dash until trusted source; Chrome Step80 8/8 smoke PASS. Commit 679a2541.
81. [x] PREVIEW-ONLY — disabled node role selector. Commit 6f00e2c3.
82. [x] PREVIEW-ONLY — disabled read-only public key field. Commit 6f00e2c3.
83. [x] PREVIEW-ONLY — redacted token status, never accepts credentials. Commit 6f00e2c3.
84. [x] PREVIEW-ONLY — disabled save action. Commit 6f00e2c3.
85. [x] PREVIEW-ONLY — disabled SSH host field. Commit 6f00e2c3.
86. [x] PREVIEW-ONLY — disabled SSH port field. Commit 6f00e2c3.
87. [x] PREVIEW-ONLY — disabled SSH user field. Commit 6f00e2c3.
88. [x] PREVIEW-ONLY — disabled host key scan action. Commit 6f00e2c3.
89. [x] PREVIEW-ONLY — independent host fingerprint verification warning. Commit 6f00e2c3.
90. [x] PREVIEW-ONLY — disabled agent install action; Step90 Mac Chrome smoke 8/8 PASS. Commit 6f00e2c3.

## Tunnels and discovery
91. [x] PREVIEW-ONLY — snapshot links shown as existing connections, not verified deployed tunnels. Commit f202ac8c.
92. [x] PREVIEW-ONLY — discover-all action visible and disabled. Commit f202ac8c.
93. [x] PREVIEW-ONLY — discovery refresh visible and disabled. Commit f202ac8c.
94. [x] PREVIEW-ONLY — control/carrier/data-plane verification notice. Commit f202ac8c.
95. [x] PREVIEW-ONLY — health refresh visible and disabled. Commit f202ac8c.
96. [x] PREVIEW-ONLY — snapshot-backed bilingual tunnel table. Commit f202ac8c.
97. [x] PREVIEW-ONLY — tunnel refresh visible and disabled. Commit f202ac8c.
98. [x] PREVIEW-ONLY — empty-state plan preview disclosure. Commit f202ac8c.
99. [x] PREVIEW-ONLY — disabled confirmation checkbox. Commit f202ac8c.
100. [x] PREVIEW-ONLY — disabled deploy button; Chrome Step100 smoke 8/8 results observed. Commit f202ac8c.
101. [x] PREVIEW-ONLY — disabled cancel control. Commit 98b8605d.
102. [x] PREVIEW-ONLY — disabled drift-check control. Commit 98b8605d.
103. [x] PREVIEW-ONLY — disabled certificate-rotation control. Commit 98b8605d.
104. [x] PREVIEW-ONLY — disabled decommission control. Commit 98b8605d.
105. [x] PREVIEW-ONLY — disabled retry-decommission control. Commit 98b8605d.

## Connectivity and recovery
106. [x] PREVIEW-ONLY — bilingual empty-state change history table. Commit 98b8605d.
107. [x] PREVIEW-ONLY — disabled history refresh control. Commit 98b8605d.
108. [x] PREVIEW-ONLY — SSH migration rollback/fallback disclosure. Commit 98b8605d.
109. [x] PREVIEW-ONLY — disabled dual-listen checkbox. Commit 98b8605d.
110. [x] PREVIEW-ONLY — explicit warning against removing old SSH path before new access verified; Chrome smoke 8/8. Commit 98b8605d.
111. [x] PREVIEW-ONLY — disabled route doctor action. Commits 6f7dec8d, f7ff8bd3.
112. [x] PREVIEW-ONLY — accessible diagnosis empty-state status. Commits 6f7dec8d, f7ff8bd3.
113. [x] PREVIEW-ONLY — disabled path graph refresh action. Commits 6f7dec8d, f7ff8bd3.
114. [x] PREVIEW-ONLY — snapshot-driven read-only edge table. Commits 6f7dec8d, f7ff8bd3.
115. [x] PREVIEW-ONLY — connectivity matrix count placeholder, no verified pair matrix. Commits 6f7dec8d, f7ff8bd3.
116. [x] PREVIEW-ONLY — disabled pair probe action. Commits 6f7dec8d, f7ff8bd3.
117. [x] PREVIEW-ONLY — probe result empty state. Commits 6f7dec8d, f7ff8bd3.
118. [x] PREVIEW-ONLY — stale-data warning. Commits 6f7dec8d, f7ff8bd3.
119. [x] PREVIEW-ONLY — disabled retry with no invented error. Commits 6f7dec8d, f7ff8bd3.
120. [x] PREVIEW-ONLY — localized empty states; Step120 Chrome first run failed JS syntax, fixed f7ff8bd3 and rerun 8/8 PASS. Commits 6f7dec8d, f7ff8bd3.

## Cluster and audit
121. [x] PREVIEW-ONLY — selectable node inventory sourced from local snapshot. Commit e4c44844.
122. [x] PREVIEW-ONLY — selected-deployment action visible but disabled. Commit e4c44844.
123. [x] PREVIEW-ONLY — local selection state updates without network mutations. Commit e4c44844.
124. [x] PREVIEW-ONLY — disabled preflight gate. Commit e4c44844.
125. [x] PREVIEW-ONLY — disabled confirmation checkbox. Commit e4c44844.
126. [x] PREVIEW-ONLY — read-only jobs empty-state table. Commit e4c44844.
127. [x] PREVIEW-ONLY — job progress column with no fabricated data. Commit e4c44844.
128. [x] PREVIEW-ONLY — job failure column with no fabricated data. Commit e4c44844.
129. [x] PREVIEW-ONLY — explicit backend immutable-audit evidence requirement. Commit e4c44844.
130. [x] PREVIEW-ONLY — disabled audit refresh; Step130 Chrome smoke 8/8 passed. Commit e4c44844.
131. [x] PREVIEW-ONLY — audit evidence requirements note. Commit 4b0a2158.
132. [x] PREVIEW-ONLY — disabled audit pagination controls. Commit 4b0a2158.
133. [x] PREVIEW-ONLY — disabled token rotation control. Commit 4b0a2158.
134. [x] PREVIEW-ONLY — visible kill switch interruption warning. Commit 4b0a2158.
135. [x] PREVIEW-ONLY — disabled destructive confirmation checkbox. Commit 4b0a2158.

## Finance and reports
136. [x] PREVIEW-ONLY — per-node finance verified-data empty state. Commit 4b0a2158.
137. [x] PREVIEW-ONLY — local nonnegative bounded rate input. Commit 4b0a2158.
138. [x] PREVIEW-ONLY — backend versioning/effective date requirement disclosed. Commit 4b0a2158.
139. [x] PREVIEW-ONLY — start/end date inputs. Commit 4b0a2158.
140. [x] PREVIEW-ONLY — client-side date order validation and Step140 Chrome smoke 8/8. Commit 4b0a2158.
141. [ ] TODO — load report
142. [ ] TODO — CSV download
143. [ ] TODO — loading state
144. [ ] TODO — empty state
145. [ ] TODO — error state
146. [ ] TODO — currency
147. [ ] TODO — units
148. [ ] TODO — precision
149. [ ] TODO — table overflow
150. [ ] TODO — download feedback

## Acceptance

- Verify each control in keyboard, touch, mobile, RTL/LTR, light/dark, loading, error, authorization, and empty states.
- Original BAFT logo not yet verified; no substitute logo.
- CI and browser QA required; no merge/deploy without explicit approval.
- First actual dashboard harmonization commit: 914a869.

## Execution ledger — numbered progress

- Step **1/150**: 16 static `.card` sections identified in source. No claim of browser or dynamic-section completeness.
- Steps **2–4/150**: static source inventory counted from live branch: 31 button tags, 29 input tags, 10 select tags. These are **source-inventory only**, not interaction, browser, accessibility or security PASS. Dynamic controls still need step 8.
- Current phase: **150-step audit, inventory subphase**; 50-step welcome/login acceptance remains open (logo, contrast, browser and auth review).
- Report every subsequent action with exact phase and step number; do not infer completion from commits alone.

- Step **8/150**: source inspection found 8 dynamic button templates in dashboard rendering (node token rotation/revoke, tunnel deploy/plan clear, drift, certificate rotation, decommission/cancel). This is a source inventory, **not** a browser/permission PASS. Counts include repeated decommission templates and exclude any DOM created outside button templates.

- Step **9/150**: static handler-name scan found 30 distinct onclick expressions and no missing named function declarations in `internal/bcc/dashboard.go`; **PARTIAL / NOT ACCEPTED**. Next: runtime generated DOM, API/authorization mapping, loading/error/disabled behavior and navigation dead ends.

- Step **10/150**: source audit identified no semantic navigation landmark or anchor in the backend dashboard template (`internal/bcc/dashboard.go`), while API-driven sections exist. **GAP / NOT ACCEPTED**; do not infer that the V3 prototype has the same limitation. Next: design navigation IA and validate focus, active-state, RTL and route/section access before implementation acceptance.

- Step **11/150**: inspected `internal/bcc/dashboard.go` and `web/bcc-command-center-v3-preview.html` at the UI branch. Identified a dark-only operational dashboard and separate light/dark preview palette; colors are not unified. **SOURCE GAP / NOT ACCEPTED**. Proposed semantic roles: bg, surface, field, border, text, muted, accent, focus, success, warning, error, unknown. Do not claim WCAG contrast PASS before browser/computed-style testing.

- Step **12/150**: source CSS inspection shows no shared spacing token system and divergent responsive breakpoints between backend dashboard and V3 preview. **SOURCE GAP / NOT ACCEPTED**; next: define common spacing/breakpoint tokens and test narrow-width overflow, RTL and keyboard/touch in browser. No production UI changed.

- Step **13/150**: source typography inventory completed for backend dashboard and V3 preview. 9px/10px labels in preview require legibility and zoom checks; avoid declaring WCAG PASS from CSS inspection. **SOURCE GAP / NOT ACCEPTED**. Proposed next gate: semantic text-size tokens, minimum readable metadata, RTL/LTR language-specific line-height and 200% zoom/browser tests.

- Step **14/150**: inspected CSS radius rules in backend dashboard and V3 preview. Button 10px is aligned, card 16px vs 15px diverges, and the preview does not expose a complete form/badge radius system in inspected CSS. **SOURCE GAP / NOT ACCEPTED**. Next: standardize control/card/small-card/pill radius tokens and validate generated dialogs/forms and responsive states in browser.

- Step **15/150**: inspected shadow, border, focus and stacking CSS in operational dashboard and V3 preview. **SOURCE GAP / NOT ACCEPTED**: visual elevation language diverges; lack of explicit z-index alone does not prove overlay failure. Proposed elevation tiers: flat/bordered, raised, overlay; check dark/light contrast, focus visibility and stacking in rendered browser before acceptance. No production changes.

- Step **16/150**: inspected V3 preview layout source and operational dashboard structure. V3 is a standalone dashboard preview (header, navigation, main topology overview, demo JSON snapshot), not evidence of a complete welcome/login flow. **SOURCE GAP / NOT ACCEPTED**. Next: explicitly define Welcome/Login entry states, hierarchy, brand presentation, CTA and safe transition into authenticated dashboard; test mobile/desktop and accessibility before PASS.

- Implementation follow-up: unlike prior source-only audits, step 16 now has an actual UI code change in `web/bcc-command-center-v3-preview.html` (commit `7d75ff9a`). This is demo entry only; no auth integration, no production deployment. Browser verification required before marking [x].

- Step **17/150**: actual Welcome brand placeholder implemented; official GoldApp logo not substituted without verified asset. Browser checks scheduled at step 20; previous headless attempts yielded no verified PASS.

- Step **18/150**: implemented error-resilient official-logo opt-in and monogram fallback in preview. No real asset path set, no auth or production change; browser verification due step 20.

- Step **19/150**: implemented Welcome hero readability CSS changes. Browser verification scheduled at step 20; source change alone is not a visual PASS.

- Step **20/150** browser batch (steps 16–20): On Mac Google Chrome headless, exact GitHub preview HTML saved to `/tmp/baft-v3-step20.html`, Chrome dumped 15,354-byte DOM; checks: `welcome=True`, `enterPreview=True`, `welcome-illustration=True`, `previewApp hidden=True`, `dir=rtl=True`. **DOM smoke PASS only**. Chrome stderr included CVDisplayLink and process policy warnings; no claim of interaction/viewport/console PASS. Need click, language/theme, mobile screenshots, missing asset checks in subsequent browser run.

- Step **21/150**: implemented Welcome feature cards in HTML/CSS and FA/EN dictionary. No production change. Browser regression batch due step 25.

- Step **22/150**: implemented Welcome CTA interaction hardening. No auth bypass, production changes, or browser PASS claimed; batch browser test due at 25.


## HQ parallel integration directive — effective from Step 101

User-approved integration work runs alongside Steps 101–150: inventory existing BCC APIs/contracts; connect authenticated read-only data in isolated test environment; validate server/tunnel/monitoring values and UNKNOWN semantics; only then consider lab write paths with security/RBAC/integration QA; after Step 150 prepare staged rollout with backup/rollback and explicit acceptance. No production mutations/cutover, secrets exposure, use of Ashkan, or unapproved paid resources. Browser QA every 10 steps (next 110). Integration completion requires evidence, not UI smoke tests.

### Parallel BCC integration discovery — Steps 101–110
Code search on repository default branch identified `internal/bcc/server.go` API routes and `internal/bcc/dashboard.go` browser client. Confirmed route names: `GET /api/tunnels`, `POST /api/tunnels/cancel`, `/api/tunnels/drift`, `/api/tunnels/rotate-cert`, `/api/tunnels/decommission/plan`, `/api/tunnels/decommission`, `GET /api/change-ledger`. Existing `web/bcc-live-adapter-v3.js` supports same-origin authenticated read-only `GET /api/tunnels`, mapping only explicitly verified coordinates and treating tunnel health as UNKNOWN. This is CONTRACT DISCOVERY ONLY: no authenticated staging API request, backend connectivity validation or production mutation performed. Next integration gate: verify response schema/auth and test against isolated BCC environment.

### Parallel integration discovery — Steps 111–120
Confirmed from `internal/bcc/server.go` that BCC routes include `/api/route-doctor`, `/api/path-graph`, `/api/path-matrix`, `/api/path-probes`, `/api/path-discovery`, `/api/monitoring`, `/api/history`, `/api/health`, `/api/nodes` and `/api/tunnels`. `internal/bcc/route_doctor.go` defines PASS/FAIL/NOT_ASSESSED verdicts and staged evidence; `internal/bcc/pathprobe.go` defines probe classes FULL_DATA/BYTE_CEILING/HANDSHAKE_ONLY/CONNECT_ONLY/TIMEOUT_UNREACHABLE/LOCAL_CONFLICT/UNKNOWN. Discovery is static code review only: no authenticated test BCC available, no live API schema verified, no actual probe executed. Step120 Chrome gate initially caught JavaScript syntax error (render undefined), repaired in f7ff8bd3 and rerun 8/8 smoke passed. Retain initial failure in evidence.

### Integration gate status — Steps 121–130
UI now supports local node selection only; no real deploy, jobs, audit, RBAC or staging API evidence. The backend endpoints `/api/nodes`, `/api/jobs`, `/api/deploy`, `/api/audit`, `/api/change-ledger` were identified earlier in `internal/bcc/server.go`, but method/schema/auth compatibility and staging connectivity still require verification. Step130 Chrome/Puppeteer 8/8 smoke scenarios passed (FA/EN × light/dark × desktop/mobile); this is not backend or selected-node interaction coverage.

### Integration status — Steps 131–140
Security/audit and finance views are local-only. No verified live node finance, immutable audit records, token rotation, kill-switch or write API invocation. Local rate and report date checks do not establish backend authorization, persisted rate versions or report correctness. Step140 Mac Chrome smoke 8/8 passed (FA/EN, light/dark, desktop/mobile), no captured errors/overflow. Independent interaction validation still required.
