# BCC V3 autonomous execution — 2026-10-08

Source branch: ui/bcc-command-center-v3. Baseline: fb7cb64903e762c57089d1c784f551f9b5fff65a.
Authority: #67 PRODUCTIZATION RESET and owner 150-step instruction.

## Acceptance ledger

The earlier audit is retained unchanged. Its preview checkmarks are not product acceptance.
Current execution checkpoint: 80/150. Checkpoint 20 source: 17be92b.
This counts implemented foundation work, not final visual or operational acceptance.
Later ranges remain OPEN; the entire product has not passed final acceptance.

Completed foundation work: HQ/audit/source inventory; all supplied references inspected; actual
braided BAFT asset bundled; original branch/worktree preserved; shared palettes and navigation;
Mac Chrome smoke for landing/sign-in/nine routes, desktop and emulated mobile.

Implemented, awaiting final acceptance:
- Local NASA night texture, native WebGL sphere with geographic Canvas fallback, gold routes.
- The same V3 assets embedded in the BCC binary at /<secret>/v3/.
- Existing native POST login, error response, session cookie, CSRF and logout reused.
- Existing BCC operational cards reused in the new navigation, with scoped IDs and session
  requests; no bearer token in browser storage.
- Existing node action ID interpolation changed to data attributes to prevent IDs from
  becoming executable inline JavaScript.
- Authenticated GET views for nodes/tunnels/monitoring/audit/certificates/backups/jobs.
- Traffic history derived from verified byte-counter deltas with timestamp checks.
- Full backend operational localization, endpoint-specific workflows and visual fidelity
  are still incomplete. No 150/150 claim is authorized.

## Evidence / attempts

1. Initial native Go test FAIL: failed login still selected the legacy page; corrected.
2. Original JS suite FAIL: old VM fixture omitted document language and expected raw backend
   errors that had already been redacted before this session. Updated fixture and negative
   expectations; subsequent full suite 40/40 PASS.
3. First Chrome native test FAIL: backups unconfigured and logout assertion expected a form
   although the native logout intentionally returns the welcome page.
4. Second Chrome attempt FAIL: route assertion raced asynchronous hash navigation.
5. Corrected native Chrome test PASS: bad/successful native login, session GETs, expected
   unconfigured-backup error, eight viewport/language/theme cells, API 401 after logout,
   captured JS errors=0. This used an isolated loopback BCC and empty disposable database.
6. Full internal/bcc Go suite PASS (218.158s); web package compile PASS.
7. Final source browser check first failed an inventory expectation: 16 top-level cards vs 17 headings; corrected expectation. Mutation/reduced-motion checks remain pending.

Screenshots on the authorized Mac: /tmp/baft-product-landing.png,
 /tmp/baft-product-login.png, /tmp/baft-product-dashboard.png,
 /tmp/baft-product-mobile.png, /tmp/baft-product-auth-dashboard.png.

## Boundaries

No Ashkan; no production server/active tunnel/user state; no merge/tag/release/cost.
The V3 server route is an explicit staging opt-in. Existing default dashboard remains available
during acceptance. This is one repository and one BCC binary, not a disconnected demo.

## Asset provenance

- BAFT brand: owner attachment 1000052161.png, unchanged.
- Earth: NASA Earth Observatory Black Marble 2012, bundled from
  https://eoimages.gsfc.nasa.gov/images/imagerecords/79000/79765/dnb_land_ocean_ice.2012.3600x1800.jpg
- Geographic outlines: johan/world.geo.json countries.geo.json:
  https://raw.githubusercontent.com/johan/world.geo.json/master/countries.geo.json
  Rounded/simplified for local rendering; no server locations inferred from geography.

## Checkpoints 20 and 30

20: native session integration, seven real read-only APIs, eight viewport/language/theme
cells, bad and successful login, logout invalidation and zero browser errors passed.
30: native public header and localized login/error/rate-limit states corrected; local
Vazirmatn variable font bundled with OFL; authenticated pages no longer expose a demo
entry CTA or demo document title. Original operations use isolated selectors and native
CSRF. The first Save Node browser attempt returned 201 but raised ops_q undefined: an
over-broad rewrite had changed a local variable. Removed that rewrite; dynamic selectors
are scoped by fallback lookup. Retest saved a disposable loopback fixture, kept an unsafe
node ID as text, and rejected a write without CSRF (401); browser errors=0.

