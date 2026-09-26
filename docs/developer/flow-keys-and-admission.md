# Flow Keys and Admission

Features that sit in front of the backends and act per client, such as
load-balancer affinity, access control, rate limiting and session
persistence, share five building blocks. This guide describes each one as
it exists in the tree, the rules that come with it, and where a new
feature plugs in:

- [Flow keys](#flow-keys) (`pkg/proxy/flowkey`) read the part of a request
  or flow that a feature keys on, such as the client address, a header or
  the TLS server name, and hash it.
- [Per-key tables](#per-key-tables) (`pkg/util/keytable`) keep a value for
  each flow key, bounded in size and expiring on their own.
- [Stream admission](#stream-admission) (`l4.Admission`) judges
  connections, UDP flows and datagrams on `tcp`, `tls` and `udp` listeners
  before anything is relayed.
- [The HTTP listener seam](#the-http-listener-seam) (`wrapListener`) runs
  middleware for a whole HTTP listener, ahead of routing.
- [The `none` reference](#the-none-reference) (`reserved.ReferenceNone`)
  lets a path clear something it would otherwise inherit from its backend.

A feature that adds a top-level configuration section should also follow
[Adding a New Configuration Value](adding-new-config.md).

| Listener | Where a feature judges traffic | Keys it can read |
|---|---|---|
| `http`, including `mgmt` and `metrics` | `wrapListener` for the whole listener; the route chain in `pkg/routing` for a backend or path | `KeySource.OnHTTP()` |
| `tcp`, `tls` | `Admission.Peer`, then `Admission.Flow` | `KeySource.OnStream(...)` |
| `udp` | `Admission.Peer` for each new flow, `Admission.Datagram` for each datagram | `KeySource.OnStream(...)` |
| native (`mysql`, `postgres`, `clickhouse`, `flight-sql`) | no hook yet | `KeySource.OnNative()` |

## Flow keys

The ALB's `hrw` strategy uses flow keys on every plane today, and the
Kubernetes controller uses the listener checks below to leave out a
`loadBalancingKey` that a route's listener cannot read.

### Key sources

A key source is a spelling in configuration.
`flowkey.ParseKeySource` ([keysource.go](../../pkg/proxy/flowkey/keysource.go))
turns it into a `KeySource`: a `Kind`, plus `Name` for a header, cookie or
query parameter, or `TLV` for a PROXY protocol TLV.

| Spelling | `KeyKind` | Reads | HTTP | Stream | Native |
|---|---|---|---|---|---|
| `client_ip`, or empty | `KeyClientIP` | the client address, never its port | yes | yes | yes |
| `host` | `KeyHost` | the request host without its port, ignoring case | yes | | |
| `header:<name>` | `KeyHeader` | the first value of the header | yes | | |
| `cookie:<name>` | `KeyCookie` | the cookie's value, without surrounding quotes | yes | | |
| `query:<name>` | `KeyQuery` | the parameter's value as sent, not unescaped | yes | | |
| `method` | `KeyMethod` | the request method | yes | | |
| `path` | `KeyPath` | the request path as decoded, without its query string | yes | | |
| `query` | `KeyRawQuery` | the whole query string as sent | yes | | |
| `sni` | `KeySNI` | the TLS server name the client offered, ignoring case | | `tls` | |
| `proxy_tlv:<type>` | `KeyProxyTLV` | the first PROXY protocol v2 TLV of that type | | `tcp` or `tls` with `proxy_protocol` | |
| `user` | `KeyUser` | the name a session authenticated as | | | yes |

Parsing rules:

- A name may not be empty or contain a space, a tab, `;`, `=`, `&` or `,`.
  Header names are canonicalized (`x-tenant` becomes `X-Tenant`); cookie
  and query parameter names match exactly.
- A TLV type is one byte, parsed as a Go integer literal: decimal, or hex
  with `0x` as in `proxy_tlv:0xEA`. A leading `0` makes it octal, so
  `proxy_tlv:010` is type 8.
- Anything else fails with an error that wraps
  `flowkey.ErrInvalidKeySource` and lists the valid spellings.

Three methods say which listeners can read a source. Validation must use
them, so that a configured key can never be unreadable at run time:

- `OnHTTP()`: every source but `sni`, `proxy_tlv:<type>` and `user`.
- `OnStream(flowkey.StreamListener{TLS: ..., ProxyProtocol: ...})`:
  `client_ip` always, `sni` when `TLS` is set, and `proxy_tlv:<type>` when
  `ProxyProtocol` is set. A `udp` listener never reads a PROXY header, but
  `proxy_protocol: true` on one passes validation and is ignored, so pass
  `ProxyProtocol` as false for `udp`, as the snippet below does.
- `OnNative()`: `client_ip` and `user`.

The model to copy is the check of an ALB's `hrw.key` against each stream
listener that serves it, in `pkg/config/validate/validate.go`, where
`listener` is `pkg/config/listener`:

```go
if !o.HRW.KeySource.OnStream(flowkey.StreamListener{
	TLS:           options.Protocol == listener.ProtocolTLS,
	ProxyProtocol: options.ProxyProtocol && options.Protocol != listener.ProtocolUDP,
}) {
	return fmt.Errorf(...)
}
```

A feature may accept fewer sources than `ParseKeySource` does.
`FollowsClient()` is false for `method`, `path` and `query`, which describe
the shape of a request rather than a client. The ALB refuses those for
`hrw.key` (`ParseHRWKey` in `pkg/backends/alb/options/keysource.go`) and for
`sticky.table.key`, since no client's affinity can follow them. Check the
same way when some kinds make no sense for a feature.

### What a key requires

Whether a listener can read a key says where the key can be read.
`KeySource.Requires()` says when: what a request must have been through
before the key has a value.

```go
type Requirement uint8

const (
	RequiresPrincipal Requirement = 1 << iota // the identity an authenticator established
	RequiresBody                              // the request body, buffered and bounded
)
```

- A kind read from the principal declares `RequiresPrincipal`, and one read
  from the request body declares `RequiresBody`; every other kind requires
  nothing. Today `user` is the only kind that requires anything, and only
  native listeners read it, once their session has authenticated.
- `Requirement.Has` tests a set, as `lb.Needs.Has` does.

Where a feature sits decides which keys it may use. Validation should refuse
the others, just as it refuses a key that a listener cannot read:

- **Listener scope** reads only keys that require nothing. That covers
  [the HTTP listener seam](#the-http-listener-seam) and
  [stream admission](#stream-admission), which run before any authenticator
  or body filter.
- **Route scope,** in the route chain that `pkg/routing` builds, reads a
  principal key only inside `attachAuthenticator`, and a body key only
  inside `bodyfilter`. A feature placed ahead of the authenticator, for
  example to turn away an unauthenticated flood cheaply, may key only on
  what requires nothing.
- **Not every route has a body filter.**
  - Every route of a backend applies `attachAuthenticator`.
  - Only the routes on a listener apply `bodyfilter`, and only to POST, PUT
    and PATCH requests.
  - A backend's own route has no body filter. `registerPathRoutes` gives
    `applyMiddleware` no frontend options for it. That own route is what an
    ALB pool, a rule's `next_route` and the ClickHouse native bridge
    dispatch into. The bridge adds a filter only when its listener sets
    `max_request_body_size_bytes`.
- **The ALB's pick** therefore always runs inside the `attachAuthenticator`
  of the route that reached it. So a strategy that the ALB keys, such as
  `hrw`, sees the principal whenever that route has an authenticator. The
  pick is inside a body filter only when some route the request passed
  through applied one. A nested ALB, or one behind a rule, has only the
  filter of the route the request entered by, if that route had one. With
  `truncate_request_body_too_large` set, that filter may also have cut the
  body short.
- **No kind reads the body yet.** Before one does, either give a backend's
  own route the body filter or have the key bound its own read, and state
  the rule here to match.

### Extractors

Build an extractor once, when a configuration is loaded or swapped, and
call it for each request or flow. The functions it returns do not
allocate.

```go
func HTTP(ks KeySource, v6Prefix int) func(*http.Request) Value
func Stream(ks KeySource, v6Prefix int) func(flow.Flow) Value
func StreamValue(ks KeySource, v6Prefix int, f flow.Flow) Value

type Value struct {
	Hash uint64
	OK   bool
}
```

- **`OK` is false when there was nothing to key on:** a missing header,
  cookie, parameter or TLV, an empty value, or no server name. `Hash` is
  then zero. Each feature decides what a miss means: `hrw` spreads such a
  request at random, while a rate limiter might put every request that
  lacks the key in one shared bucket, or exempt them.
- **A source a listener cannot read is a miss, never a fallback.**
  `HTTP` for `sni` and `StreamValue` for `header:<name>` always return
  `Value{}`; neither falls back to the client address. Validation has to
  keep such a key out of the configuration.
- **Convert at the call site.** `flowkey` never returns a feature's own
  type. The ALB converts with `lb.Flow{Key: v.Hash, HasKey: v.OK}`.
- **`StreamValue`** is the unbound form, for a caller whose source varies
  by flow and must not allocate a closure per call. The ALB's stream
  adapter uses it because it resolves the source per nesting level at pick
  time.
- **The client address.** On HTTP it is the address the listener's
  `trusted_proxies` resolved from the forwarding headers (see
  [the HTTP listener seam](#the-http-listener-seam)), or else the peer's.
  On a stream listener it is `Flow.Client` (see
  [what each stage finds](#what-each-stage-finds)). An IPv4-mapped IPv6
  address keys as its IPv4 form. An IPv6 address is first masked to
  `v6Prefix` bits, since privacy addressing rotates a client's low bits.
  Any value outside 1 to 127 keys the whole address, so pass
  `flowkey.DefaultIPv6Prefix` (64) unless the feature makes the prefix
  configurable.
- **`HTTPResponse`** reads a key from a response's headers: the value an
  upstream hands a client to send back. See
  [keys learned from a response](#keys-learned-from-a-response).
- **`Cookie(h http.Header, name string) (string, bool)`** returns the raw
  value of a request cookie, quotes trimmed, without allocating. It is the
  parser behind `cookie:<name>`. Use it when a feature needs the value
  itself rather than its hash, as the ALB does to read its sticky token.
- **`user`** is read and hashed (`lb.HashString` of the name) by the
  native ALB adapter (`pkg/backends/alb/native`), because it comes from a
  native session's route input, which `flowkey` may not import. That
  adapter keys every other kind on the client address. The spelling and
  the kind still belong to `flowkey`.

### Keys learned from a response

```go
func HTTPResponse(ks KeySource) func(http.Header) Value
```

An upstream often creates a session in its response, and the client first
sends the value back on its next request. The ALB's `sticky.table.learn:
response` stores a pin under that value as the response is written, so
the next request finds it.

- **What it reads.** `OnHTTPResponse()` is true only for `header:<name>`
  and `cookie:<name>`, and `HTTPResponse` reads only those; any other kind
  is a miss.
  - For `header:<name>`, it reads the first value of the response header
    of that name.
  - For `cookie:<name>`, it reads the value that the last `Set-Cookie` for
    that name sets. Whitespace is trimmed, as a browser trims it, and then
    quotes, as `HTTP` trims them.
  - A `Set-Cookie` whose last `Max-Age` is 0 or less deletes the cookie, so
    it sets nothing.
- **Both sides must agree.** The key read from a response must equal the
  key `HTTP` reads from the request that sends the value back. The golden
  test pins that, and a fuzz test checks it for every value a browser
  returns unchanged.
- It does not allocate, and like `HTTP` it is built once per configuration.

### Composite keys

```go
func HTTPComposite(sources []KeySource, v6Prefix int) func(*http.Request) Composite
func StreamComposite(sources []KeySource, v6Prefix int) func(flow.Flow) Composite

type Composite struct {
	Value
	Present int
}
```

Parts fold in order, starting from zero: `h = lb.Mix(h ^ part)`. So:

- Order matters: `[client_ip, header:X-Tenant]` and its reverse are
  different keys.
- A missing part folds as zero, so requests that lack the same parts share
  a key. `OK` is set only when every part was present, and `Present`
  counts the parts that were. A feature can give requests that lack a
  part one shared bucket (key on `Hash` whatever `OK` says) or skip them
  (require `OK`).
- A composite of one part is not the bare key, since it is mixed once
  more. An empty composite is never `OK`.
- A composite owns its sources, so the caller may reuse its slice.

### Matching addresses

Flow keys are hashes. They group requests and flows, but they cannot be
compared with an address range, so a feature that matches addresses, such
as an IP access list, reads the address itself:

- On HTTP, `request.ClientIP(r)` (`pkg/proxy/request`) returns the
  resolved client address as a string. Parse it with `netip.ParseAddr` and
  `Unmap` it, and decide what an address that does not parse means.
- On a stream listener, `Flow.Client` is a `netip.AddrPort`, already
  unmapped. It is the zero value when a PROXY header named a source that
  is not an IP address, so check `IsValid`.
- `clientip.Trusted` (`pkg/proxy/clientip`), built by `ParseTrusted`, is
  an existing set of addresses and prefixes whose `Contains` unmaps before
  it matches.

### Hashing and the golden test

Keys are hashed by `pkg/lb/key.go`: 64-bit FNV-1a over the bytes
(`HashString`, `HashBytes`, `HashFold` for names compared without regard
to case, `HashAddr` for addresses), finished with `Mix`, a 64-bit
finalizer. Nothing is seeded, so every replica and every restart computes
the same key for the same input. That is what lets replicas behind one
front door agree on where a key belongs. Never add a seed, or switch
hashes, for keys that must agree.

`pkg/proxy/flowkey/golden_test.go` pins the hash of every kind that
`flowkey` reads, for fixed inputs: the HTTP client address in IPv4, in
IPv6 masked to /64 and resolved through a trusted proxy; host, header,
cookie, query parameter, method, path and query string; the header and
cookie keys learned from a response, which must equal the request's; the
stream client address in IPv4 and IPv6; the server name; and a PROXY
protocol TLV.
`user`, which the native adapter hashes, is not pinned there. A change
that moves any pinned key fails the test. Pin every kind that `flowkey`
reads, and never edit a pinned value to make a refactor pass.

### Adding a key kind

1. Append a `KeyKind` constant and a spelling constant (or a prefix such as
   `header:`), add a case to `ParseKeySource`, and add the spelling to its
   error message.
2. Decide which listeners can read it. `OnStream` and `OnNative` name what
   they allow, but `OnHTTP` names what it excludes, so exclude the new
   kind there if HTTP cannot read it. The native ALB adapter keys every
   kind but `user` on the client address, so leave a new kind out of
   `OnNative` unless that adapter learns to read it.
3. Declare in `Requires` whether it is read from the principal or from the
   request body.
4. Read it in `HTTP`, `StreamValue` or both, without allocating. An empty
   value is `Value{}`. If a response can set it, read it in `HTTPResponse`
   too, and include it in `OnHTTPResponse`.
5. Pin its hash in `golden_test.go`, and cover its parsing, its
   requirements and each listener type in the package's tests.
6. `FollowsClient` names what it excludes, so `hrw.key` and
   `sticky.table.key` accept the new kind unless it describes the shape of
   a request and is added there. If they accept it, document it in
   [the ALB guide](../alb.md).

### Import boundary

`flowkey` directly imports only `pkg/lb`, `pkg/proxy/l4/flow`,
`pkg/proxy/context` and `pkg/proxy/headers`. Its `boundary_test.go` checks
those direct imports: it forbids `pkg/backends`, `pkg/kube` and
`pkg/proxy/l4`, with their subpackages, except `pkg/proxy/l4/flow`.

The relay rule matters beyond this package. The relay's tests import the
logging and listener packages, and through them depend on `pkg/config`,
the ALB options, `flowkey`, and much of `pkg/backends` and `pkg/cache`.
Any package in that graph that imports `pkg/proxy/l4` creates an import
cycle in the relay's test package. `go build` does not notice; `go vet`,
`go test` and `make lint` do, and `go list -deps -test ./pkg/proxy/l4`
lists the graph. That is why `l4.Flow` and `l4.ProxyHeader` are aliases
of `flow.Flow` and `flow.ProxyHeader`, which live in the leaf package
`pkg/proxy/l4/flow`.

The same rule binds a new feature: **no package that `pkg/config` depends
on may import `pkg/proxy/l4`.** A feature's options package is imported by
`pkg/config` once it is a configuration section. Its `l4.Admission`
implementation has to return `l4.Verdict`, so it belongs in a separate
package that only the daemon's wiring imports.

## Per-key tables

`keytable.Table[V]` ([keytable.go](../../pkg/util/keytable/keytable.go))
holds one value per flow key. The ALB's sticky table mode keeps a member
per key in one. A rate limiter can keep a counter per key in another.

```go
type Options struct {
	TTL            time.Duration // an entry expires this long after it is stored; 0 never
	Idle           time.Duration // an entry expires once unread this long; 0 never
	MaxEntries     int           // default 100,000
	RefuseWhenFull bool          // refuse a new key when full, rather than drop an entry for it
}

func New[V any](o Options) *Table[V]
func (t *Table[V]) Get(key uint64, now int64) (V, bool)
func (t *Table[V]) GetOrPut(key uint64, v V, now int64) (V, bool)
func (t *Table[V]) Put(key uint64, v V, now int64) bool
func (t *Table[V]) Len() int
```

- **Keys** are `Value.Hash`. Decide first what a miss (`OK` false) means,
  since a table has no notion of one.
- **Time.** `now` is in nanoseconds, from the same clock on every call to a
  table. `Get` and `GetOrPut` mark an entry read, which is what `Idle`
  measures from. A hot entry's last-read time is written at most once a
  second, or once per sixteenth of `Idle` when that is shorter. So an
  entry can expire that much sooner than `Idle` after its last read. Add
  that margin to `Idle` when an entry must outlive it.
- **Creating a value.** Call `Get` first. It takes only a read lock and
  does not allocate. On a miss, build the value and call `GetOrPut`, which
  returns the value some other caller stored in the meantime, if any.
  Every caller racing to create a key's value then gets the same one. With
  `Get` followed by `Put`, the last caller's value would replace the
  others, and whatever they recorded in theirs would be lost.
- **Values are copied** in and out. A value that must change in place, such
  as a counter, should be a pointer that the feature synchronizes itself.
- **Bounded.** The table is split into up to 64 separately locked shards,
  each holding its share of `MaxEntries`. A table is full when the shard a
  new key falls in is full, so it can refuse a key a little before it
  holds `MaxEntries` in all. A new key in a full shard first drops the
  expired entries among a sample of 8 of the shard's entries. If none has
  expired:
  - by default, the least recently read entry of the sample is dropped to
    make room;
  - with `RefuseWhenFull`, the key is refused instead: `GetOrPut` and `Put`
    report false. Use this when losing a live entry would be wrong, as
    losing a counter resets it. The feature decides what a refused key
    gets, and `Len` against `MaxEntries` is its gauge.
- **No goroutine.** An expired entry is skipped when read. Each shard
  sweeps out its expired entries once every 256 new keys that arrive in
  it, refused ones included, so a full table reclaims space even under a
  flood of new keys.
- **Cost.** A `Get` hit costs about 30 ns. `GetOrPut` and `Put` take the
  shard's write lock and allocate one entry when they store.
- **Reloads.** A table lives as long as whoever holds it. To keep entries
  across a config reload, keep a registry of tables by name, and hand the
  old table to the new configuration when the options that shape it are
  unchanged. `sticky.TableFor` in `pkg/backends/alb/sticky` does this for
  the ALB, as `pkg/backends/alb/statscarry.go` does for member stats.

## Stream admission

### The contract

From [pkg/proxy/l4/admission.go](../../pkg/proxy/l4/admission.go):

```go
type Verdict uint8

const (
	Allow Verdict = iota
	Drop
	Reject
)

type Admission interface {
	Peer(f Flow) Verdict
	Flow(f Flow) Verdict
	Datagram(f Flow, size int) Verdict
	Datagrams() bool
}

// Holder is optional; see UDP specifics below.
type Holder interface {
	Hold(f Flow) time.Duration
}
```

`l4.Config.Admission` holds one admission per listener; nil admits
everything. The daemon builds a new `l4.Config` for each stream listener in
`streamConfig` (`pkg/daemon/setup/listeners.go`), at startup and on every
reload, and swaps it in with `Update`. A feature sets `Admission` there;
nothing sets it yet. Two consequences:

- **An admission is rebuilt on every reload.** State that must outlive a
  reload, such as a limiter's counters, belongs in a registry outside it.
- **There is one slot.** Features that both judge a listener must be
  combined into one admission: access control first, so a denied client
  costs the limiter nothing; `Datagrams()` true if any part needs it; and
  `Holder` implemented by the combination itself if any part implements
  it, since the relay asks only the admission it holds. `Hold` is not told
  which part denied the flow, so the combination has to remember or work
  it out again.

### Stages

| Stage | `tcp` | `tls` | `udp` |
|---|---|---|---|
| `Peer` | once per connection, before any byte is relayed | the same, before the ClientHello is read, so there is no `ServerName` | once per new flow, before anything is dialed |
| `Flow` | after the route lookup, before a member is picked | the same, after the ClientHello, so `ServerName` is set | never |
| `Datagram` | never | never | every datagram from the client before it is relayed, the first included, while `Datagrams()` is true |

A connection that is not TLS on a `tls` listener (`not_tls`), or that no
backend routes (`no_route`), ends before the `Flow` stage, so it is judged
by `Peer` alone.

### What each stage finds

- **`Listener`, `Protocol` and `Client`,** always.
- **`Proxy`,** which reads PROXY protocol TLVs, on every `tcp` and `tls`
  connection the daemon accepts, whether or not the listener takes the
  PROXY protocol; without a header it finds none. It can be nil, and always
  is on `udp`, so check it before calling `ProxyTLV`, and never read a
  non-nil `Proxy` as meaning that a header arrived.
- **`ServerName`,** only at the `Flow` stage on `tls`: the name as the
  client sent it, case and all. It is empty when the client sent none and
  a backend with no hosts catches such connections.

On a listener with `proxy_protocol`, `Client` is the source the header
named, and reading the header is part of the `Peer` stage. Anything that
trusts `Client`, such as an IP access list, has two cautions to heed:

- An empty `trusted_proxies` believes every peer's header, so any client
  can name its own source. A feature that trusts `Client` should require
  `trusted_proxies`, or at least warn, when `proxy_protocol` is on.
- When a trusted peer sends no header, or a LOCAL one, `Client` is that
  peer's own address.

### Verdicts

| Verdict | `tcp`, `tls` | `udp` at `Peer` | `udp` at `Datagram` |
|---|---|---|---|
| `Allow` | relayed | the flow proceeds | relayed |
| `Drop` | closed | the flow ends and its client is held | discarded |
| `Reject` | reset | as `Drop` | as `Drop` |

- **Closed** sends a FIN, unless unread bytes from the client make the
  kernel reset the connection instead.
- **Reset** sets `SO_LINGER` to zero, then closes, so the client sees a
  reset. The listener's connection wrapper implements `Reset() error`,
  reaching the TCP connection directly or through a PROXY protocol
  connection. The relay also resets a bare `*net.TCPConn`, and closes any
  other connection instead. Stream listeners are started without the
  listener-level connection limit, whose wrapper would hide the TCP
  connection; the relay applies `connections_limit` itself. Keep it that
  way, or half-close and reset both degrade to a plain close.

What is counted:

- A connection denied at either stage, a `udp` flow denied at `Peer`, and
  a `udp` flow that expired with no datagram allowed all count
  `result="denied"` in `trickster_proxy_stream_connections_total`.
- A denied datagram, and a datagram from a held client, counts
  `reason="denied"` in `trickster_proxy_stream_dropped_datagrams_total`.
  Datagrams already queued on a flow that `Peer` denies are discarded
  without being counted.

See [Metrics](../metrics.md). A feature adds its own decision metrics;
these only record that the relay turned something away.

### Rules for an implementation

- **Stages never run on a listener's accept or receive loop.** Each runs on
  the goroutine of the connection or flow it judges, so a slow admission
  never stalls the loop itself. It still holds resources while it runs:
  on `tcp` and `tls` a connection counts against `connections_limit` from
  the moment it is accepted, so enough slow callbacks stop the listener
  accepting; on `udp` a flow being judged counts against the 128 flows
  that may be opening at once, and past them every new flow is refused.
  Closing or restarting the listener waits for callbacks to return, and
  nothing cancels one. Keep every stage fast and bounded.
- **Be safe for concurrent use.** Many connections and flows are judged at
  once.
- **The relay holds no lock during a callback**, so an admission may call
  back into its server (`ActiveConnections`, `ActiveSessions`). The daemon
  creates the server after building its first `Config`, so reaching it
  takes wiring of the feature's own.
- **`Datagrams()` is read once per `Update`**, never per datagram. While it
  is false, `Datagram` is never called and a new flow opens as soon as
  `Peer` allows it. Changing the answer takes a new `Config`.
- **Treat a `Config` as immutable once it is handed to `Update`**; build a
  new one for each reload, as the daemon does.
- **Nothing is judged twice at `Peer` or `Flow`.** A `tcp` or `tls`
  connection is judged once, under the configuration it was accepted with,
  and a `udp` flow is judged at `Peer` once. Neither is asked again, so a
  reload that newly denies a client does not end its open connections or
  flows. Only datagrams are judged afresh: a `udp` flow loads the current
  configuration for each datagram, so a new admission that judges
  datagrams reaches flows that are already open.

### UDP specifics

- **A client is an address and a port.** A flow, and a hold, belong to one
  source address and port, and the same address from a new port is a new
  flow, judged again. A feature that keys on the client should key on
  `Flow.Client.Addr()`.
- **Peer denials are held.** After `Peer` denies a client's new flow, the
  client's later datagrams are dropped without asking (`denied`) for
  `l4.DefaultDeniedHold` (5 seconds), or for whatever the admission's
  `Holder.Hold(f)` returns for that flow. Zero or a negative duration holds
  nothing, so each later datagram opens a flow of its own and is judged
  again. Any configuration swap releases every hold. The daemon swaps every
  stream listener's configuration on every reload, even one that changes
  nothing about the listener, so any reload ends every hold, and a reload
  that reverses a denial takes effect at once.
- **The hold table is bounded.** At most 4096 clients are held; past that
  the oldest hold is dropped, and that client is judged again on its next
  datagram. Those judgments are bounded by the 128 flows that may be
  opening at once, and the excess is refused (`result="refused"`). That
  is the relay's behavior under a flood from more sources than the table
  holds.
- **Denied datagrams never cost an upstream.** While `Datagrams()` is true,
  a new flow's queued datagrams are judged before anything is dialed, and
  the flow dials only once one is allowed. A flow with nothing allowed yet
  waits in an allowance of its own (256 flows), outside the session bound
  (`connections_limit`, 1024 sessions by default), so denied sources
  cannot take sessions from allowed ones. Past that allowance the oldest
  waiting flow is expired and the newcomer refused. A waiting flow expires
  once the listener's `stream.idle_timeout` (60 seconds by default on
  `udp`) passes with nothing allowed, and counts `denied`.
- **Only relayed traffic keeps a flow alive.** Denied datagrams never
  refresh a flow's idle timer; relayed datagrams in either direction do. A
  flow whose client turns to denied traffic goes idle once its upstream
  falls quiet too, and until then the upstream's replies still reach the
  client.
- **Choosing a hold.** A limiter keyed per client should implement
  `Holder` and return the time until that client may send again. A static
  access list can keep the default and rely on the release at reload.

### What an Allow promises a limiter

- **Not that anything is relayed.** A `udp` flow that `Peer` allows can
  still be refused at the waiting allowance or the session bound, and a
  dial can fail.
- **Not one question per datagram.** A reload that lands while a datagram
  is being judged has the new admission judge it again, so a limiter whose
  counters outlive the reload can be charged twice for that one datagram.
- **Nothing per byte on `tcp` or `tls`.** A connection is judged when it
  opens and never again, so a limiter there can limit connections, not
  bandwidth.
- **Nothing about replies.** On `udp` only datagrams from the client are
  judged; replies from the upstream are not.

### Testing an admission

`pkg/proxy/l4/admission_test.go` drives every stage against real sockets
with fake admissions; copy from it. Lessons from writing it:

- Pass `go test` an explicit `-timeout`. Otherwise a callback that never
  returns hangs the package for ten minutes.
- Wait on the observer's counts, measured from a baseline, rather than
  reading a gauge at once: a flow leaves the active count just before its
  end is reported.
- When a test needs a dial to fail, fail it with the standard library's
  `syscall.ECONNREFUSED` rather than an error of the test's own.

## The HTTP listener seam

`wrapListener` in
[pkg/daemon/setup/listeners.go](../../pkg/daemon/setup/listeners.go)
wraps the router of every HTTP listener:

```go
func wrapListener(o *listenerconfig.Options, routerLogger *accesslog.Logger, next http.Handler) http.Handler {
	return clientip.Middleware(trustedProxies(o), accesslog.RouterMiddleware(routerLogger, next))
}
```

- **Coverage.** Every `http` listener, `mgmt` and `metrics` included, and
  every endpoint of it: the plain, TLS and HTTP/3 endpoints share one
  wrapped router. Every request passes through, including requests that no
  route matches (404) and the readiness path, which is served ahead of each
  proxy listener's routes. Stream and native listeners never pass through
  it.
- **Order, outermost first:** client IP resolution from the listener's
  `trusted_proxies`, then the router-level access log, then `next`. On a
  proxy listener `next` is the readiness guard over the listener's router;
  on `mgmt` it is the management router, and on `metrics` the metrics
  router.
- **A feature goes between the access log and `next`,** listener-scope
  access control first, then rate limiting:
  `clientip.Middleware(trusted, accesslog.RouterMiddleware(logger, acl(limit(next))))`.
  There it sees the resolved client (`request.ClientIP(r)`), and a request
  it answers itself, such as a 403 or a 429, is logged by the router-level
  access log with its status, as unmatched requests are. That log exists
  only when the top-level `access_log` is configured, and never on
  `metrics`; without it, such denials show only in the feature's own
  metrics.
- **Only keys that require nothing.** The seam runs before any
  authenticator or body filter, so a feature here may key only on sources
  whose `Requires()` is zero (see [what a key requires](#what-a-key-requires)).
- **No cost when nothing is configured.** With no trusted proxies and no
  access log, both wrappers return `next` unchanged. Added middleware must
  do the same: return `next` when the listener has nothing configured.
- **Handle a nil `next`.** On reload the daemon also builds the previous
  configuration's listeners, with nil routers and no logger, only to
  compare their addresses and options. Added middleware must pass a nil
  `next` through without building anything.
- **Keep state outside the handler.** `wrapListener` runs again on every
  reload, and its result replaces the old handler in place unless the
  listener itself must restart. State that has to outlive a reload, such
  as rate-limit counters, belongs in a registry kept outside the handler.
- **Readiness probes pass through it.** A listener-scope access list on a
  proxy listener must allow the prober, or the feature must exempt the
  readiness path.
- **Not for a backend or a path.** Middleware for a backend or a path
  belongs in the route chain built in `pkg/routing`. `attachAuthenticator`
  there shows where an authenticator is applied, and how a path's
  reference replaces or clears its backend's.

## The `none` reference

[pkg/config/reserved](../../pkg/config/reserved/reserved.go) defines
`ReferenceNone = "none"`, with `References()` and `IsReference(name)`.

- **Meaning.** Given in place of an object name, `none` refers to no
  object. On a path it clears what the path would inherit from its
  backend: `authenticator_name: none` turns off the backend's
  authenticator for that path. Validation (`ValidateConfigMappings` in
  `pkg/backends/options`) does not try to resolve it, and routing
  (`attachAuthenticator` and `hasAuthenticator` in `pkg/routing`) does not
  apply the backend's authenticator.
- **Paths only.** On a backend, `authenticator_name: none` fails
  validation as an undefined authenticator, since there is nothing to
  clear.
- **Reserve the name.** An object kind whose references accept `none` must
  refuse it as a name, or a reference could not tell the two apart. Its
  options' `Validate` does it in one line, as authenticators, caches,
  rules and request rewriters do:
  `if o.Name == "" || reserved.IsReference(o.Name) { ... }`. That works
  because each kind's `Lookup.Validate` first sets `o.Name` from the
  object's key; a new kind must do the same. Matching is case-sensitive,
  so `None` is an ordinary name.
- **Register a new section.** A new top-level section of named objects
  also belongs in `overlaySections` (`pkg/config/overlay.go`). That list is
  what refuses the reserved `kgw--` name prefix in file-sourced names, and
  what lets a generated overlay define the section's objects.
- **A new reference that accepts `none`,** such as a path's access list or
  rate limiter, compares against `reserved.ReferenceNone` rather than the
  literal, skips resolving it during validation, and honors it where the
  route chain is built.
