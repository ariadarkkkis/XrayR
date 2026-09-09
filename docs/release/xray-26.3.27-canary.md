# Xray 26.3.27 release qualification and canary runbook

This release embeds Xray `v26.3.27` through Go module `v1.260327.0` and requires
Go 1.26 for reproducible builds. Release qualification creates, but does not
deploy, the Linux amd64 and arm64 release candidates.

Run the complete gate from a clean checkout on a Linux release host:

```sh
sh scripts/qualify-release.sh --container
```

The gate verifies tidy module metadata and checksums, runs the complete
hermetic suite, builds static archives with the release linker flags, exercises
the CLI and no-node configuration startup, and starts the production image.
Successful archives are written to `out/release/` with the existing
`XrayR-linux-64.zip` and `XrayR-linux-arm64-v8a.zip` names. Each archive includes
`RELEASE-METADATA.txt`; the adjacent `.dgst` file contains MD5, SHA-1, SHA-256,
and SHA-512 digests for compatibility with the existing release process.

## Known target-release risks

- VLESS/REALITY combinations have upstream stability reports around this target
  release. Exercise the deployed combination on the canary and roll back on
  handshake, routing, or connection-stability regressions; do not patch the
  embedded core locally.
- Xray no longer exposes Shadowsocks IV-check control. `DisableIVCheck` remains
  parseable but only emits a deprecation warning; it cannot restore the removed
  behavior.
- TLS verification and certificate-pinning behavior follows the target core.
  Legacy configurations rejected by its stricter checks must be corrected or
  rolled back, not bypassed.
- Legacy mKCP header/seed behavior now follows the supported finalmask path.
  Canary any node that relies on non-default mKCP settings before broad rollout.
- Custom outbound endpoint/user cardinality is validated more strictly. A
  validation failure is a configuration migration signal, not permission to
  weaken the validation.
- The static Linux targets are amd64 and arm64 only. Do not substitute an
  artifact from one architecture on the other.

## Canary checklist

### Before deployment

- [ ] Record the canary node, architecture, current config hash, and normal CPU,
  RSS, connection count, traffic, reporting cadence, and error-rate baseline.
- [ ] Download both the candidate and its `.dgst`; verify at least SHA-256 and
  confirm `RELEASE-METADATA.txt` says XrayR `0.9.6-26.3.27`, Xray `v26.3.27`,
  module `v1.260327.0`, and the correct architecture.
- [ ] Run `XrayR version` and compare the canary configuration with the sanitized
  compatibility matrix, especially protocol, transport, TLS/REALITY, fallback,
  socket, and custom outbound settings.
- [ ] Preserve the current binary, config, service definition, and logs. Record
  the SHA-256 of the previous known-good `0.9.6-25.9.11` artifact.
- [ ] Confirm the panel requires no route, credential, schema, or deployment
  change. Do not modify SSPanel as part of this rollout.

### Observe the canary

- [ ] Start one canary node and verify its first full update/report interval:
  node and user fetches succeed, ETag/304 handling remains normal, and no node
  status POST is attempted against SSPanel 2023.3.
- [ ] Exercise every protocol and transport assigned to the node, including
  TLS/REALITY and fallback paths where configured. Confirm new and existing
  connections traverse the intended inbound, outbound, and routing rules.
- [ ] Confirm add/change/remove reconciliation completes without a process
  restart and a failed refresh leaves the last working handler active.
- [ ] Compare panel and node upload/download accounting after a successful
  report. Confirm a deliberately failed report retains counters for retry.
- [ ] Verify online-IP/device-limit, speed-limit, and audit-rejection signals on
  the panel and node logs.
- [ ] Observe CPU, RSS, file descriptors, goroutines, connection count, latency,
  error rate, restarts, and log volume for at least 24 hours and peak traffic.

### Success criteria

- [ ] No panic, restart, handler loss, unexpected authentication request, or
  TLS/REALITY validation or handshake regression occurs.
- [ ] Every scheduled panel fetch/report succeeds apart from an intentionally
  injected failure, and traffic accounting differs from the established panel
  baseline by no more than 1% after timing lag is reconciled.
- [ ] Connection success and latency remain within the node's recorded normal
  envelope; CPU and RSS remain within 20% of baseline at comparable traffic.
- [ ] The canary completes 24 hours including peak traffic before expanding the
  rollout. Any unchecked criterion blocks expansion.

## Rollback checklist

The previous known-good release is `0.9.6-25.9.11`. Before canary deployment,
retain its architecture-matched archive and recorded SHA-256 on the node or in
the approved artifact store; a tag name alone is not a rollback artifact.

Rollback immediately when any of these conditions is met:

- the process cannot start with the previously valid configuration, panics, or
  enters a restart loop;
- two consecutive scheduled panel reconciliation/report cycles fail;
- an active handler disappears, user reconciliation corrupts the last working
  state, or SSPanel 2023.3 receives an unexpected node-status request;
- protocol, routing, TLS/REALITY, fallback, limiter, online-IP, or audit behavior
  fails for a supported canary configuration;
- reconciled traffic accounting differs by more than 1%, or a failed report
  loses counters;
- CPU or RSS exceeds 125% of the comparable baseline for 15 minutes, or latency,
  error rate, connection churn, or file descriptors leave the operator's normal
  alert envelope.

To roll back, stop the canary service, restore the verified architecture-matched
`0.9.6-25.9.11` binary and its saved configuration, restart the service, and
verify one full reconciliation/report cycle plus representative connections.
Preserve candidate logs and metrics for diagnosis. Do not alter SSPanel or apply
an untracked upstream patch during rollback.
