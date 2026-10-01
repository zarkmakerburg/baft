# 05 — Configuration and Routes

Configuration schema version 1 is represented by `configs/schema-v1.json` and example IR/EX YAML files.

The strict YAML loader rejects duplicate keys, anchors/aliases, merge keys, custom tags, multiple documents, excessive nesting, files over 1 MiB, and unknown typed fields.

## Dialer peer

```yaml
peer:
  address: 192.0.2.20:443
  server_name: ex.example
  allowed_identity: urn:baft:node:ex-01
```

Dial address, TLS server name, and expected peer identity are intentionally separate.

## TLS

Baseline configuration requires TLS 1.3, explicit CA/certificate/key files, and disabled session tickets. Runtime also checks private-key file permissions.

## Transport

`primary: h2` is the only default transport. H3 remains disabled until its later experimental gate. Shards are limited to 1..8.

## Resource limits

`data_memory_mib`, receive limits, and replay limits bound Stage-C memory behavior. Receive and replay pools do not silently borrow from each other.

`max_flows` caps concurrent Flows for the whole node, shared across every peer and Shard. Past the cap, EX answers OPEN with `OPEN_ERR RESOURCE_EXHAUSTED` without dialing the target, and IR closes the new local connection without sending OPEN. Independently, each Shard accepts at most 64 Flows (`max_flows_per_shard` in HELLO_ACK); the example value `256` equals 4 Shards × 64.

## Recovery

```yaml
recovery:
  enabled: false
  retention_seconds: 30
```

`recovery.enabled: true` turns on same-process ECRL carrier replacement (Step 5.7): when a Shard's carrier fails, the live Session rebinds to a new carrier with epoch fencing and bounded replay instead of ending. It requires `retention_seconds` between 1 and 300; `mode` may be omitted or set to `same_process`.

`durable: true` and any other `mode` are rejected rather than silently ignored: recovery state is not persisted, so resume across a process restart or machine reboot is not supported. See [Known limitations](../../KNOWN-LIMITATIONS.en.md) for the exact scope.

## Revocation (EX)

```yaml
revocation:
  file: /etc/baft/revoked.yaml
```

The optional `revocation` section is accepted only on listeners, because only the listener authenticates the peer of every Carrier; the path must be absolute. The list is read with the same strict YAML (or JSON) parser, and an empty file is an empty list:

```yaml
identities:
  - urn:baft:node:ir-02
serials:
  - "0A:1B:2C"
fingerprints:
  - "<certificate SHA-256, 64 hex digits>"
```

- If `revocation.file` is set but missing or invalid, the node does not start (fail-closed).
- `systemctl reload baft` (SIGHUP) re-reads the file; established Carriers of a newly revoked peer are cut immediately. An invalid file is rejected on reload and the current list is kept.
- Revocation is add-only: removing an entry takes effect on the next restart.
- Serials compare independently of `:`, leading zeros, and letter case. In Noise mode only `identities` apply, because the outer TLS carries no client certificate there.

## Routes

Outbound IR Routes provide a local listener and a remote Route ID. Inbound EX Routes provide the fixed target plus an explicit peer allowlist. The wire peer never supplies the target host/port.

Validate with:

```bash
baft config validate --file /etc/baft/ir.yaml
```

Successful validation checks configuration semantics only; it does not prove network reachability or certificate-file availability.
