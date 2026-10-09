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
