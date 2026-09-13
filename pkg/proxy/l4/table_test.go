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

package l4

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

func originBackend(t *testing.T, name, addr string) backends.Backend {
	t.Helper()
	o := bo.New()
	o.OriginURL = "tcp://" + addr
	if err := o.Initialize(name); err != nil {
		t.Fatal(err)
	}
	b, err := backends.New(name, o, nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type pooledBackend struct {
	backends.Backend
	p pool.Pool
}

func (b *pooledBackend) Pool() pool.Pool { return b.p }

func newPool(t *testing.T, members ...*pool.Target) pool.Pool {
	t.Helper()
	p := pool.New(members, int(healthcheck.StatusUnchecked))
	t.Cleanup(p.Stop)
	return p
}

func member(b backends.Backend, weight int, status int32) *pool.Target {
	st := healthcheck.NewStatus(b.Name(), "", "", status, time.Time{}, nil)
	return pool.NewWeightedTarget(http.NotFoundHandler(), st, b, weight)
}

func pooledOf(t *testing.T, name string, members ...*pool.Target) backends.Backend {
	t.Helper()
	b, err := backends.New(name, bo.New(), nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return &pooledBackend{Backend: b, p: newPool(t, members...)}
}

func TestTableLookup(t *testing.T) {
	tbl := NewTable()
	if !tbl.Empty() {
		t.Fatal("new table is not empty")
	}
	exact, one, any, all := Static("exact"), Static("one"), Static("any"), Static("all")
	for host, up := range map[string]Upstream{
		"Shop.Example.com.": exact, "*.example.com": one, "**.wild.example.com": any, "": all,
	} {
		if err := tbl.Add(host, up); err != nil {
			t.Fatal(err)
		}
	}
	if tbl.Empty() {
		t.Fatal("populated table reports empty")
	}
	cases := map[string]Upstream{
		"shop.example.com":        exact,
		"SHOP.example.com.":       exact,
		"api.example.com":         one,
		"a.b.example.com":         all,
		"deep.wild.example.com":   any,
		"a.b.c.wild.example.com":  any,
		"other.org":               all,
		"":                        all,
		"wild.example.com":        one,
		"example.com":             all,
		"notexample.com":          all,
		"sub.notwild.example.com": all,
		"x.wild.example.com.":     any,
	}
	for host, want := range cases {
		if got := tbl.Lookup(host); got != want {
			t.Errorf("Lookup(%q) = %v, want %v", host, got, want)
		}
	}
	for _, host := range []string{"shop.example.com", "*.example.com", "**.wild.example.com"} {
		if err := tbl.Add(host, exact); !errors.Is(err, ErrDuplicateHost) {
			t.Errorf("Add(%q) again = %v, want ErrDuplicateHost", host, err)
		}
	}
	if err := tbl.Add("", exact); !errors.Is(err, ErrDuplicateCatchAll) {
		t.Errorf("second catch-all = %v", err)
	}
	if err := tbl.Add("bad host", exact); err == nil {
		t.Error("a hostname with whitespace was routed")
	}
	var nilTable *Table
	if nilTable.Lookup("x") != nil || !nilTable.Empty() {
		t.Error("nil table must route nothing")
	}
	strict := NewTable()
	_ = strict.Add("only.example.com", exact)
	if strict.Lookup("other.example.com") != nil || strict.Lookup("") != nil {
		t.Error("a table without a catch-all routed an unknown host")
	}
}

func TestStaticAndFromBackend(t *testing.T) {
	if got, ok := Static("h:1").Addr(); !ok || got != "h:1" {
		t.Errorf("Static = %v, %v", got, ok)
	}
	// an address under the reserved .invalid domain can never resolve, so it is refused without
	// a lookup, as a backend is whose origin names one
	for _, addr := range []string{"unresolved.kgw.invalid:1", "x.INVALID.:9", "x.invalid"} {
		if !Refusing(addr) {
			t.Errorf("Refusing(%q) = false", addr)
		}
		if _, ok := Static(addr).Addr(); ok {
			t.Errorf("Static(%q) dials", addr)
		}
	}
	if Refusing("invalid.example.com:1") || Refusing("10.0.0.1:1") {
		t.Error("a resolvable address is refused")
	}
	if _, ok := FromBackend(originBackend(t, "gone", "unresolved.kgw.invalid:1")).Addr(); ok {
		t.Error("a backend under .invalid dials")
	}
	if FromBackend(nil) != nil {
		t.Error("nil backend yields an upstream")
	}
	hostless, err := backends.New("hostless", bo.New(), nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if FromBackend(hostless) != nil {
		t.Error("a backend without an origin host yields an upstream")
	}
	if got, ok := FromBackend(originBackend(t, "o", "10.0.0.1:9000")).Addr(); !ok || got != "10.0.0.1:9000" {
		t.Errorf("origin backend = %v, %v", got, ok)
	}
}

func TestPoolUpstreamRotatesWithWeights(t *testing.T) {
	a := originBackend(t, "a", "10.0.0.1:1")
	b := originBackend(t, "b", "10.0.0.2:1")
	down := originBackend(t, "down", "10.0.0.3:1")
	up := FromBackend(pooledOf(t, "alb",
		member(a, 2, healthcheck.StatusPassing),
		member(b, 1, healthcheck.StatusPassing),
		member(down, 1, healthcheck.StatusFailing)))
	counts := make(map[string]int)
	for range 6 {
		addr, ok := up.Addr()
		if !ok {
			t.Fatal("a pool with healthy members refused")
		}
		counts[addr]++
	}
	if counts["10.0.0.1:1"] != 4 || counts["10.0.0.2:1"] != 2 || counts["10.0.0.3:1"] != 0 {
		t.Errorf("weighted rotation = %v", counts)
	}
	// an even pool is a plain rotation
	even := FromBackend(pooledOf(t, "even", member(a, 1, healthcheck.StatusPassing),
		member(b, 1, healthcheck.StatusPassing)))
	first, _ := even.Addr()
	second, _ := even.Addr()
	third, _ := even.Addr()
	if first == second || first != third {
		t.Errorf("rotation: %v, %v, %v", first, second, third)
	}
	empty := FromBackend(pooledOf(t, "empty"))
	if got, ok := empty.Addr(); ok {
		t.Errorf("empty pool = %v", got)
	}
	holderless := &pooledBackend{Backend: hostlessBackend(t)}
	if got, ok := FromBackend(holderless).Addr(); ok {
		t.Errorf("nil pool = %v", got)
	}
	// a member with nothing to dial refuses its own share rather than passing it on
	mixed := FromBackend(pooledOf(t, "mixed", member(a, 1, healthcheck.StatusPassing),
		member(hostlessBackend(t), 1, healthcheck.StatusPassing)))
	var refused int
	for range 4 {
		if _, ok := mixed.Addr(); !ok {
			refused++
		}
	}
	if refused != 2 {
		t.Errorf("refused %d of 4, want the hostless member's share", refused)
	}
}

func hostlessBackend(t *testing.T) backends.Backend {
	t.Helper()
	b, err := backends.New("hostless", bo.New(), nil, http.NotFoundHandler(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPoolUpstreamFollowsNestedPools(t *testing.T) {
	// an outer pool of two inner pools, as a weighted rule in endpoint mode compiles to
	inner1 := pooledOf(t, "inner1", member(originBackend(t, "a", "10.1.0.1:1"), 1, healthcheck.StatusPassing),
		member(originBackend(t, "b", "10.1.0.2:1"), 1, healthcheck.StatusPassing))
	inner2 := pooledOf(t, "inner2", member(originBackend(t, "c", "10.2.0.1:1"), 1, healthcheck.StatusPassing))
	outer := FromBackend(pooledOf(t, "outer", member(inner1, 1, healthcheck.StatusPassing),
		member(inner2, 1, healthcheck.StatusPassing), member(hostlessBackend(t), 1, healthcheck.StatusPassing)))
	seen := make(map[string]int)
	var refused int
	for range 6 {
		addr, ok := outer.Addr()
		if !ok {
			refused++
			continue
		}
		seen[addr]++
	}
	if refused != 2 || seen["10.2.0.1:1"] != 2 || seen["10.1.0.1:1"] != 1 || seen["10.1.0.2:1"] != 1 {
		t.Errorf("outer rotation = %v with %d refused; want each inner pool its share, rotating within",
			seen, refused)
	}
	// a pool nested beyond the depth bound is not followed
	deep := FromBackend(pooledOf(t, "l0", member(pooledOf(t, "l1", member(pooledOf(t, "l2",
		member(originBackend(t, "z", "10.9.0.1:1"), 1, healthcheck.StatusPassing)),
		1, healthcheck.StatusPassing)), 1, healthcheck.StatusPassing)))
	if got, ok := deep.Addr(); ok {
		t.Errorf("a pool three deep was followed: %v", got)
	}
}
