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

## Recovery

`recovery.enabled: true` is rejected until the Stage-D recovery contract is implemented. Unsupported behavior must not be silently ignored.

## Routes

Outbound IR Routes provide a local listener and a remote Route ID. Inbound EX Routes provide the fixed target plus an explicit peer allowlist. The wire peer never supplies the target host/port.

Validate with:

```bash
baft config validate --file /etc/baft/ir.yaml
```

Successful validation checks configuration semantics only; it does not prove network reachability or certificate-file availability.
