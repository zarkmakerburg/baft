# 31 — Discovery of existing tunnels (report only)

HQ A2. BCC can ask a node what BAFT units, configs and ownership markers it already has and report them. **Discovery is not adoption.** There is no adopt, overwrite, delete, restart, migration, marker creation or config conversion anywhere in this path; every state below is information. Adoption stays on hold; when it comes it must be a separate, explicit, previewable operation with approval.

```
BCC -> signed read-only job (tunnel_discover, no parameters) -> agent -> inspect -> report -> BCC classifies and stores
```

## What the agent reads

Only the node's systemd unit directory and the files its units point to, read-only:

- unit files named `baft*.service` (not `baft-agent*`, which is the agent, and nothing else), at most 16, regular files only (a symlink or a file over 64 KiB is reported as a problem and not followed);
- for a unit whose `ExecStart` is exactly `baft run --file <absolute .yaml/.yml/.json path>`: that config (regular file, at most 1 MiB, loaded with BAFT's own strict loader) and the `baft.managed.json` next to it (at most 16 KiB);
- `systemctl is-active` for each unit; no other command.

Every file is opened **once**, without following a symlink in its final component (`O_NOFOLLOW`, non-blocking so a FIFO cannot hang it; discovery fails closed on a platform without it), and the regular-file and size checks and the bounded read are done on that same descriptor. Swapping the path afterwards, to a symlink or another file, cannot change what is read. The config is parsed (BAFT's own strict decoder, chosen by its extension) from the very bytes that were hashed, never reopened, so a report's digest and facts always describe the same version of the file. Only the final path component is checked: a directory that is itself a symlink is followed, as an administrator's own layout.

It reports facts, not contents: the unit and config SHA-256, service state, the unit's `# baft-tunnel:` header, the config's role, listen, peer, route and target, and the marker's own fields. File contents, `Environment=` lines, keys and secrets never leave the node. The report is bounded (48 KiB, long values clipped, units capped), carries no timestamp and is byte-identical for an unchanged node.

## States (classified by BCC from its own inventory)

| State | Meaning |
|---|---|
| `MANAGED` | the primary unit's marker is BAFT's, names an **active** tunnel of this node with the right role, and the live files equal the digests **BCC itself verified** (the same rules as drift detection) |
| `DRIFTED` | same owner on both sides, but the files, service or route facts changed since |
| `MISSING` | BCC records an active tunnel on the node, but there is no unit file for the node's own service |
| `DISCOVERED_UNMANAGED` | a BAFT unit with a valid config and **no** ownership marker: not created or claimed by BAFT |
| `OWNERSHIP_CONFLICT` | contradictory claims: a marker claimed by someone else; a marker naming a tunnel BCC does not know, a tunnel of other nodes, or one BCC records as superseded or rolled back; a role that contradicts BCC; a unit header that names another tunnel than the marker; a unit that says it is BAFT-managed but has no marker; BCC expects the primary unit to be BAFT's but its files carry no marker; a second unit sharing the marker |
| `UNKNOWN` | not understood, unreadable, not provable, or the tunnel is being changed by BCC right now (nothing is concluded until it settles); a primary unit that exists but cannot be read is UNKNOWN, never MISSING; no verified digests in BCC means never MANAGED |

A marker is only a claim; `MANAGED` needs BCC's own records to agree. Whatever the state, discovery does nothing to the artifact: `DISCOVERED_UNMANAGED`, `OWNERSHIP_CONFLICT` and `UNKNOWN` are never touched, and uninstall (when it exists) may only remove what is proven BAFT-owned.

## Where it is visible

- `POST /api/discovery?node=<id>` or `?all=1` (admin) queues the read-only job; the call takes no path or other input, one discovery per node at a time, revoked nodes are not asked. `GET /api/discovery[?node=<id>]` (admin) returns the stored inventory per node: instances with state and reasons, summary counts, problems.
- The dashboard card **Existing tunnels (discovery)**.
- Audit log: `discovery.start` per request and `discovery.completed` per finished discovery, with the summary, the state of each unit, whether it changed since the last discovery, and any problem (a failed job, an unreadable report or a node that does not answer within 10 minutes is a recorded problem, not a finding).
- The inventory is part of the BCC state (schema migration 4, table `node_discovery`) and of its backups.

## Limits

Only BAFT units named `baft*.service` in the node's unit directory are found. A transport started some other way (a container, a script, a differently named unit) is not discovered, and that absence says nothing about it. The ownership marker lives next to a config, so two units that share a config directory share a marker; the second one is reported as a conflict.
