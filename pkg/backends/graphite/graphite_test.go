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

package graphite

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	cstatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/testutil/graphite/mockserver"
)

func TestGraphiteClientInterfacing(t *testing.T) {
	c, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	var oc backends.Backend = c
	var tc backends.TimeseriesBackend = c.(*Client)
	if oc.Name() != "test" {
		t.Errorf("expected %s got %s", "test", oc.Name())
	}
	if tc.Name() != "test" {
		t.Errorf("expected %s got %s", "test", tc.Name())
	}
	if _, ok := oc.(backends.MergeableTimeseriesBackend); ok {
		t.Error("graphite must not register as a mergeable timeseries backend (D9)")
	}
}

func TestNewClient(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))

	conf, err := config.Load([]string{
		"-origin-url", "http://1",
		"-provider", "test",
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Errorf("Could not find default configuration")
	}

	o := &bo.Options{Provider: "TEST_CLIENT"}
	c, err := NewClient("default", o, nil, cache, nil, nil)
	if err != nil {
		t.Error(err)
	}

	if c.Name() != "default" {
		t.Errorf("expected %s got %s", "default", c.Name())
	}

	if c.Cache().Configuration().Provider != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Cache().Configuration().Provider)
	}

	if c.Configuration().Provider != "TEST_CLIENT" {
		t.Errorf("expected %s got %s", "TEST_CLIENT", c.Configuration().Provider)
	}

	if !o.FastForwardDisable {
		t.Error("expected fast forward to be disabled")
	}

	if o.Graphite == nil {
		t.Error("expected graphite options to be initialized")
	}

	// a render with no series format (graphite-web defaults to png) must be
	// declined with canOPC so the DPC hands it to the Object Proxy Cache
	r, _ := http.NewRequest(http.MethodGet, "http://1/render?target=a.b&from=-1h", nil)
	trq, rlo, canOPC, err := c.(*Client).ParseTimeRangeQuery(r)
	if trq == nil || rlo != nil || !canOPC || !errors.Is(err, ErrNotAccelerable) {
		t.Errorf("expected (trq, nil, true, ErrNotAccelerable) got (%v, %v, %t, %v)", trq, rlo, canOPC, err)
	}
}

