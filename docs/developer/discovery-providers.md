# Authoring an Autodiscovery Provider

Autodiscovery providers feed ALB pools (and, in the future, other
subsystems) with live backend membership. This guide covers adding a new
provider against the `pkg/discovery` interface and registry. The `file`
provider ([pkg/discovery/file](../../pkg/discovery/file/file.go)) is the
reference implementation — it is the smallest complete provider and
exercises every part of the contract; read it alongside this guide.

## The contract

A provider implements `discovery.Discoverer`
([pkg/discovery/discovery.go](../../pkg/discovery/discovery.go)):

```go
type Discoverer interface {
    Start(ctx context.Context) error
    Stop() error
    Subscribe(q *options.Query, handler SnapshotHandler) (func(), error)
}
```

Semantics your implementation must uphold:

- **One Discoverer per named config entry.** A Discoverer is constructed
  from one entry in the top-level `discovery` config section and owns that
  entry's connection-level resources (API client, resolver, watcher).
  Multiple ALBs share it, so subscriptions must multiplex over one
  connection stack.
- **Full snapshots, never deltas.** Each emission is the complete current
  membership for that query. Consumers diff successive snapshots
  themselves.
- **Emit only on change.** Canonicalize with `Snapshot.Canonical()` and
  compare with `Snapshot.Equal()` before delivering; redundant emissions
  waste downstream work but are not errors.
- **Keep last-good on failure.** A backend-source outage (API error,
  SERVFAIL, unreadable file) must not emit an empty snapshot — say nothing
  and let the last-good membership keep serving. An *authoritative* empty
  result (the source genuinely reports zero members) is a valid snapshot
  and should be emitted. Log failures once per failure streak, not once
  per retry.
- **Debounce bursts.** Coalesce rapid source events into one emission
  (the kubernetes provider uses 250ms). Per-ALB damping
  (`alb.discovery.debounce_window`) layers on top; your debounce protects
  against event storms, not policy flapping.
- **Lifecycle.** `Subscribe` may be called before or after `Start`
  (pre-Start subscriptions launch when `Start` runs). `Stop` and the
  `Start` context both terminate everything; handlers must never be
  called after `Stop` returns. The returned unsubscribe func must stop
  that subscription's resources without affecting others — give each
  subscription its own context derived from the discoverer's, and make
  sure any blocking shutdown (e.g. client-go's `factory.Shutdown`) is
  preceded by that context's cancellation.
- **Control plane only.** Goroutines, channels and callbacks are fine
  here; nothing in a provider may run on the request hot path. Handlers
  should be treated as non-blocking (the ALB manager just takes a mutex
  and returns), but do not hold your own locks across handler calls.

## Members

Fill `discovery.Member` as completely as your source allows:

- `Name`: the provider-assigned identity seed (pod name, SRV target, file
  entry name). It seeds the generated backend name but is not identity —
  `Key()` (scheme+address+path) is.
- `Address`: `host:port`. Use `net.JoinHostPort` (IPv6 brackets).
- `Weight`: only if your source expresses one (SRV weight, file entry);
  0 means unweighted.
- `Ready`: report readiness only if your source knows it (kubernetes
  conditions). Otherwise `ReadyUnknown` — do not guess `Ready`, because
  `health_mode: provider` trusts you.
- Members your source marks as shutting down (terminating endpoints,
  deletion-stamped pods) should be **omitted** from the snapshot, so they
  drain out of pools before they die.

## Wiring a new provider in

1. **Name it** in [pkg/discovery/providers](../../pkg/discovery/providers/providers.go):
   add the constant and include it in `supported`.
2. **Connection options**, if any, go in
   [pkg/discovery/options](../../pkg/discovery/options/options.go) as a new
   optional block on `Options` (like `kubernetes:` / `dns:`), with
   `Initialize` defaults and `Validate` rules (reject the block on other
   providers' entries, and other providers' blocks on yours).
3. **Query fields**: extend `options.Query` only if an existing field
   cannot express your selection, then add a `validate<Provider>` method
   dispatched from `Query.Validate`, enforcing per-provider field usage so
   misplaced fields fail startup. Update the config-shape table in
   `docs/autodiscovery.md`.
4. **Implement** under `pkg/discovery/<provider>` with a constructor
   matching `discovery.NewDiscovererFunc`:
   `func New(name string, o *do.Options) (discovery.Discoverer, error)`.
5. **Register** it in
   [pkg/discovery/registry](../../pkg/discovery/registry/registry.go)'s
   `SupportedProviders` map.
6. **Test** without network dependencies: an in-process fake of your
   source (client-go fake clientset, an in-process miekg/dns server, a
   temp dir). Cover: initial snapshot, add/remove transitions, the
   failure-keeps-last-good path, emit-on-change suppression, and
   unsubscribe/Stop termination (no goroutine leaks — the shared suite in
   Section F's soak will catch stragglers).
7. **Document** the provider in `docs/autodiscovery.md` (config reference
   + worked example) and note any required upstream permissions (as
   `docs/autodiscovery-rbac.md` does for kubernetes).

Dependencies: keep them lean and justify them in a decision record (see
`trickster-data/decision-dependencies.md` for the pattern); the repo
vendors, so a heavy SDK shows up in every clone.

## Roadmap providers

Candidates from the issue #609 thread, in likely priority order: `consul`
(native API), `ec2`, `gce`, `etcd`, `docker`. Until each lands, its users
are served by:

- the `file` provider — any external SD can emit the member list
  (Prometheus `file_sd`-style) via cron, sidecar, or consul-template; or
- `dns_srv` / `dns_a` — Consul's DNS interface, cloud private DNS zones,
  and Docker's embedded DNS all work today with no new code.
