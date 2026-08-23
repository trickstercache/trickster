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
	"io"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Existence is the outcome of an existence check (design note §4.5 table)
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
	// TTL is how long an expansion is reused
	TTL time.Duration
}

// expandResult is one document of a /metrics/expand response. graphite-web
// emits one document per top-level brace alternative, concatenated, so the
// body is a stream of these rather than a single value.
type expandResult struct {
	Results []string `json:"results"`
}

// IsWildcard reports whether a path expression needs expansion
func IsWildcard(expr string) bool {
	return strings.ContainsAny(expr, "*?[{")
}

// Expand returns the leaf paths a path expression matches. A plain path
// expands to itself without a round trip; existence is established later
// by the probe (a present metric returns a stepped series even when young,
// §9.3, and an absent one returns nothing).
func (e *Expander) Expand(ctx context.Context, expr string) ([]string, string, error) {
	if !IsWildcard(expr) {
		return []string{expr}, ExpansionID([]string{expr}), nil
	}
	if leaves, id, ok := e.Registry.Target(expr); ok {
		return leaves, id, nil
	}
	leaves, err := e.find(ctx, expr)
	if err != nil {
		return nil, "", err
	}
	id := e.Registry.SetTarget(expr, leaves, e.TTL)
	return leaves, id, nil
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

// find expands a path expression to the concrete leaf paths it matches.
//
// It uses /metrics/expand rather than /metrics/find: find is a tree browser
// that answers one level at a time, and given a pattern with a wildcard at
// an interior level it returns a single node echoing the pattern back
// (measured on graphite-web 1.1.10: dev.fast.cpu.*.percent yields one node
// with id "dev.fast.cpu.*.percent"). Keying the registry on that pattern
// would reintroduce exactly the staleness the design note's §5.5 warns
// about, because a new metric under the wildcard would not change the key.
// /metrics/expand?leavesOnly=1 enumerates the concrete leaves instead.
func (e *Expander) find(ctx context.Context, query string) ([]string, error) {
	body, err := e.Origin.Get(ctx, "/metrics/expand",
		url.Values{"query": {query}, "leavesOnly": {"1"}, "format": {"json"}})
	if err != nil {
		e.observe(ResultError)
		return nil, err
	}
	// one document per top-level brace alternative, concatenated
	dec := json.NewDecoder(bytes.NewReader(body))
	seen := make(map[string]struct{})
	out := make([]string, 0, 8)
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
			seen[p] = struct{}{}
			out = append(out, p)
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
