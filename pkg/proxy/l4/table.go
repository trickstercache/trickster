/*
 * Copyright 2026 The Trickster Authors
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

// Package l4 relays stream connections and datagrams to upstream members without reading
// the protocol they carry, selecting the member by the TLS server name where one is offered.
package l4

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
)

var (
	// ErrDuplicateHost indicates a host already routed by the table.
	ErrDuplicateHost = errors.New("host is already routed")
	// ErrDuplicateCatchAll indicates a second upstream for connections naming no routed host.
	ErrDuplicateCatchAll = errors.New("the listener already has a catch-all upstream")
)

// Table routes a connection to an upstream by the server name it offered: an exact host first,
// then the longest wildcard suffix, then the catch-all for a connection naming no routed host.
type Table = HostTable[Upstream]

// HostTable maps server names to values as the relay routes them, so anything that must agree with the
// relay's choice of backend, such as an admission, looks the name up the same way.
type HostTable[V any] struct {
	catchAll    V
	hasCatchAll bool
	exact       map[string]V
	wild        []wildEntry[V]
}

// wildEntry is one wildcard host: the suffix it stands under and whether it spans any depth.
type wildEntry[V any] struct {
	suffix   string
	anyDepth bool
	value    V
}

// NewTable returns an empty table.
func NewTable() *Table {
	return NewHostTable[Upstream]()
}

// NewHostTable returns an empty HostTable.
func NewHostTable[V any]() *HostTable[V] {
	return &HostTable[V]{exact: make(map[string]V)}
}

// Add maps host to v; an empty host is the catch-all. A wildcard host follows the router's
// spelling: *.example.com spans one label and **.example.com any number.
func (t *HostTable[V]) Add(host string, v V) error {
	h, err := hostnames.Normalize(host, hostnames.AllowEmpty)
	if err != nil {
		return err
	}
	if h == "" {
		if t.hasCatchAll {
			return ErrDuplicateCatchAll
		}
		t.catchAll, t.hasCatchAll = v, true
		return nil
	}
	if hostnames.IsWildcard(h) {
		e := wildEntry[V]{suffix: "." + hostnames.Suffix(h), anyDepth: hostnames.IsAnyDepth(h), value: v}
		if slices.ContainsFunc(t.wild, func(o wildEntry[V]) bool {
			return o.suffix == e.suffix && o.anyDepth == e.anyDepth
		}) {
			return fmt.Errorf("%w: %s", ErrDuplicateHost, h)
		}
		t.wild = append(t.wild, e)
		// longest suffix first, so the most specific wildcard wins
		slices.SortStableFunc(t.wild, func(a, b wildEntry[V]) int {
			return len(b.suffix) - len(a.suffix)
		})
		return nil
	}
	if _, dup := t.exact[h]; dup {
		return fmt.Errorf("%w: %s", ErrDuplicateHost, h)
	}
	t.exact[h] = v
	return nil
}

// Lookup returns the value for a server name, or the zero value when nothing routes it.
func (t *HostTable[V]) Lookup(host string) V {
	var zero V
	if t == nil {
		return zero
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host != "" {
		if v, ok := t.exact[host]; ok {
			return v
		}
		for _, e := range t.wild {
			if !strings.HasSuffix(host, e.suffix) {
				continue
			}
			if e.anyDepth || !strings.Contains(strings.TrimSuffix(host, e.suffix), ".") {
				return e.value
			}
		}
	}
	if t.hasCatchAll {
		return t.catchAll
	}
	return zero
}

// Empty reports whether the table routes nothing at all.
func (t *HostTable[V]) Empty() bool {
	return t == nil || (!t.hasCatchAll && len(t.exact) == 0 && len(t.wild) == 0)
}
