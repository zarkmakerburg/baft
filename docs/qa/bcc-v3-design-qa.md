# BAFT V3 visual comparison — open

final result: blocked

Reference set: user-provided 03 and 04 (landing), 02 and 06 (dark and light command center), 01 (owner brand). Baseline: committed Mac Chrome captures in this directory at a0a1c39. After screenshots are pending because the Mac remote-control quota is exhausted. No updated visual pass is claimed.

| Screen | Baseline mismatch | Change on ui/bcc-command-center-v3 | Recheck |
| --- | --- | --- | --- |
| Landing 1440 × 1000 | The owner mark sat above centered copy with a rectangular dark plate; the globe was a dim full circle with barely visible routes. | The real mark now anchors a left column, the headline sits beside it, the Earth extends through the right side, and illustrative gold routes render after the dark globe shade. The header brand is on the left, as in the references. | Capture dark and light, FA and EN in Mac Chrome; inspect mark edge and arc intensity. |
| Dashboard 1440 × 1000 | The map was about 440 px across, with an empty details column; server status and chart sat below. | A 590 px map occupies the main column, server list and measured traffic occupy a right rail, and connection details appear only after selection. The sphere radius scales with the larger canvas. | Capture loaded DEMO inventory and a real empty BCC separately; verify menu routes, inspector selection, scrolling and no overflow. |
| Small viewport | The previous single-column layout passed 390 px before the redesign. | The dashboard rail now stacks under the map and the landing mark moves out of the narrow copy column. | Repeat 390 px FA/EN dark/light and reduced-motion checks. |

The reference images depict populated links and numeric telemetry. The authenticated empty BCC must show unknown/empty until link-scoped evidence exists. The disposable 10-node inventory has explicit DEMO aliases and reserved addresses, no agent tokens, and no proven link telemetry. Its current browser presentation still needs a new capture.

Code syntax parsed before each commit; native and visual behavior on the new SHA remain unverified. No production operation, merge, tag or release.

## Evidence checkpoint — 2026-10-09 (source + exact-SHA CI; NOT acceptance)

- Verified PR #159 remains OPEN/DRAFT at `d55b851cf1f2a6add8825eb21db91e1ed9c59be4`; issue #67 remains open. Exact-head GitHub Actions completed SUCCESS: CI `37932630227`, static-analysis `37932630169`, BCC V3 UI contract `37932630159`, r3.1 `37932630487`. These results verify automated workflows, **not** fresh Mac Chrome visual QA or live link telemetry.
- Welcome/Login step 31 asset-path check: commit `d687a550` corrected the static preview's missing image URL to the existing `web/assets/baft-brand.png` already used by the integrated product shell. This only fixes asset loading by source inspection. Owner-approved transparency and final Mac Chrome rendering remain unverified.
- Welcome/Login steps 18 (full browser WCAG/keyboard/screen-reader), 31 (approved brand), 49 (real authenticated sign-in), 50 (final verification) remain OPEN. The 150-step BCC audit remains at 149/150 BLOCKED: current-head Chrome reference comparison, approved enrolled IR/EX lab telemetry, and final visual/localization fidelity remain unverified.
- Safety boundary: documentation-only checkpoint; no operational tunnel/node changes, merge, deploy, tag, release or acceptance assertion.

## Post-merge evidence checkpoint — 2026-10-10

- GitHub PR #159 is now **MERGED** (2026-10-09 20:12:53 UTC), merge commit `f985ec6e3fe2e169f5b7279c3eebf0803a98b312`, from exact UI head `bfb728ce6bcd0f71e7fa8dac624c755a73ea8e0a`. The OPEN/DRAFT observation above is historical, not current. Issue #67 remains OPEN.
- Exact-head GitHub Actions: CI `37981842955` SUCCESS; static-analysis `37981843142` SUCCESS; BCC V3 UI contract `37981842863` SUCCESS; r3.1 `37981842996` SUCCESS. These are automated tests only; they do not prove real Chrome acceptance or live telemetry.
- **Ordered Welcome/Login remaining gates:** step 18 full browser keyboard/screen-reader/accessibility verification; step 31 owner-approved official logo and rendered transparency; step 49 real authenticated sign-in/session check; step 50 final visual and functional acceptance. All remain OPEN/UNVERIFIED. Existing asset-path correction is not logo approval.
- **150-step BCC audit:** remains **149/150 BLOCKED**. Post-redesign Mac Chrome visual/responsive FA/EN dark/light comparisons were not rerun; enrolled IR/EX link telemetry remains unverified. No fabricated PASS or final delivery claim.
- This checkpoint is documentation-only on the named UI branch after PR merge; it is **not** part of the already merged PR and is not automatically on `main`. No tunnel, node, server, deployment, release, tag or additional merge is authorized by this note.
- Reproducible document checks before writing: pre-existing QA checkpoint and blocked acceptance present; new merge SHA and all four workflow run IDs recorded; acceptance gates explicitly remain open.

## Post-merge source-only accessibility checkpoint — 2026-10-10

