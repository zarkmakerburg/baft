# BAFT BCC — 150-step UI audit

Status: IN PROGRESS; unchecked items are not verified. Draft PR #159 only; no production deployment.

## Navigation and design foundation
1. [ ] IN PROGRESS — inventory sections; backend dashboard sections identified, pending full dynamic verification
2. [x] SOURCE INVENTORY — 31 static `<button>` elements in `internal/bcc/dashboard.go` (dynamic buttons remain in step 8)
3. [x] SOURCE INVENTORY — 29 static `<input>` elements in `internal/bcc/dashboard.go`
4. [x] SOURCE INVENTORY — 10 static `<select>` elements in `internal/bcc/dashboard.go`
5. [ ] TODO — inventory links
6. [ ] TODO — inventory badges
7. [ ] TODO — inventory dialogs
8. [x] SOURCE INVENTORY — 8 dynamic `<button>` templates with `onclick` in `internal/bcc/dashboard.go`; verify generated DOM and permissions separately
9. [ ] TODO — detect dead controls
10. [ ] TODO — navigation map
11. [ ] TODO — color tokens
12. [ ] TODO — spacing tokens
13. [ ] TODO — type scale
14. [ ] TODO — radius tokens
15. [ ] TODO — shadow tokens

## Welcome and login
16. [ ] TODO — welcome layout
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

- Steps **2–4/150**: static source inventory counted from live branch: 31 button tags, 29 input tags, 10 select tags. These are **source-inventory only**, not interaction, browser, accessibility or security PASS. Dynamic controls still need step 8.
- Current phase: **150-step audit, inventory subphase**; 50-step welcome/login acceptance remains open (logo, contrast, browser and auth review).
- Report every subsequent action with exact phase and step number; do not infer completion from commits alone.

- Step **8/150**: source inspection found 8 dynamic button templates in dashboard rendering (node token rotation/revoke, tunnel deploy/plan clear, drift, certificate rotation, decommission/cancel). This is a source inventory, **not** a browser/permission PASS. Counts include repeated decommission templates and exclude any DOM created outside button templates.
