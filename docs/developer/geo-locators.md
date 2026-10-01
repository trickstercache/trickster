# Geo Locators

A geo locator places a client: it turns an address, or an HTTP request, into a location. Geo ACLs
([user guide](../geo-acl.md)) judge whatever a locator returns, so a new source of location data is a
new locator provider, and nothing in the ACLs, the route chain or the listeners changes for it.

## Packages

| Package | Holds |
|---|---|
| `pkg/proxy/geo` | `Location`, `Code2`, `Fields`, list entries, the country and continent tables |
| `pkg/proxy/geo/locator` | the `Locator`, `RequestLocator` and `BuildTimer` interfaces |
| `pkg/proxy/geo/locator/providers` | provider names, `IsValidProvider`, `ReadsAddresses`, shared option rules |
| `pkg/proxy/geo/locator/options` | the `geo_locators` section: one block per provider |
| `pkg/proxy/geo/locator/<provider>` | each provider, with its `options` subpackage |
| `pkg/proxy/geo/locator/filesource` | watching a provider's data files and loading each change |
| `pkg/proxy/geo/locator/registry` | the constructor map, `New`, and the running `Set` |
| `pkg/proxy/geo/acl` | compiled geo ACLs: `Check`, `CheckRequest`, the session gate |
| `pkg/proxy/geo/acl/options` | the `geo_acls` section |
| `pkg/proxy/geo/acl/handler`, `pkg/proxy/geo/acl/stream` | the HTTP middleware and the stream admission |

`pkg/config` imports the options packages, and the packages they import. None of them may import
`pkg/config`, `pkg/proxy/l4`, a provider's implementation or a database reader;
`pkg/proxy/geo/boundary_test.go` checks the list. Add a new options package to that list.

## The interface

```go
type Locator interface {
	Locate(addr netip.Addr) (geo.Location, error)
	Serves() geo.Fields
	Close() error
}

type RequestLocator interface {
	LocateRequest(r *http.Request) (geo.Location, error)
}
```

- **`Locate`** returns the zero `Location` and a nil error when it has no answer, and an error when
  the lookup failed. Both are judged as no location; the lookup metric tells them apart. The address
  arrives unmapped.
- **Answer from memory.** Every stage that asks is on a request's or a session's path: an HTTP
  request, a native session before authentication, a stream connection before it is relayed. A
  lookup must not block, and must not allocate. Native and stream listeners never ask a provider
  that could block; a provider that must call out to a network belongs behind a cache, on HTTP only,
  and must say so through `providers.ReadsAddresses` or a rule like it.
- **`Serves`** reports which of country, continent and subdivision the locator fills. Fix it with the
  first load: a geo ACL with subdivision entries refuses a locator that serves none when the
  configuration is applied, and a replacement that serves fewer fields must be refused, never loaded.
  A country implies its continent, so an ACL with continent entries accepts a locator that serves
  countries.
- **`RequestLocator`** is for a provider that reads the request rather than the address, as `header`
  does. The route chain calls `LocateRequest` when a locator implements it, and only the route chain
  does, so such a provider's `providers.ReadsAddresses` is false and validation keeps it off native
  and stream listeners.
- **`Close`** stops whatever the locator runs, such as a file watcher. A lookup may still be underway
  when it is called, since a reload closes a replaced locator while old requests drain, so `Close`
  must leave the loaded data readable.
- **`BuildTimer`**, optional, reports when the loaded data was built, for
  `trickster_geo_locator_build_timestamp_seconds`.

## Adding a provider

1. **Name it** for how it locates, not for a vendor, in `pkg/proxy/geo/locator/providers`, and add it
   to `supported`. Decide `ReadsAddresses`.
2. **Write its options package** under `pkg/proxy/geo/locator/<provider>/options`, with `New`,
   `Clone`, `Initialize` for defaults and `Validate`. Validation builds nothing: it may check that a
   file is readable (`providers.CheckReadable`) but must never load it, since `-validate-config` stops
   after validation. A file-based provider takes `reload_interval` and checks it with
   `providers.ValidateReloadInterval`. An option that takes one of a set of names is a typed value with
   `Parse`, `String`, `MarshalText` and `UnmarshalText`, as `mmdb`'s `Schema` is.
3. **Add its block to `pkg/proxy/geo/locator/options`**: the field, `Clone`, `Initialize`, the block
   check in `Validate` and the provider's own validation.
4. **Write the provider** in `pkg/proxy/geo/locator/<provider>`. A provider that reads files hands
   `filesource.Watch` a load function. `Watch` loads the files before it returns and fails when the
   first load fails; afterward it loads each change, counts reloads, and keeps the last good data when
   a load returns an error. Keep the loaded data behind an atomic pointer and swap it whole; never
   change data a lookup may be reading.
5. **Register its constructor** in `pkg/proxy/geo/locator/registry`.
6. **Test it**: every field it reads, a missing record, IPv4 and IPv6, a replacement under concurrent
   lookups with the race detector, every replacement it must refuse, and a benchmark showing no
   allocation. `pkg/testutil/geodb` builds MaxMind DB files for tests, so no binary database is
   committed.
7. **Document it** in [Geo ACLs](../geo-acl.md) and in the example configuration.

## Lifecycle

`pkg/daemon/setup` builds the locators each configuration needs (only those an attached geo ACL
names, or every one when the Kubernetes controller is enabled), compiles each attached geo ACL into
its options, and keeps the running set on the server instance:

- a locator whose options are unchanged is kept across a reload, with its loaded data;
- a replaced or removed locator is closed only once the reload commits, since a reload that rolls
  back serves the old configuration again;
- an apply that fails closes the locators it built, and none of those still serving;
- `Shutdown` closes them ahead of the caches.

Backends and paths share their geo ACL's options with every clone, so the ACL compiled into them
reaches a pool member that discovery instantiates from a template.
