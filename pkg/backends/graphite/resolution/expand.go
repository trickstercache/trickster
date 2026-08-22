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
	"context"
	"encoding/json"
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

type findNode struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Leaf int    `json:"leaf"`
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
	leaves, err := e.find(ctx, expr, true)
	if err != nil {
		return nil, "", err
	}
	id := e.Registry.SetTarget(expr, leaves, e.TTL)
	return leaves, id, nil
}

// Exists reports whether a leaf path exists at the origin
func (e *Expander) Exists(ctx context.Context, path string) Existence {
	leaves, err := e.find(ctx, path, true)
	if err != nil {
		return ExistsUnknown
	}
	if slices.Contains(leaves, path) {
		return Exists
	}
	return NotExists
}

// find calls /metrics/find and returns the matching ids; leafOnly drops
// branch nodes
func (e *Expander) find(ctx context.Context, query string, leafOnly bool) ([]string, error) {
	body, err := e.Origin.Get(ctx, "/metrics/find", url.Values{"query": {query}, "format": {"treejson"}})
	if err != nil {
		e.observe(ResultError)
		return nil, err
	}
	var nodes []findNode
	if err := json.Unmarshal(body, &nodes); err != nil {
		e.observe(ResultError)
		return nil, err
	}
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if leafOnly && n.Leaf == 0 {
			continue
		}
		if n.ID != "" {
			out = append(out, n.ID)
		}
	}
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
