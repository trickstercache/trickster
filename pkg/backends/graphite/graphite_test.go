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
	"errors"
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

func TestGraphiteClientInterfacing(t *testing.T) {
	// this test ensures the client will properly conform to the
	// Backend and TimeseriesBackend interfaces
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
