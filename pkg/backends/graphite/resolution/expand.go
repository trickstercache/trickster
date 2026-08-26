/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package resolution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
)

// Existence is the outcome of an existence check
type Existence int

const (
	// ExistsUnknown: the check itself failed (transport, non-2xx)
	ExistsUnknown Existence = iota
	// Exists: /metrics/find returned a leaf for the path
	Exists
	// NotExists: /metrics/find returned no leaf for the path
	NotExists
)

// Expander resolves target path expressions to leaf metric paths via
// /metrics/find, caching expansions in the registry's target layer
type Expander struct {
	Origin   *Origin
	Registry *Registry
	Observer Observer
	Tracers  *Tracers
	// TTL is how long an expansion is reused
	TTL          time.Duration
	MaxLeaves    int
	MaxLeafBytes int

	mu       sync.Mutex
	inflight map[string]*expandCall
}

// expandCall is one in-progress expansion shared by coalesced callers
type expandCall struct {
	done   chan struct{}
	cancel context.CancelFunc
	// waiters and closed are guarded by Expander.mu: admission, zero-waiter
	// abandonment, and inflight-map removal must be one atomic decision
	waiters int
	closed  bool
	leaves  []string
	id      string
	err     error
}

// the last caller out of a still-open call closes and removes it before
// canceling, so a later caller starts fresh instead of joining a canceled ctx
func (e *Expander) leave(expr string, call *expandCall) {
	e.mu.Lock()
	call.waiters--
	abandoned := call.waiters == 0 && !call.closed
	if abandoned {
		call.closed = true
		if e.inflight[expr] == call {
			delete(e.inflight, expr)
		}
	}
	e.mu.Unlock()
	if abandoned {
		call.cancel()
	}
}

// the call is closed and removed under the mutex before done is closed, so no
// new caller can join once its shared context is about to be released
func (e *Expander) complete(expr string, call *expandCall) {
	e.mu.Lock()
	call.closed = true
	if e.inflight[expr] == call {
		delete(e.inflight, expr)
	}
	e.mu.Unlock()
	close(call.done)
	call.cancel()
}

// Default expansion fan-out bounds, mirrored by the graphite options
// package; these guard memory, not functionality.
const (
	DefaultMaxLeaves    = 4096
	DefaultMaxLeafBytes = 2 * 1024 * 1024
)

// ErrExpansionTooLarge is returned when an expansion exceeds the
// configured fan-out bounds; the request is served unaccelerated.
var ErrExpansionTooLarge = errors.New("wildcard expansion exceeds the configured bounds")

// expandResult is one document of a /metrics/expand response; graphite-web
// emits one document per top-level brace alternative, concatenated.
type expandResult struct {
	Results []string `json:"results"`
}

// IsWildcard reports whether a path expression needs expansion
func IsWildcard(expr string) bool {
	return strings.ContainsAny(expr, "*?[{")
}

// Expand returns the leaf paths a path expression matches. A plain path
// expands to itself without a round trip; existence is established by probes.
func (e *Expander) Expand(ctx context.Context, expr string) ([]string, string, error) {
	if !IsWildcard(expr) {
		return []string{expr}, ExpansionID([]string{expr}), nil
	}
	if leaves, id, ok := e.Registry.Target(expr); ok {
		return leaves, id, nil
	}
	// an expression whose expansion recently exceeded the fan-out bounds
	// is declined without another origin round trip
	if _, neg := e.Registry.Negative("expand\x00" + expr); neg {
		return nil, "", ErrExpansionTooLarge
	}
	// one shared origin call per expression, with its own lifecycle: canceled
	// only when no callers remain, while each caller may return on its own ctx
	e.mu.Lock()
	if e.inflight == nil {
		e.inflight = make(map[string]*expandCall)
	}
	call, running := e.inflight[expr]
	if !running {
		sctx, cancel := context.WithCancel(context.Background())
		call = &expandCall{done: make(chan struct{}), cancel: cancel, waiters: 1}
		e.inflight[expr] = call
		safego.Go(func(r any, stack []byte) {
			logger.Error("graphite expansion panicked", logging.Pairs{
				"query": expr, "panic": fmt.Sprint(r), "stack": string(stack),
			})
			call.err = errors.New("graphite expansion panicked")
			e.complete(expr, call)
		}, func() {
			call.leaves, call.err = e.find(sctx, expr)
			if call.err == nil {
				// find's slice is canonical; SetTarget takes ownership
				call.id = e.Registry.SetTarget(expr, call.leaves, e.TTL)
			} else if errors.Is(call.err, ErrExpansionTooLarge) {
				e.Registry.SetNegative("expand\x00" + expr)
			}
			e.complete(expr, call)
		})
	} else {
		call.waiters++
	}
	e.mu.Unlock()
	select {
	case <-call.done:
		e.leave(expr, call)
		return call.leaves, call.id, call.err
	case <-ctx.Done():
		e.leave(expr, call)
		return nil, "", ctx.Err()
	}
}

// Exists reports whether a leaf path exists at the origin
func (e *Expander) Exists(ctx context.Context, path string) Existence {
	leaves, err := e.find(ctx, path)
	if err != nil {
		return ExistsUnknown
	}
	if slices.Contains(leaves, path) {
		return Exists
	}
	return NotExists
}

// expands a path expression to concrete leaves via /metrics/expand?leavesOnly=1:
// /metrics/find echoes an interior wildcard back as a single node, which would
// key the registry on the pattern and hide new metrics beneath it
func (e *Expander) find(ctx context.Context, query string) ([]string, error) {
	ctx, span := tspan.NewChildSpan(ctx, e.Tracers.Get(), "GraphiteExpand")
	if span != nil {
		defer span.End()
	}
	body, err := e.Origin.Get(ctx, "/metrics/expand",
		url.Values{"query": {query}, "leavesOnly": {"1"}, "format": {"json"}})
	if err != nil {
		e.observe(ResultError)
		return nil, err
	}
	maxLeaves, maxBytes := e.MaxLeaves, e.MaxLeafBytes
	if maxLeaves <= 0 {
		maxLeaves = DefaultMaxLeaves
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxLeafBytes
	}
	// one document per top-level brace alternative, concatenated
	dec := json.NewDecoder(bytes.NewReader(body))
	seen := make(map[string]struct{})
	out := make([]string, 0, 8)
	nameBytes := 0
	for {
		var res expandResult
		if err := dec.Decode(&res); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			e.observe(ResultError)
			return nil, err
		}
		for _, p := range res.Results {
			if p == "" {
				continue
			}
			if _, dup := seen[p]; dup {
				continue
			}
			if len(out) >= maxLeaves || nameBytes+len(p) > maxBytes {
				e.observe(ResultError)
				return nil, fmt.Errorf("%w: %q matches more than %d leaves or %d bytes",
					ErrExpansionTooLarge, query, maxLeaves, maxBytes)
			}
			seen[p] = struct{}{}
			out = append(out, p)
			nameBytes += len(p)
		}
	}
	slices.Sort(out)
	if len(out) == 0 {
		e.observe(ResultEmpty)
	} else {
		e.observe(ResultStep)
	}
	return out, nil
}

func (e *Expander) observe(result string) {
	if e.Observer != nil {
		e.Observer.Probe(KindFind, result)
	}
}
