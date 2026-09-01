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

**Do not implement Start/Stop/Subscribe by hand**: the shared
`discovery.Lifecycle` owns that skeleton — subscription bookkeeping,
launch-on-Start for early registrations, stop-everything teardown, and
stopped-state guards (`discovery.ErrStopped`). A provider supplies only a
`NewSubscriptionFunc` that validates one (already-cloned) query and
returns a `SubscriptionRunner` (`Launch(ctx)`/`Stop()`), and its
constructor returns `discovery.NewLifecycle(name, newSub)`. Deliver
snapshots through a `discovery.Emitter` wrapping the handler: it
canonicalizes, suppresses no-change emissions, serializes deliveries, and
guarantees silence after `Stop`. All three in-tree providers are built
this way; the `file` provider remains the smallest end-to-end example.

Semantics your implementation must uphold:

- **One Discoverer per named config entry.** A Discoverer is constructed
  from one entry in the top-level `discovery` config section and owns that
  entry's connection-level resources (API client, resolver, watcher).
  Multiple ALBs share it, so subscriptions must multiplex over one
  connection stack.
- **Full snapshots, never deltas.** Each emission is the complete current
  membership for that query. Consumers diff successive snapshots
  themselves.
- **Emit only on change.** The `Emitter` enforces this (canonicalize +
  compare) when you deliver through it; hand-rolled delivery paths must
  replicate it.
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
- **Lifecycle.** The `Lifecycle` handles subscribe-before/after-Start
  ordering, teardown fan-out, and unsubscribe isolation; your runner's
  obligations are that `Launch` honors its context, `Stop` is idempotent
  and prevents further emission (stop your `Emitter`), and any blocking
  shutdown (e.g. client-go's `factory.Shutdown`) is preceded by canceling
  the context that its goroutines watch.
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
2. **Connection options**, if any, go in their own
   `pkg/discovery/<provider>/options` package, and are referenced from
   [pkg/discovery/options](../../pkg/discovery/options/options.go) as a new
   optional block on `Options` (like `kubernetes:` / `dns:`), with
   `Initialize` defaults and `Validate` rules (reject the block on other
   providers' entries, and other providers' blocks on yours).

   Your options package also owns its `NewErrInvalidOptions(name, detail)`,
   defined in its `options.go` and built from
   [pkg/discovery/errors](../../pkg/discovery/errors/errors.go), so the base
   options package holds no per-provider constructors. That shared leaf
   package exists to break a cycle — `pkg/discovery/options` imports every
   provider's options package, so a provider's options package cannot import
   it back — which is why the error *type* lives outside both. Keep
   `pkg/discovery/errors` free of Trickster imports.
3. **Query fields**: extend `options.Query` only if an existing field
   cannot express your selection, then add a `validate<Provider>` method
   dispatched from `Query.Validate`, enforcing per-provider field usage so
   misplaced fields fail startup. Update the provider documentation in
   [docs/alb-autodiscovery.md](../alb-autodiscovery.md).
4. **Implement** under `pkg/discovery/<provider>` with a constructor
   matching `discovery.NewDiscovererFunc`:
   `func New(name string, o *do.Options) (discovery.Discoverer, error)` —
   typically a small provider struct holding connection state, whose
   `newSubscription` method is handed to `discovery.NewLifecycle`.
5. **Register** it in
   [pkg/discovery/registry](../../pkg/discovery/registry/registry.go)'s
   `SupportedProviders` map.
6. **Test** without network dependencies: an in-process fake of your
   source (client-go fake clientset, `pkg/testutil/dnsserver`, a
   temp dir). Cover: initial snapshot, add/remove transitions, the
   failure-keeps-last-good path, emit-on-change suppression, and
   unsubscribe/Stop termination (no goroutine leaks — `goleak` covers this
   per-package, and `integration/alb_discovery_soak_test.go` catches
   stragglers over hours when run by hand before a release).
7. **Document** the provider in
   [docs/alb-autodiscovery.md](../alb-autodiscovery.md) (config reference
   + worked example) and note any required upstream permissions (as its
   RBAC section does for kubernetes).

Dependencies: keep them lean and justify them in a decision record (see
`trickster-data/decision-dependencies.md` for the pattern); the repo
vendors, so a heavy SDK shows up in every clone.

## Shared machinery

Two packages exist so that providers do not each re-derive them:

- **`pkg/discovery/poller`** — the polling loop: jittered start, immediate
  first iteration, per-iteration cadence chosen by the source (a DNS TTL
  floor, a blocking query's `PollNow`), cancel-safe Start/Stop, and panic
  isolation. A poll-based provider supplies a `poller.Source` and gets the
  rest. `pkg/discovery/poller/http` is the outbound-HTTP source; per-poll
  request shaping (credentials, signing, blocking-query parameters) goes
  through its `RequestDecorator` rather than into the package.

  **Do failure accounting outside the response handler.** A transport or
  credential error returns before any handler runs, so a provider that
  counts failures inside its handler will silently miss exactly the
  failures operators care about. See how `httpsd`'s subscription wraps the
  source's `Poll`.

- **`pkg/discovery/blockingquery`** — the cursor half of HashiCorp's
  blocking query protocol, shared by `consul` and `nomad`. It handles the
  three traps a provider would otherwise rediscover: an index that goes
  backwards means the server's state was reset and the client must start
  over rather than park forever; an index below 1 is reserved; and a
  resource changing faster than the loop needs a floor between requests, or
  "the server does the waiting" becomes a spin against that server at its
  busiest. A provider using it still owns a poll timeout that outlasts the
  server-side wait, since `poller/http` deliberately holds no second
  deadline underneath the iteration context.

  **Testing a blocking-query provider** requires a fake that honors the
  wait parameter, not one that answers immediately — the timeout path is
  the common case for a stable service, and it is also the only way a
  client learns its cursor went backwards, since it cannot learn that while
  parked. `pkg/testutil/blockingquery` provides one; point it at whichever
  cursor header the API uses.

- **`pkg/discovery/memberlist`** — decoders for the two member-list
  documents (native and Prometheus `file_sd`/`http_sd`), shared by the
  `file` and `http_sd` providers.

- **`pkg/bytes.ReadBoundedBody`** — bound every response read. Pass
  `truncate: false` for a document that is only meaningful whole: it fails
  past the limit rather than handing back a fragment, which would parse
  into a plausible but wrong membership.

  `truncate: true` is the *fixed-length prefix* read, not "read a small
  body and don't worry if it's big". It allocates the whole bound and fails
  a short read, so using it for an error document discards the upstream's
  message — which is the one thing that document exists to carry. Use
  `false` with a small limit there.

- **`pkg/secret.Secret`** — any credential string a provider's options
  carry. It redacts itself from YAML, JSON and `String()`, so a config dump
  or the management API cannot emit it.

- **`pkg/aws`** — the AWS credential chain and SigV4 signing, kept outside
  `pkg/discovery` so both the proxy path and the `aws` provider use one
  implementation. It imports nothing from Trickster, which is what keeps
  `pkg/discovery/aws` from cycling through `pkg/backends/options`.

## Which provider to read first

All eleven providers implement the same contract, but they are not equally
good to learn from. Pick by the shape you are building:

| if your source is | read | why |
| --- | --- | --- |
| an HTTP endpoint you poll | [`httpsd`](../../pkg/discovery/httpsd) | the smallest complete poll-based provider; one request, one document |
| event-driven / watch-based | [`file`](../../pkg/discovery/file) | the reference for the notify-plus-poll shape, with no network in the way |
| a blocking-query registry | [`nomad`](../../pkg/discovery/nomad) | smaller than `consul` and uses the same shared cursor |
| a paginated cloud inventory | [`gcp`](../../pkg/discovery/gcp) | one call, one auth mode, no join |
| several calls joined together | [`azure`](../../pkg/discovery/azure) | the hardest shape here: two or three lists joined in memory |
| one provider, several APIs | [`aws`](../../pkg/discovery/aws) | the `serviceLister` split behind an `aws.service` selector |

Start with `httpsd` regardless — every poll-based provider above is that
one plus its own complications.

### The cloud `service` selector

`aws`, `gcp` and `azure` each take a **required** `service` field naming
which API to read (`ec2`/`ecs`, `gce`, `vm`). A new API from a cloud
Trickster already supports is a new `service` value and a new lister, not a
new provider: it inherits the credentials, the poll loop, the options block
and the failure accounting rather than restating them.

The field is required even where only one value exists today. A default
added later can never be removed, so every subsequent service would be
reached by an operator opting out of a value they never chose. Values name
the **product** an operator would recognize rather than the API serving it
— which is why Cloud Load Balancing would arrive as `gcp` `service: gclb`
even though the Compute API serves it alongside `gce`.

## What upstreams actually do

Four behaviors that unit tests against hand-built documents will not teach
you, each found by running against the real thing:

- **An empty list is not always an empty inventory.** Azure answers a list
  against an unregistered resource provider with `HTTP 200` and
  `{"value":[]}`. Nothing distinguishes it from a genuinely empty
  subscription at the list level, so the provider detects the case once and
  logs what to run.
- **A documented parameter may be silently ignored at the wrong scope.**
  Azure's `statusOnly=true` works on the subscription-wide VM list and is
  accepted-then-ignored on the resource-group-scoped one — the same 200,
  minus the data. Assume a parameter you rely on is load-bearing, and
  assert on its effect rather than on the request succeeding.
- **Fields the documentation implies are absent in practice.** Docker's
  `/containers/json` carries no `Health` object at all; GCE omits
  `metadata.items` entirely rather than sending an empty list.
- **Never let a missing signal empty the pool.** If the data a provider
  needs to judge readiness is absent, fail the refresh and keep the
  last-good membership. Treating "unknown" as "not running" drains every
  member with no error to explain it, which is the worst failure a
  discovery provider can have.

Capture a real response as a testdata fixture once you have one, and write
the raw bytes the API sent — not a re-encoding of your own decoded structs,
which only proves those structs are self-consistent. 