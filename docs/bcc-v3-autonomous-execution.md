# BCC V3 autonomous execution — 2026-10-08

Source branch: ui/bcc-command-center-v3. Baseline: fb7cb64903e762c57089d1c784f551f9b5fff65a.
Authority: #67 PRODUCTIZATION RESET and owner 150-step instruction.

## Acceptance ledger

The earlier audit is retained unchanged. Its preview checkmarks are not product acceptance.
Current accepted checkpoint: 10/150, source commit 2e2851c.
Steps 11–30 and native integration are in progress; later ranges remain OPEN.

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
