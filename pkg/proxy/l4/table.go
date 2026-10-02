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
type Table struct {
	catchAll Upstream
	exact    map[string]Upstream
	wild     []wildEntry
}

// wildEntry is one wildcard host: the suffix it stands under and whether it spans any depth.
type wildEntry struct {
	suffix   string
	anyDepth bool
	up       Upstream
}

// NewTable returns an empty table.
func NewTable() *Table {
	return &Table{exact: make(map[string]Upstream)}
}

// Add routes host to up; an empty host is the catch-all. A wildcard host follows the router's
// spelling: *.example.com spans one label and **.example.com any number.
func (t *Table) Add(host string, up Upstream) error {
	h, err := hostnames.Normalize(host, hostnames.AllowEmpty)
	if err != nil {
		return err
	}
	if h == "" {
		if t.catchAll != nil {
			return ErrDuplicateCatchAll
		}
		t.catchAll = up
		return nil
	}
	if hostnames.IsWildcard(h) {
		e := wildEntry{suffix: "." + hostnames.Suffix(h), anyDepth: hostnames.IsAnyDepth(h), up: up}
		if slices.ContainsFunc(t.wild, func(o wildEntry) bool {
			return o.suffix == e.suffix && o.anyDepth == e.anyDepth
		}) {
			return fmt.Errorf("%w: %s", ErrDuplicateHost, h)
		}
		t.wild = append(t.wild, e)
		// longest suffix first, so the most specific wildcard wins
		slices.SortStableFunc(t.wild, func(a, b wildEntry) int {
			return len(b.suffix) - len(a.suffix)
		})
		return nil
	}
	if _, dup := t.exact[h]; dup {
		return fmt.Errorf("%w: %s", ErrDuplicateHost, h)
	}
	t.exact[h] = up
	return nil
}

// Lookup returns the upstream for a server name, or nil when nothing routes it.
func (t *Table) Lookup(host string) Upstream {
	if t == nil {
		return nil
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host != "" {
		if up, ok := t.exact[host]; ok {
			return up
		}
		for _, e := range t.wild {
			if !strings.HasSuffix(host, e.suffix) {
				continue
			}
			if e.anyDepth || !strings.Contains(strings.TrimSuffix(host, e.suffix), ".") {
				return e.up
			}
		}
	}
	return t.catchAll
}

// Empty reports whether the table routes nothing at all.
func (t *Table) Empty() bool {
	return t == nil || (t.catchAll == nil && len(t.exact) == 0 && len(t.wild) == 0)
}
