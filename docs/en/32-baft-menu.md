# 32 — The `baft` menu and the terminal header

HQ A3 (menu and header). Running `baft` on a terminal (or `baft menu`) opens a numbered menu on the server, over plain lines with no raw mode, so it works over SSH and on a phone. It is a front for what already exists. **It is read-only apart from the support bundle, which writes one archive after an explicit yes, and Uninstall (13), which runs the same flow as `baft uninstall` (doc 34): the exact plan first, then separate confirmations (each data class defaults to NO, active tunnels, and typing `uninstall`)**. Anything not built yet is marked `(planned)` and says so when chosen. It never installs, repairs, updates, restarts, adopts or edits anything.

Direct commands (`baft status`, `baft doctor --json`, `baft logs`, `baft version`, ...) are unchanged: **no splash, no escape sequence**, script-friendly. Without a terminal `baft` still prints usage and exits 2, and `baft menu` refuses.

## The menu

| # | Item | What it does |
|---|---|---|
| 1 | Overview / Status | `baft status` |
| 2 | Doctor | run `baft doctor`, or preview the fixes (commands are shown, never run) |
| 3 | Servers | this server's node id, role and agent state; servers are managed in BCC |
| 4 | Tunnels (this node) | a local read-only inventory: BAFT units, their config facts and ownership marker (the same read as discovery, doc 31); BCC does the MANAGED/DRIFTED/... classification |
| 5 | BCC | the BCC service state here and the BCC commands to run on the BCC host |
| 6 | Monitoring | the node's local metrics; fleet monitoring and health are in BCC |
| 7 | Certificates | the CA and certificate files named in the config: subject, expiry, days left (`WARNING` under 30, `FAILED` when expired) |
| 8 | Backup / Restore | backup guidance, read-only `baft-bcc restore-preview`, and the offline local `baft-bcc restore ... --yes` command |
| 9 | Logs | last 100 or 500 lines (follow live with `baft logs -f`) |
| 10 | Support bundle | after an explicit yes, writes the secret-safe diagnostics archive |
| 11 | Update | **planned** |
| 12 | Repair | **planned** |
| 13 | Uninstall | HQ's submenu: 1 binaries only, 2 agent only, 3 BCC only, 4 services + binaries, 5 full uninstall, 6 preview, 0 back; every entry shows the plan first and removes nothing without the confirmations (doc 34) |
| 14 | Advanced | configuration check, release information, agent, network diagnostics, debug information, service information, ownership/drift, local paths, version and build metadata |
| 0 | Exit | also `q`, `exit` or end of input |

Flags: `--file`, `--service`, `--release-state`, `--unit-dir`, `--state-dir`, `--agent-unit`, `--bcc-service`. Every string that comes from the host (hostname, config, unit files) is stripped of control characters before it is printed, and so is what the user types, so no escape sequence can reach the terminal. The Tunnels view uses a systemd adapter that refuses everything except `systemctl is-active`.

The header's **Health** is a fast local verdict (service state and loopback metrics: `HEALTHY`, `DEGRADED`, `DOWN` or `UNKNOWN`), with no remote probe, so the menu opens instantly; `baft doctor` is the full picture.

## The terminal header

The header is an ANSI/Unicode/ASCII interpretation of the official logo, not a pixel copy: a **golden woven B**, black technical cable in its bowls, vertical golden network lines and golden nodes, then `B A F T` and *Resilient Network Fabric*, then Version, Node, Role, Health and Release. It uses no image, font, network or dependency, and no animation (nothing blinks, spins or delays a command).

Palette: primary gold `#D4AF37`, highlight `#FFD76A`, deep gold `#9B6A12`, white/gray/charcoal neutrals; status colors only for status (green healthy, gold warning, red down, gray unknown). The brand itself stays gold.

**Capability detection** before anything is drawn:

| Mode | When |
|---|---|
| `FULL_COLOR` | a terminal with `COLORTERM=truecolor` or `24bit` |
| `256_COLOR` | a terminal whose `TERM` contains `256color` |
| `MONOCHROME` | a terminal without color support, or `NO_COLOR` set (honored fully: no color escapes at all) |
| `PLAIN` | stdout is not a terminal, or `TERM=dumb`: no graphics, two plain lines |

Unicode needs a UTF-8 locale (`LC_ALL`/`LC_CTYPE`/`LANG`); otherwise the whole header falls back to ASCII (`+-|`, `#=.`, `o`). Width comes from the terminal (or `COLUMNS`, else 80): **WIDE** (100 columns or more, large mark beside the title and the context), **NORMAL** (70-99, small mark above them) and **COMPACT** (under 70, two lines for a phone: `BAFT ◈ v0.1.2` and `node · role · HEALTH`). Color is never the only indicator: health is always printed as a word.
