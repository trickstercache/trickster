# Non-Cryptographic Randomness

Trickster uses fast, non-cryptographic ("weak") random numbers in a few places. The application uses them for request correlation IDs, traffic sampling and simulated latency. Tests use them for reproducible data and shuffles. A weak source is fine for these jobs, but it is predictable, so it must never produce anything an attacker should not be able to guess.

To keep every use visible and reviewable, weak randomness goes through `pkg/util/weak`, with the narrow standalone-core exception below. The application draws from one package, tests from another, and neither may use the other's. This lets us audit production use, phase it out when a use becomes security-relevant, and keep test-only helpers out of the running application.

## Which Package to Use

| Where the code lives | Use |
| --- | --- |
| Application code: non-test files under `cmd/` and `pkg/` | [`pkg/util/weak/compat`](../../pkg/util/weak/compat/compat.go) |
| Tests and tooling: `_test.go` files, `pkg/testutil`, packages whose directory name ends in `test` (such as `streamtest`), and everything under `integration/`, `examples/` and `hack/` | [`pkg/util/weak/weaktest`](../../pkg/util/weak/weaktest/weaktest.go) |
| Anything security-relevant: tokens, keys, nonces, session IDs | `crypto/rand`, never `pkg/util/weak` |

Do not introduce direct `math/rand` or `math/rand/v2` imports elsewhere. The standalone `pkg/lb` tree has a separately tested standard-library-only dependency boundary. Its four existing `math/rand/v2` users are explicitly allowed: `hrw/hrw.go` and `p2c/p2c.go` for load spreading, `rr/rr.go` for the initial rotation offset, and `lbtest/lbtest.go` for reproducible test flows. Their existing `gosec` justifications remain in place; the exception does not cover new files or cryptographic uses.

### Application Code

`compat` offers `Uint64`, `IntN` and `Int64`, all drawn from a randomly seeded source:

```go
import "github.com/trickstercache/trickster/v2/pkg/util/weak/compat"

if compat.IntN(100) < percent {
	// mirror this request
}
```

Keep `compat` small. When adding a caller, say in the pull request why a predictable value is acceptable there. When a use moves to `crypto/rand`, remove any `compat` function that no longer has callers.

### Tests

`weaktest.NewRand` returns a generator whose sequence is fixed by its two seeds, so a failing test fails the same way every run. `weaktest.IntN` draws from a randomly seeded source, for tests that do not care which values they get:

```go
import "github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

rng := weaktest.NewRand(42, 0) // same seeds, same sequence
rng.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })

n := weaktest.IntN(1000)
```

`weaktest.Rand` is an alias for the `math/rand/v2` generator. Helpers can accept a `*weaktest.Rand` and call its methods without importing `math/rand/v2`.

## How the Rules Are Enforced

The rules are checked at runtime and at build time, so a defect in one check is caught by another.

### At Runtime

The application's `main` calls `weak.RegisterUnlessTestMode()` before anything else. From then on, every `weaktest` function panics with `weak: test-only randomness used outside of a test; use package compat`. Unit tests never run `main`, so `weaktest` works normally inside them.

`compat` has no runtime check. Application code such as mirror sampling runs inside unit tests too, and a process-wide switch cannot tell that code apart from test code that misuses `compat`. The build-time checks below cover that direction.

### `make check-weak-random`

[`hack/check-weak-random`](../../hack/check-weak-random/main.go) reads the imports of every Go file under `cmd/`, `pkg/`, `integration/`, `examples/` and `hack/`, and fails when:

- a file outside `pkg/util/weak/` imports `math/rand` or `math/rand/v2`, except for the four standalone-core uses listed above
- test or tooling code imports `pkg/util/weak/compat`
- application code imports `pkg/util/weak/weaktest`

Both `make test`, which CI runs, and `make lint` run this check. Its own unit tests also check the whole repository, so a plain `go test` catches violations as well.

### `golangci-lint`

The `forbidigo` linter in [`.golangci.yml`](../../.golangci.yml) flags any use of a `math/rand` or `math/rand/v2` function or type outside `pkg/util/weak/` and the four standalone-core files. It resolves imports by type, so renamed imports such as `mrand "math/rand/v2"` are caught too. Methods on a `*weaktest.Rand` are still allowed. The configuration does not lint `_test.go` files, so `make check-weak-random` is what covers tests. `gosec` also still reports weak randomness (G404); its reviewed exceptions are confined to these locations.

## Test Mode for Launched Processes

A test that starts a real trickster process can set the `TRICKSTER_TEST_MODE` environment variable to any non-empty value. `main` then skips registration, and `weaktest` stays usable in that process. Never set it in production. Integration tests that run trickster in-process through `daemon.Start` never call `main`, so they do not need it.

## Adding a Test Helper Package

A new helper package that tests import is test code, so it must use `weaktest`. Name its directory with a `test` suffix, like `streamtest`, or place it under `pkg/testutil`, so `make check-weak-random` classifies it as test code.