- Branch comparison: `bfb728ce6bcd0f71e7fa8dac624c755a73ea8e0a...ui/bcc-command-center-v3` is **ahead by one documentation-only commit** (prior checkpoint `76db3bb`); the merged PR #159 does not contain this post-merge note. No new merge is authorized.
- Re-read `web/bcc-welcome-login-v3-preview.html` (blob `898c6b3265f93d184b0a751843bfb84b2985f236`) and `internal/bcc/command_center_v3_test.go` (blob `930eb7d67ad91d38df3b6c87cb59948894481c6d`). **8/8 targeted source-presence assertions passed**: skip-link/target, four dynamically localized accessible labels, reduced-motion scroll behavior, disabled demo credentials/submit, corrected logo URL, and native-test coverage for session/logout, CSRF rejection, and invalid/rate-limited sign-in.
- **Evidence scope:** string/source assertions only. Existing Go test definitions are not proof that they were executed in this checkpoint. No new exact-SHA CI run or Chrome/keyboard/screen-reader session was executed here. The source references a logo asset; owner-approved logo/transparency remains unverified.
- **Ordered gates remain OPEN:** Welcome/Login 18 (browser accessibility), 31 (owner-approved logo), 49 (real interactive authenticated browser session), 50 (final acceptance). BCC 150-step acceptance remains **149/150 BLOCKED** pending fresh FA/EN dark/light Chrome captures and authorized enrolled IR/EX telemetry. No live operations, deployment, release, tag, or merge.


## Accessibility source evidence — 2026-10-10, post-merge UI branch

- Checked `ui/bcc-command-center-v3` at pre-check HEAD `d2aa469d4200b888e761fbd7d5cb6cc90b30db41`, preview blob `0ecf51fb4d9befab6e4a7075f909bc0073174114`. PR #159 is merged to main at `f985ec6e`; this checkpoint is **post-merge branch-only**.
- Targeted static source assertions: **14/14 PASS**. Checked skip-link and focusable `main` target, visible target outline, theme toggle `aria-pressed` initial/dynamic state, language/direction and localized control labels, reduced-motion behavior, existing logo asset path, disabled demo username/password/submit, prevented demo form submission, live status region, and non-production notice.
- Previous source-only accessibility fixes now present in this branch include focusable skip target, theme `aria-pressed`, and visible target focus outline. This checkpoint does **not** establish keyboard/browser/screen-reader behavior, owner-approved logo rendering, actual authenticated login, or any live enrolled IR/EX telemetry.
- **Acceptance remains blocked:** Welcome/Login steps 18, 31, 49, 50 OPEN; BCC audit 149/150 BLOCKED. Exact-head GitHub Actions runs for this post-merge branch were not observed in this checkpoint (the GitHub workflow query returned no matching PR-triggered runs). No live operations, main merge, deploy, tag or release.


## Owner logo approval checkpoint — 2026-10-10

- Owner explicitly approved the BAFT logo in chat. This closes the **owner-design-approval sub-gate** of Welcome/Login step 31; it does not establish rendered-image acceptance.
- Source check on `web/bcc-welcome-login-v3-preview.html` (blob `0ecf51fb4d9befab6e4a7075f909bc0073174114`): header image `id="officialLogo"` references `./assets/baft-brand.png`, with load/error handling and `object-fit:contain`. This is source-only evidence; no browser image-load, transparency, responsive, or visual comparison test was run.
- Step 31 remains **TECHNICAL VERIFICATION PENDING** until actual approved asset identity and browser rendering are checked. Step 18 browser accessibility, step 49 real session, step 50 final acceptance remain OPEN. BCC 150-step audit remains 149/150 BLOCKED. No merge, deployment or operational tunnel changes.


## Logo binary-format audit — 2026-10-10

- **Direct blob verification:** fetched `web/assets/baft-brand.png` from the authorized UI branch as Base64, blob SHA `be3efae9e5fc2d5df6f7a92461a9424e325db7b8`. Its Base64 prefix `/9j/4AAQSkZJRgABAQ` decodes to a JPEG/JFIF header, **not a PNG signature**. Filename extension and actual content disagree. Header check: JPEG PASS; PNG FAIL. This is a binary-signature check, not a visual browser test.
- Preview `web/bcc-welcome-login-v3-preview.html` blob `0ecf51fb4d9befab6e4a7075f909bc0073174114` points `#officialLogo` to `./assets/baft-brand.png`; its image load/error handling is present. Source path check PASS; rendered transparency and exact approved-artwork identity **NOT VERIFIED**.
- **Next safe correction (not executed):** obtain the exact approved artwork as a genuine alpha-capable PNG, replace only this asset after byte-level/visual review, then perform browser responsive, FA/EN, dark/light and keyboard/screen-reader checks. Do not automatically convert the existing JPEG and claim transparency or artwork fidelity.
- **Acceptance gates:** step 18 browser accessibility OPEN; step 31 owner design approved but technical asset/render gate BLOCKED; steps 49 and 50 OPEN. BCC 150-step audit remains 149/150 BLOCKED. PR #159 merged; post-merge UI branch diverged from main (ahead 7, behind 1 at this check). No merge, deploy, tunnel or live-server changes.
