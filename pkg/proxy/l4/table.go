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
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
)

// maxPoolDepth bounds how far a pool of pools is followed when selecting an address: a pool
// whose members are themselves pools, which is what a weighted rule over discovered endpoints is.
const maxPoolDepth = 2

var (
	// ErrDuplicateHost indicates a host already routed by the table.
	ErrDuplicateHost = errors.New("host is already routed")
	// ErrDuplicateCatchAll indicates a second upstream for connections naming no routed host.
	ErrDuplicateCatchAll = errors.New("the listener already has a catch-all upstream")
)

// Upstream chooses the address a connection is relayed to.
type Upstream interface {
	// Addr returns the host:port to dial for the next connection, or false to refuse it.
	Addr() (string, bool)
}

// reservedTLD is the top-level domain reserved never to resolve; an address under it refuses
// connections without a lookup, which is how a member that must refuse its share is expressed.
const reservedTLD = ".invalid"

// Refusing reports whether an address can never be dialed: one whose host is under the reserved
// .invalid domain.
func Refusing(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return strings.HasSuffix(host, reservedTLD)
}

// Static returns an upstream with one fixed address, or one that refuses every connection when
// the address can never be dialed.
func Static(addr string) Upstream {
	if Refusing(addr) {
		return refusingUpstream{}
	}
	return staticUpstream{addr: addr}
}

type staticUpstream struct {
	addr string
}

func (s staticUpstream) Addr() (string, bool) {
	return s.addr, true
}

// refusingUpstream refuses every connection, without a lookup or a dial
type refusingUpstream struct{}

func (refusingUpstream) Addr() (string, bool) {
	return "", false
}

// pooled is implemented by a backend whose members are a load balancer pool.
type pooled interface {
	Pool() pool.Pool
}

// FromBackend returns an upstream over a backend: a pool holder commits each connection to one
// healthy member by weighted rotation, a member that is itself a pool choosing again the same
// way, and any other backend is dialed at its origin host. A member that cannot be dialed refuses
// its share rather than passing it to a sibling, as the HTTP load balancer does.
func FromBackend(b backends.Backend) Upstream {
	return fromBackend(b, 0)
}

func fromBackend(b backends.Backend, depth int) Upstream {
	if b == nil {
		return nil
	}
	if p, ok := b.(pooled); ok {
		return &poolUpstream{pool: p.Pool, depth: depth}
	}
	cfg := b.Configuration()
	if cfg == nil || cfg.Host == "" {
		return nil
	}
	return Static(cfg.Host)
}

// poolUpstream rotates over a pool's healthy members with the members' weights, so consecutive
// connections are apportioned as the round robin mechanism apportions requests.
type poolUpstream struct {
	pool  func() pool.Pool
	depth int
	pos   atomic.Uint64
	// nested keeps one rotation per member that is itself a pool, keyed by the member's
	// backend, which outlives the pools swapped beneath it as membership changes
	nested sync.Map
}

func (p *poolUpstream) Addr() (string, bool) {
	pl := p.pool()
	if pl == nil {
		return "", false
	}
	targets := pl.Targets()
	if len(targets) == 0 {
		return "", false
	}
	t := targets[p.start(targets)]
	if t == nil {
		return "", false
	}
	return p.resolve(t)
}

func (p *poolUpstream) start(targets pool.Targets) int {
	// each member owns a weight-sized span of the rotation, as the round robin mechanism does
	var total int
	weighted := false
	for _, t := range targets {
		if t == nil {
			continue
		}
		if t.Weight() != 1 {
			weighted = true
		}
		total += t.Weight()
	}
	if total == 0 {
		return 0
	}
	k := int(p.pos.Add(1) % uint64(total)) // #nosec G115 -- the value is below total, an int sum
	if !weighted {
		return k
	}
	for i, t := range targets {
		if t == nil {
			continue
		}
		k -= t.Weight()
		if k < 0 {
			return i
		}
	}
	return len(targets) - 1
}

func (p *poolUpstream) resolve(t *pool.Target) (string, bool) {
	b := t.Backend()
	if b == nil {
		return "", false
	}
	if _, ok := b.(pooled); ok {
		if p.depth+1 >= maxPoolDepth {
			return "", false
		}
		v, _ := p.nested.LoadOrStore(b, fromBackend(b, p.depth+1))
		if up, ok := v.(Upstream); ok && up != nil {
			return up.Addr()
		}
		return "", false
	}
	cfg := b.Configuration()
	if cfg == nil || cfg.Host == "" || Refusing(cfg.Host) {
		return "", false
	}
	return cfg.Host, true
}

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
