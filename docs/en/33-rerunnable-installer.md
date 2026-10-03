# 33 — The rerunnable installer

HQ A3. `install.sh` can be run again on a server that already has BAFT, and **a rerun is not a reinstall**. It looks first, shows a plan, changes only what the plan lists, checks the result, and rolls back if the check fails.

```
INSPECT -> BUILD PLAN -> SHOW PLAN -> APPLY -> VERIFY -> ROLLBACK IF FAILED
```

## States

| State | Meaning | What a run does |
|---|---|---|
| `FRESH_INSTALL` | nothing of BAFT is here | the normal install; it never asks |
| `ALREADY_INSTALLED` | installed, paired, running | nothing (the release is compared when it is downloaded) |
| `CURRENT` | as above, and identical to the verified release | **nothing at all** |
| `UPGRADE_AVAILABLE` | as above, the verified release is newer | replace the binaries, restart once |
| `REPAIR_REQUIRED` | installed and paired, but stopped, disabled or a permission is off | start/enable/fix permission only |
| `PARTIAL_INSTALL` | an interrupted install, or an EX still waiting for pairing | complete what is missing |
| `BROKEN_INSTALL` | the config does not validate, the Noise key is gone, or an EX has lost its certificates | **refused**; nothing is changed (exit 3) |

## What a rerun never does

A rerun never rotates or regenerates the Noise key, the outer CA and server certificate, the agent token or the BCC job key; never overwrites a valid config; never rewrites an existing systemd unit (so a unit built by the BCC tunnel builder keeps its ownership digests); never deletes a tunnel; never clears state; never resets or lowers the release state (an older release is refused as a downgrade); and never changes ownership or modes of existing directories. A different agent token, job key or release root is a visible `replace` in the plan, never silent.

## Controls

```
sudo bash install.sh --role ex --plan            # inspect and show the plan; no root, no network, writes nothing
sudo bash install.sh --role ex --plan --json     # the same, for scripts
sudo bash install.sh --role ex --yes             # apply a plan that changes an existing install
sudo bash install.sh --role ex --re-pair --yes   # deliberately start a new pairing (config is backed up first)
```

A fresh install never asks. A plan that changes an existing install asks `Apply this plan? [y/N]` on a terminal, and without a terminal needs `--yes` (otherwise it exits 4 and changes nothing). BCC's SSH bootstrap passes `--yes` because the operator already confirmed the bootstrap. `--re-pair` is the only way to replace a valid config, and it interrupts the tunnel until pairing completes.

## Apply, verify, roll back

Before a file is replaced it is copied to `/opt/baft/backups/rerun-<time>/` (the newest three runs are kept). A new binary must run (`baft version`) before any service is touched; after a restart the service must stay up on the same process for a few seconds; the config must validate. Only then is the release state recorded. If any step fails, the previous files are restored, files created by the run are removed, and the service that was running is restarted. A service the run never touched is not restarted. The release state is never recorded for a release that failed verification.

## Idempotency gate (CI)

`tests/e2e/install_two_roles.sh` (release install) reruns both installers **five times** on a healthy EX/IR pair and requires: the same SHA-256 of config, Noise key, certificates, units, release state and binaries; the same owners and modes; the same service process (no restart); traffic still flowing; no pairing or reply code printed; no backup written. It then checks that a stopped service is repaired by a rerun, that a changing plan is refused without `--yes`, that an upgrade keeps every secret and moves the release state forward, that a downgrade is refused, and that upgrades to a release whose binary does not run (or whose service dies) are rolled back with the old binary, config, state and tunnel intact. `tests/installer/plan_test.go` covers the state matrix of `--plan --json` without root or systemd.

## Limits

Source installs (`--from-source`) rebuild the binaries, so a rerun there is an upgrade by definition. `--plan` does not download, so it can only compare with `BAFT_VERSION`; the exact comparison happens after the release is verified. Update and Repair in the `baft` menu stay `(planned)` until a later change wires them to this installer.