Chrome source checks: web/qa/bcc-native-chrome.cjs, bcc-local-mutation-chrome.cjs,
bcc-globe-chrome.cjs. Native empty state, seven GETs, eight layout cells, session/logout,
mutation/XSS-negative/CSRF-negative, actual WebGL texture and reduced motion all exercised.
These are Mac Chrome runs in isolated profiles against loopback only. They do not certify
SSH, active tunnels, backup recovery, deployment, or complete visual fidelity.

Font: Vazirmatn variable WOFF2 from the official rastikerdar/vazirmatn repository,
https://raw.githubusercontent.com/rastikerdar/vazirmatn/master/fonts/webfonts/Vazirmatn%5Bwght%5D.woff2
License preserved in web/assets/Vazirmatn-OFL.txt.

Checkpoint 30 repeat evidence: a chained run passed native checks, then timed out waiting
for network-idle navigation. Isolated mutation rerun passed. Another rapid repeat returned
429 on the missing-CSRF negative instead of 401, due to protective request limiting; this
was not counted as PASS. Use fresh disposable state for each mutation suite and wait for
DOMContentLoaded plus the authenticated page mode rather than background network silence.

## Checkpoint 40 — native login

31–40: shared textured Earth art in sign-in; original brand; labeled bounded native fields;
required-field errors; correction clears field errors; show/hide password; correct localized
toggle state; duplicate-submit busy state; localized native bad-login response; four responsive
theme/width cells. Real Mac Chrome bcc-login-chrome.cjs PASS: empty fields sent no POST,
password toggles correctly, native failure remained styled after language change, no overflow
or JS errors. Native full login/logout/API/matrix retest PASS on fresh loopback BCC.
Screenshot /tmp/baft-login-1440-dark.png reviewed against reference composite: brand/globe
and gold-black language are shared; final page proportions and whole-product fidelity OPEN.

## Checkpoint 50 — dashboard integrity and session handling

41–45: oversized native POST has a styled 400/translated error and successful keyboard
login; Go checks 400 and native 429 with Retry-After. 46–50: telemetry chart uses
real timestamps, valid non-resetting byte deltas, Mbps and separate empty/loading/error
states; 401 clears protected DOM and opens native sign-in. Live dashboard now shows
the chart/server list (a native-only hide rule was corrected). Globe selection status
localizes and becomes UNKNOWN with blank measurements after three minutes on the page.

Mac Chrome PASS: form/400/keyboard, GL/reduced-motion/staleness, real session
invalidation with protected data removed, chart deterministic API fixture including
reset/future counter and 503 error, and prior native login/API/matrix. Synthetic chart
fixture is explicitly test-only; no telemetry is claimed live. 40 JS tests and
selected Go access/security tests PASS.

Failures recorded: after Mac automation sessions rotated, an old loopback staging
process held 18772 while the control file referred to a new failed instance; the
404/selector timeout was resolved by verifying/killing only that old disposable
process and starting a fresh one. A history browser check found native dashboardLower
hidden by the legacy-section rule; corrected, rebuilt embedded binary and retested.
A globe QA assertion expected title case though the source English status is uppercase;
corrected the assertion and mapped Persian health statuses explicitly.

Native Chrome replay on a reused staging process reached 429 after logout instead
of the unauthenticated 401. This rate-limit result remains recorded as a failed
run. A fresh isolated BCC session then passed all seven GETs/eight display cells,
logout 401 and the separately labeled deterministic history fixture.

## Checkpoint 60 — measured network overview

Authenticated dashboard now reads protected nodes, tunnels, monitoring and active
alerts. Server/tunnel/alert counts are from separate successful API responses;
failures remain unknown (—), rather than false zero. Added GET-only /api/alerts with
native session access and Go authentication check. The one-server traffic sample is
explicitly labeled and selectable by node; rates derive from actual history deltas.
Changing language/theme no longer refetches the same route repeatedly. Real
monitoring status and latency populate the server list without inventing locations.

Mac Chrome PASS on fresh loopback BCC: authenticated zero counts (0/0/0), empty
traffic, seven read routes, eight viewport/language/theme states, logout 401, no JS
errors; screenshot /tmp/baft-product-auth-dashboard.png reviewed. Deterministic
history fixture PASS with two node options, valid deltas, reset/future rejection
and 503 error distinction. Go access test and 40 JavaScript tests PASS.

Visual discrepancy remains: without verified coordinates or telemetry, live globe
correctly shows no routes and the chart has no measured samples. The reference
images show populated networks; final comparison awaits approved non-production
coordinates/telemetry. No real server or tunnel action was taken.

## Checkpoint 70 — Servers real operations

