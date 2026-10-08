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
17. [ ] TODO — original logo asset
18. [ ] TODO — logo fallback
19. [ ] TODO — hero readability
20. [ ] TODO — illustration
21. [ ] TODO — feature cards
22. [ ] TODO — entry CTA
23. [ ] TODO — anchor focus
24. [ ] TODO — sign-in heading
25. [ ] TODO — preview gate
26. [ ] TODO — auth routes
27. [ ] TODO — session safety
28. [ ] TODO — CSRF safety
29. [ ] TODO — rate limits
30. [ ] TODO — login integration gate

## Localization and RTL
31. [ ] TODO — Persian strings
32. [ ] TODO — English strings
33. [ ] TODO — RTL
34. [ ] TODO — LTR
35. [ ] TODO — logical properties
36. [ ] TODO — date formats
37. [ ] TODO — number formats
38. [ ] TODO — IP text direction
39. [ ] TODO — status labels
40. [ ] TODO — error labels
41. [ ] TODO — button labels
42. [ ] TODO — tooltips
43. [ ] TODO — screen reader language
44. [ ] TODO — language preference
45. [ ] TODO — missing translations

## Theme and accessibility
46. [ ] TODO — navy gold
47. [ ] TODO — light palette
48. [ ] TODO — dark palette
49. [ ] TODO — success color
50. [ ] TODO — warning color
51. [ ] TODO — error color
52. [ ] TODO — unknown color
53. [ ] TODO — normal contrast
54. [ ] TODO — small text contrast
55. [ ] TODO — control contrast
56. [ ] TODO — focus rings
57. [ ] TODO — disabled states
58. [ ] TODO — hover states
59. [ ] TODO — pressed states
60. [ ] TODO — reduced motion

## Monitoring and history
61. [ ] TODO — monitor header
62. [ ] TODO — node KPI
63. [ ] TODO — UP KPI
64. [ ] TODO — DOWN KPI
65. [ ] TODO — UNKNOWN KPI
66. [ ] TODO — monitor table
67. [ ] TODO — noise RTT
68. [ ] TODO — health latency
69. [ ] TODO — error rate
70. [ ] TODO — last seen
71. [ ] TODO — sessions
72. [ ] TODO — routes
73. [ ] TODO — history selector
74. [ ] TODO — history chart
75. [ ] TODO — history legend

## Nodes and SSH
76. [ ] TODO — node ID
77. [ ] TODO — alias
78. [ ] TODO — management address
79. [ ] TODO — IPv4
80. [ ] TODO — IPv6
81. [ ] TODO — role
82. [ ] TODO — public key
83. [ ] TODO — agent token
84. [ ] TODO — save node
85. [ ] TODO — SSH host
86. [ ] TODO — SSH port
87. [ ] TODO — SSH user
88. [ ] TODO — host key scan
89. [ ] TODO — fingerprint verification
90. [ ] TODO — agent install

## Tunnels and discovery
91. [ ] TODO — existing tunnels
92. [ ] TODO — discover all
93. [ ] TODO — discovery refresh
94. [ ] TODO — layered health
95. [ ] TODO — health refresh
96. [ ] TODO — tunnel table
97. [ ] TODO — tunnel refresh
98. [ ] TODO — plan preview
99. [ ] TODO — plan confirmation
100. [ ] TODO — deploy
101. [ ] TODO — cancel
102. [ ] TODO — drift check
103. [ ] TODO — cert rotation
104. [ ] TODO — decommission
105. [ ] TODO — retry decommission

## Connectivity and recovery
106. [ ] TODO — change history
107. [ ] TODO — history refresh
108. [ ] TODO — SSH migration plan
109. [ ] TODO — dual listen
110. [ ] TODO — access verification
111. [ ] TODO — route doctor
112. [ ] TODO — diagnosis result
113. [ ] TODO — path graph refresh
114. [ ] TODO — path graph
115. [ ] TODO — connectivity matrix
116. [ ] TODO — pair probe
117. [ ] TODO — probe result
118. [ ] TODO — stale data
119. [ ] TODO — error retry
120. [ ] TODO — empty state

## Cluster and audit
121. [ ] TODO — cluster nodes
122. [ ] TODO — selected deployment
123. [ ] TODO — selection state
124. [ ] TODO — preflight
125. [ ] TODO — deploy confirmation
126. [ ] TODO — recent jobs
127. [ ] TODO — job progress
128. [ ] TODO — job failure
129. [ ] TODO — immutable audit
130. [ ] TODO — audit refresh
131. [ ] TODO — audit readability
132. [ ] TODO — audit pagination
133. [ ] TODO — token rotation
134. [ ] TODO — kill switch warning
135. [ ] TODO — destructive confirmation

## Finance and reports
136. [ ] TODO — node finance
137. [ ] TODO — rate input
138. [ ] TODO — versioned rate
139. [ ] TODO — report dates
140. [ ] TODO — date validation
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
