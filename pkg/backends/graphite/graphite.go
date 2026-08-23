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

// Package graphite provides the Graphite backend provider.
//
// This is the proxy-only scaffolding: every Graphite endpoint is reverse
// proxied to the origin through the "/" prefix path. Render request parsing,
// resolution prediction, and the Delta Proxy Cache integration land in later
// phases of the Graphite implementation plan (trickster-data). Until
// then the inherited ParseTimeRangeQuery returns (nil, nil, false, nil), so no
// request reaches the DPC.
package graphite

import (
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/model"
	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
)

var _ backends.TimeseriesBackend = (*Client)(nil)

// Client Implements the Proxy Client Interface
type Client struct {
	backends.TimeseriesBackend
	resolver *resolution.Resolver
	learner  *resolution.Learner
	registry *resolution.Registry
	observer resolution.Observer
	tracers  *resolution.Tracers
}

// Resolver returns the step resolver for this backend
func (c *Client) Resolver() *resolution.Resolver { return c.resolver }

// Registry returns the resolution registry for this backend
func (c *Client) Registry() *resolution.Registry { return c.registry }

// Close stops background ladder learning
func (c *Client) Close() {
	if c.learner != nil {
		c.learner.Close()
	}
}

var _ types.NewBackendClientFunc = NewClient

// NewClient returns a new Client Instance
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, _ backends.Backends,
	_ types.Lookup,
) (backends.Backend, error) {
	if o != nil {
		// Fast Forward is not supported for Graphite (decision D10)
		o.FastForwardDisable = true
		if o.Graphite == nil {
			o.Graphite = gro.New()
		}
	}
	c := &Client{}
	b, err := backends.NewTimeseriesBackend(name, o, c.RegisterHandlers, router,
		cache, model.NewModeler())
	c.TimeseriesBackend = b
	if err != nil {
		return c, err
	}
	err = c.initResolution(name, o, cache)
	return c, err
}

// initResolution assembles the resolution registry, probe engine and
// resolver from the backend's options
func (c *Client) initResolution(name string, o *bo.Options, cache cache.Cache) error {
	var g *gro.Options
	if o != nil {
		g = o.Graphite
	}
	if g == nil {
		g = gro.New()
	}
	static, err := resolution.NewStatic(staticRules(g))
	if err != nil {
		return err
	}
	var store resolution.Store
	if g.ResolutionRegistry.Persist && cache != nil {
		store = cache
	}
	ro := resolution.RegistryOptions{
		TTL:            time.Duration(g.ResolutionRegistry.TTL),
		NegativeTTL:    time.Duration(g.ResolutionRegistry.NegativeTTL),
		NegativeTTLMax: gro.DefaultNegativeTTLMax,
		MaxEntries:     g.ResolutionRegistry.MaxEntries,
		KeyPrefix:      name + ".",
	}
	if ro.TTL <= 0 {
		ro.TTL = gro.DefaultRegistryTTL
	}
	if ro.NegativeTTL <= 0 {
		ro.NegativeTTL = gro.DefaultNegativeTTL
	}
	c.registry = resolution.NewRegistry(ro, store)
	// a change in static_retentions across a reload or restart invalidates
	// everything learned under the previous configuration
	if store != nil {
		key := name + ".graphite.resolution.config"
		h := configHash(g)
		if b, st, err := store.Retrieve(key); err != nil || st != status.LookupStatusHit || string(b) != h {
			if st == status.LookupStatusHit {
				c.registry.BumpGeneration()
			}
			_ = store.Store(key, []byte(h), 0)
		}
	}
	var timeout time.Duration
	if o != nil {
		timeout = time.Duration(o.Timeout)
	}
	origin := &resolution.Origin{Base: c.BaseUpstreamURL(), Client: c.HTTPClient(), Timeout: timeout}
	obs := newObserver(name)
	c.observer = obs
	c.tracers = &resolution.Tracers{}
	expander := &resolution.Expander{
		Origin: origin, Registry: c.registry, Observer: obs,
		TTL: time.Duration(g.FindCacheTTL),
	}
	if expander.TTL <= 0 {
		expander.TTL = gro.DefaultFindCacheTTL
	}
	prober := &resolution.Prober{Origin: origin, Observer: obs, Tracers: c.tracers}
	c.learner = &resolution.Learner{
		Prober: prober, Expander: expander, Registry: c.registry,
		Observer: obs, Concurrency: g.ResolutionRegistry.ProbeConcurrency,
		Budget: g.ResolutionRegistry.ProbeBudget, Name: name,
	}
	c.resolver = &resolution.Resolver{
		Registry: c.registry, Expander: expander,
		Learner: c.learner, Static: static, Observer: obs,
	}
	return nil
}

func staticRules(g *gro.Options) [][2]string {
	rules := make([][2]string, len(g.StaticRetentions))
	for i, r := range g.StaticRetentions {
		rules[i] = [2]string{r.Pattern, r.Retentions}
	}
	return rules
}

func configHash(g *gro.Options) string {
	h := sha1.New()
	for _, r := range g.StaticRetentions {
		h.Write([]byte(r.Pattern))
		h.Write([]byte{0})
		h.Write([]byte(r.Retentions))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