The four original BCC server panels run inside V3 with native session/CSRF:
registration, SSH enrollment, safe SSH port migration, and cluster nodes.
Registration now blocks missing ID/address before POST, while the backend remains
authoritative. The cluster deploy action is disabled until at least one node is
selected. Dynamic Rotate/Kill labels localize without a global subtree observer.

Real Mac Chrome bcc-servers-chrome.cjs on a fresh loopback fixture PASS:
four visible panels; invalid fields emitted zero POST; registered one synthetic
node (201); no execution from a quote-bearing ID; selection enabled/disabled
deploy; search found the row; host-key scan against 127.0.0.1:1 failed safely
while Install remained disabled; Kill cancellation left the node intact, then
a confirmed fixture-only revoke returned 200 and row became REVOKED. Zero JS
errors. No remote host was contacted. Go V3 test PASS.

A first QA attempt closed Chrome because the test held an unresolved element
Promise; fixed the test handle and repeated on fresh disposable state. Broad
dynamic localization observation was narrowed to inserted server rows. This
does not certify SSH installation or deployment against a real host.

## Checkpoint 80 — guarded server maintenance and tunnel planning

On disposable loopback BCC, real Mac Chrome verified a staged environment-token
rotation (200, secret value never entered into browser), safe invalid SSH migration
plan (400) and Start blocked before a valid plan, followed by fixture revoke (200).
The migration UI no longer leaves an unhandled rejection for a validation/network
error and clears password/key fields after verification attempts. Actual migration
and SSH agent install remain untested without an approved isolated host.

Tunnel browser proof: two disposable EX/IR registrations (201 each), five visible
tunnel operations, plan POST 200 with explicit missing-agent FAIL gates and an
disabled Deploy button, discard action, no tunnel creation POST, Route Doctor
refused an absent active tunnel. No real tunnel was started. Reproducer:
web/qa/bcc-tunnels-chrome.cjs.

## Checkpoint 90 — authenticated, read-only topology

The existing verified-coordinates importer is available in the native Settings
route. It uses the current BCC session to GET tunnels, without asking for an
admin bearer token. Coordinates must be explicitly entered as WGS84 JSON; the
UI does not infer locations from IP addresses. A native import leaves the
monitoring server list intact and never treats a tunnel phase or node metrics
as link-scoped latency/health. The top banner identifies the authenticated BCC
session without claiming that it is a particular staging deployment.