func TestSetCacheAttachesRegistryStore(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "graphite"})
	if err != nil {
		t.Fatal(err)
	}
	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache := caches["default"]

	o := bo.New()
	o.Graphite = gro.New()
	b, err := NewClient("g6", o, nil, nil, nil, nil) // no cache yet, as at startup
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	defer c.Close()
	ladder, err := resolution.NewLadder([]resolution.Rung{
		{Step: 10 * time.Second, MaxAge: 6 * time.Hour},
		{Step: time.Minute, MaxAge: 7 * 24 * time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	// nothing to persist to yet: learning still works, in memory only
	key, err := c.Registry().SetLadder("x.y", ladder)
	if err != nil {
		t.Fatal(err)
	}
	if _, st, _ := cache.Retrieve("g6.graphite.resolution.ladder." + key); st == cstatus.LookupStatusHit {
		t.Fatal("nothing may be written before a cache is attached")
	}

	c.SetCache(cache)
	if err := c.Registry().SetLeaf("x.y", key, resolution.Exact); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Registry().SetLadder("x.y", ladder); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"g6.graphite.resolution.leaf.x.y",
		"g6.graphite.resolution.ladder." + key, "g6.graphite.resolution.config"} {
		if _, st, err := cache.Retrieve(k); err != nil || st != cstatus.LookupStatusHit {
			t.Errorf("%s was not persisted (status %v, err %v)", k, st, err)
		}
	}
	// a fresh client over the same cache reads what the first one learned
	b2, err := NewClient("g6", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c2 := b2.(*Client)
	defer c2.Close()
	c2.SetCache(cache)
	if k, conf, ok := c2.Registry().Leaf("x.y"); !ok || k != key || conf != resolution.Exact {
		t.Errorf("a restarted backend did not restore the leaf: %q %v %t", k, conf, ok)
	}
}

func TestResolutionWiring(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "graphite"})
	if err != nil {
		t.Fatal(err)
	}
	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache := caches["default"]

	o := bo.New()
	o.Graphite = gro.New()
	o.Graphite.StaticRetentions = []gro.StaticRetention{{Pattern: `^dev\.fast\.`, Retentions: "10s:6h,1m:7d"}}
	b, err := NewClient("g1", o, nil, cache, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	if c.Resolver() == nil || c.Registry() == nil || c.Resolver().Static.Len() != 1 {
		t.Fatal("resolver not wired")
	}
	gen := c.Registry().Generation()
	// the same configuration does not bump the persisted generation
	b2, _ := NewClient("g1", o, nil, cache, nil, nil)
	if b2.(*Client).Registry().Generation() != gen {
		t.Error("unchanged static_retentions must not bump the generation")
	}
	// a changed static_retentions does
	o2 := bo.New()
	o2.Graphite = gro.New()
	o2.Graphite.StaticRetentions = []gro.StaticRetention{{Pattern: `^dev\.`, Retentions: "1m:1d"}}
	b3, _ := NewClient("g1", o2, nil, cache, nil, nil)
	if b3.(*Client).Registry().Generation() != gen+1 {
		t.Errorf("changed static_retentions must bump the generation: %d -> %d", gen, b3.(*Client).Registry().Generation())
	}
	c.Close()
	b2.(*Client).Close()
	b3.(*Client).Close()

	// persistence off, zero TTLs fall back to defaults, nil options tolerated
	o3 := bo.New()
	o3.Graphite = gro.New()
	o3.Graphite.ResolutionRegistry.Persist = false
	o3.Graphite.ResolutionRegistry.TTL = 0
	o3.Graphite.ResolutionRegistry.NegativeTTL = 0
	o3.Graphite.FindCacheTTL = 0
	if _, err := NewClient("g2", o3, nil, cache, nil, nil); err != nil {
		t.Error(err)
	}
	if b, err := NewClient("g3", nil, nil, nil, nil, nil); err != nil || b.(*Client).Resolver() == nil {
		t.Error("nil options must still wire a resolver")
	}
	// an invalid static rule is a configuration error
	o4 := bo.New()
	o4.Graphite = gro.New()
	o4.Graphite.StaticRetentions = []gro.StaticRetention{{Pattern: `(`, Retentions: "10s:6h"}}
	if _, err := NewClient("g4", o4, nil, cache, nil, nil); err == nil {
		t.Error("expected an error for an invalid static_retentions pattern")
	}
}

func TestSyntheticRequestsCarryPathOptions(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][]string{} // path -> observed auth/param evidence
	origin := mockserver.New()
	t.Cleanup(origin.Close)
	origin.Add("dev.fast.cpu.host01.percent", "10s:6h,60s:7d")
	inner := origin.Server.Config.Handler
	origin.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = append(seen[r.URL.Path],
			r.Header.Get("Authorization")+"|"+r.URL.Query().Get("local"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})

	o := bo.New()
	o.Graphite = gro.New()
	u, _ := url.Parse(origin.URL)
	o.Scheme, o.Host = u.Scheme, u.Host
	b, err := NewClient("auth-test", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	t.Cleanup(c.Close)
	o.HTTPClient = c.HTTPClient()
	// the daemon overlays path configs after construction; mirror that,
	// then attach the configured upstream options to every path
	o.Paths = c.DefaultPathConfigs(o)
	for _, pc := range o.Paths {
		pc.RequestHeaders = map[string]string{"Authorization": "Bearer test-key"}
		pc.RequestParams = map[string]string{"local": "1"}
	}

	// learning issues probes (/render) and existence checks; expansion
	// hits /metrics/expand. All must authenticate.
	if _, err := c.learner.Learn(context.Background(), "dev.fast.cpu.host01.percent", nil); err != nil {
		t.Fatalf("learning against an authenticated origin failed: %v", err)
	}
	if _, _, err := c.resolver.Expander.Expand(context.Background(), "dev.fast.cpu.*.percent"); err != nil {
		t.Fatalf("expansion against an authenticated origin failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for path, evs := range seen {
		for _, ev := range evs {
			if ev != "Bearer test-key|1" {
				t.Errorf("%s: synthetic request missing configured options: %q", path, ev)
			}
		}
	}
	if len(seen["/render"]) == 0 || len(seen["/metrics/expand"]) == 0 {
		t.Fatalf("vacuous: paths seen %v", func() []string {
			out := make([]string, 0, len(seen))
			for k := range seen {
				out = append(out, k)
			}
			return out
		}())
	}
}

func TestDocumentedCredentialConfig(t *testing.T) {
	// the documented example's origin_username is 'metrics'
	cred := "Basic " + base64.StdEncoding.EncodeToString([]byte("metrics:doc-pass"))
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "graphite.md"))
	if err != nil {
		t.Fatal(err)
	}
	var block string
	for part := range strings.SplitSeq(string(doc), "```") {
		if strings.HasPrefix(part, "yaml") && strings.Contains(part, "backends:") &&
			strings.Contains(part, "origin_username:") {
			block = strings.TrimPrefix(part, "yaml")
			break
		}
	}
	if block == "" {
		t.Fatal("docs/graphite.md no longer contains the credential example")
	}

	var mu sync.Mutex
	seen := map[string]bool{}
	origin := mockserver.New()
	t.Cleanup(origin.Close)
	origin.Add("dev.fast.cpu.host01.percent", "10s:6h,60s:7d")
	inner := origin.Server.Config.Handler
	origin.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != cred {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		seen[r.URL.Path] = true
		mu.Unlock()
		inner.ServeHTTP(w, r)
	})

	block = strings.ReplaceAll(block, "'http://graphite.example.com:80'", "'"+origin.URL+"'")
	block = strings.ReplaceAll(block, "'<the origin password>'", "'doc-pass'")
	cfgFile := filepath.Join(t.TempDir(), "trickster.yaml")
	if err := os.WriteFile(cfgFile, []byte(block), 0o600); err != nil {
		t.Fatal(err)
	}
	conf, err := config.Load([]string{"-config", cfgFile})
	if err != nil {
		t.Fatalf("the documented YAML must load: %v", err)
	}
	o := conf.Backends["graphite1"]
	if o == nil {
		t.Fatal("documented backend not present after load")
	}
	b, err := NewClient("graphite1", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	t.Cleanup(c.Close)
	// the daemon overlays the documented paths onto the defaults at
	// route registration; mirror that
	o.Paths = c.DefaultPathConfigs(o).Overlay(o.Paths)

	if detail, mismatched := c.resolutionIdentityMismatch(o.Paths.Match(http.MethodGet, "/render")); mismatched {
		t.Fatalf("the documented config must yield one resolution identity, got %s", detail)
	}
	if _, err := c.learner.Learn(context.Background(), "dev.fast.cpu.host01.percent", nil); err != nil {
		t.Fatalf("probe with the documented credential failed: %v", err)
	}
	if _, _, err := c.resolver.Expander.Expand(context.Background(), "dev.fast.cpu.*.percent"); err != nil {
		t.Fatalf("expansion with the documented credential failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !seen["/render"] || !seen["/metrics/expand"] {
		t.Fatalf("vacuous: authenticated paths seen %v", seen)
	}
}

func TestSynthPathOptionsMethodAware(t *testing.T) {
	c := newTestClient(t, nil)
	c.Configuration().Paths = po.List{
		{Path: "/render", Methods: []string{http.MethodPost},
			RequestHeaders: map[string]string{"Authorization": "Basic tenant-b"}},
		{Path: "/render", Methods: []string{http.MethodGet},
			RequestHeaders: map[string]string{"Authorization": "Basic tenant-a"}},
	}
	h, _ := c.synthPathOptions("/render")
	if h["Authorization"] != "Basic tenant-a" {
		t.Fatalf("synthetic GET must select the GET path config, got %v", h)
	}
}

func TestEffectiveIdentityRotation(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "graphite"})
	if err != nil {
		t.Fatal(err)
	}
	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache := caches["default"]

	build := func(cred string) *Client {
		o := bo.New()
		o.Graphite = gro.New()
		b, err := NewClient("rot", o, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		c := b.(*Client)
		t.Cleanup(c.Close)
		o.Paths = c.DefaultPathConfigs(o)
		for _, pc := range o.Paths {
			if pc.Path == "/render" {
				pc.RequestHeaders = map[string]string{"Authorization": cred}
			}
		}
		c.SetCache(cache)
		return c
	}

	a := build("Bearer tenant-a")
	digestA := a.effectiveIdentityDigest()
	// tenant A learns a ladder, persisted through the cache
	ladder, err := resolution.NewLadder([]resolution.Rung{
		{Step: 10 * time.Second, MaxAge: 6 * time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	key, err := a.Registry().SetLadder("t.leaf", ladder)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Registry().SetLeaf("t.leaf", key, resolution.Exact); err != nil {
		t.Fatal(err)
	}
	genA := a.Registry().Generation()

	// the credential rotates to tenant B: a different digest, a bumped
	// generation, and no restored resolution state
	b := build("Bearer tenant-b")
	if b.effectiveIdentityDigest() == digestA {
		t.Fatal("different pinned credentials must yield different digests")
	}
	if b.Registry().Generation() == genA {
		t.Fatal("a rotated credential must bump the persisted generation")
	}
	if _, _, ok := b.Registry().Leaf("t.leaf"); ok {
		t.Fatal("tenant B restored tenant A's leaf binding")
	}
	// and the cache key elements rotate with the identity: the digest is
	// what separates the two identities' cached series
	if digestA == "" || b.effectiveIdentityDigest() == "" {
		t.Fatal("digests must be non-empty")
	}
	// an unchanged configuration keeps its digest and generation
	c2 := build("Bearer tenant-b")
	if c2.effectiveIdentityDigest() != b.effectiveIdentityDigest() {
		t.Fatal("an unchanged identity must keep its digest")
	}
	if c2.Registry().Generation() != b.Registry().Generation() {
		t.Fatal("an unchanged identity must not bump the generation")
	}
}