Real Mac Chrome bcc-topology-chrome.cjs PASS: native login, Settings import,
authenticated real GET /api/tunnels returning zero links, zero credential prompts,
then an explicitly intercepted browser fixture with one active-phase tunnel and
two approved coordinates. Keyboard selection showed UNKNOWN health and no RTT;
zero JavaScript exceptions. The fixture is not live evidence. 40 JavaScript tests
PASS. An initial command used a wrong test glob and exited 1 before running the
unit suite; rerunning the correct web/tests/*.test.cjs passed all 40.

Open: no approved isolated agent pair with verified end-to-end link telemetry,
so deployment, decommission, path probes and true link health remain unverified.

## Checkpoint 100 — Monitoring and Security read journeys

The native Monitoring route exposes BCC monitoring and layered health; Security exposes the audit log and finance/report controls. Monitoring KPI placeholders remain unknown until a successful API response. A failed GET clears prior counts and rows. The panel has an explicit refresh control.

Real Mac Chrome bcc-monitor-security-chrome.cjs PASS on a freshly rebuilt disposable BCC: protected monitoring/audit/finance/alerts GET 200, zero writes and JS exceptions. A browser-intercepted 503 cleared stale KPIs to — and rows to empty. A reused staging instance first hit 429; fresh isolated BCC passed. A QA selector was corrected from a nonexistent legacy card ID to routed cards. 40 JS tests PASS.

Open: agent telemetry and alert delivery, chain verification against long-running evidence, finance mutations and exported reports require separate isolated acceptance. The 503 was a test fixture.

## Checkpoint 110 — SSL, Backups, Updates and Settings

Native routes read the protected BCC certificate rotation history, backup inventory and job queue. Backups now offer a selected-file restore preview with the BCC CSRF/session gate, explicit verified/refused outcome and node count comparison. It never restores data. The button is disabled without an eligible listed .baftbak file, and failed reads clear the selection. Settings language and theme controls work across the route shell.

Real Mac Chrome bcc-system-menus-chrome.cjs PASS on freshly rebuilt disposable BCC: SSL, Backups and Updates GET 200 with their native panels; an actually listed disposable backup received restore-preview POST 200, and the rendered verdict matched the response; language and theme changed; no JavaScript exceptions. The preview POST is an audited inspection, not a restore. A first QA assertion wrongly assumed the scheduled staging backup inventory would be empty; it was corrected to handle both empty and populated inventories. 40 JS tests PASS.

Open: BCC currently exposes backup inventory and restore preview, but no authenticated create/apply REST endpoints; do not claim full backup/restore operation. SSL rotation needs enrolled tunnel/agent evidence, and an actual update/deploy needs an approved isolated node. All remain unverified.

## Checkpoint 120 — connected system-menu actions

SSL rotation history links to the existing reviewed tunnel rotation controls; Updates job history links to selected-server deployment controls. These are navigable within the same native command center and introduce no automatic mutation. The Backup inspection selection remains confined to the listed encrypted files.

Real Mac Chrome bcc-system-menus-chrome.cjs PASS after rebuilding the embedded BCC: protected SSL/Backups/Updates reads, actual disposable backup preview with rendered verdict, both contextual menu links navigated to Tunnels/Servers, Settings language/theme switched, and no JS errors. 40 JS tests PASS. There is no claim that a certificate was rotated, a server was deployed or a backup restored.

Open: actual SSL rotation and update deployment need an approved isolated enrolled pair/node; full backup create/apply needs a safely specified and tested authenticated backend workflow.

## Checkpoint 130 — bilingual operations and responsive shell

The embedded native operations now translate more headings, table labels, actions and generated field labels in Persian, normalize mixed-language headings when switching back to English, and update the field's accessible name on language changes. The theme and route shell continue to use shared gold/black tokens and RTL/LTR direction.

Real Mac Chrome bcc-system-menus-chrome.cjs PASS after rebuilding the native BCC: Settings language/theme controls, English and Persian registration labels/headings, SSL/Updates contextual navigation, backup restore preview, and 390 px viewport without document-wide horizontal overflow. 40 JS tests PASS.

Open: several original operational descriptions, prompts, runtime messages and option labels remain English or mixed-language in Persian mode; a full bilingual acceptance audit is required. Other responsive device sizes and final visual reference comparison remain open.

## Checkpoint 140 — BCC session and backup preview access

The native session path uses the BCC secret route, same-origin cookie and CSRF header on the restore inspection request. Real Mac Chrome on disposable BCC verified the listed encrypted backup preview with CSRF returned 200 and rendered the backend's verified/refused verdict. A direct POST without the CSRF header returned 401; the browser held no legacy bearer token in localStorage. The tested action never applied a restore. Targeted Go tests for native session/login states and backup inventory/preview bounds passed; the earlier Chrome system-menu and failure-state journeys remain recorded.

Open: the real agent pair, live link-scoped telemetry, destructive backup apply and update deployment require separate isolated acceptance. No production BCC was contacted.

## Final visual and acceptance gate — OPEN

Mac Chrome 1440 × 1000 captures are committed at docs/qa/bcc-v3-mac-chrome-welcome.png, bcc-v3-mac-chrome-login.png and bcc-v3-mac-chrome-dashboard.png. They were compared with the user-provided gold/black logo, landing/login and command-center references (including the bilingual dark/light composites). The default now opens in the reference dark theme; the globe was enlarged and the programmatic dashboard-heading focus ring removed. Operational static guidance gained additional FA/EN translations. Chrome verified the 1440/390 × FA/EN × dark/light display matrix, seven protected menu reads, logout, backup preview/CSRF, and zero JS errors. 40 JS tests PASS.

The comparison is not a visual sign-off: the current landing globe is a contained sphere rather than the edge-filling reference composition; the supplied raster logo retains a dark rectangular plate in some contexts; the authenticated empty BCC has no verified map positions or measured traffic, whereas the references depict populated live networks. Those illustrated numbers and links cannot be presented as live. Several legacy operation prompts and descriptions remain mixed-language. Full backup apply/create REST workflow, real tunnel/agent actions, certificate rotation and update deployment remain unverified or unavailable. Therefore 150/150 is NOT accepted; final remaining stages are blocked on approved isolated agent/telemetry evidence and further product work. No production server, active tunnel or user data was touched.

## Visual follow-up

A visible Documentation action now opens the actual BAFT docs from the landing hero. Mac Chrome regenerated the committed welcome/login/dashboard captures after the dark default, larger textured globe, focus-ring adjustment and action. The production-shaped dashboard screenshot is intentionally empty because the isolated BCC has no enrolled real nodes. Native operation guidance about discovery, layered health, plan review, path evidence and audit now switches between Persian and English; Mac Chrome verified both. The reference still has a larger, richly connected Earth and a clean cutout brand mark that the current verified-data view does not match. This remains an open final acceptance discrepancy.
