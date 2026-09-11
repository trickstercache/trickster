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
package graphite

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/model"
	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

var _ backends.TimeseriesBackend = (*Client)(nil)

// Client Implements the Proxy Client Interface
type Client struct {
	backends.TimeseriesBackend
	resolver       *resolution.Resolver
	learner        *resolution.Learner
	registry       *resolution.Registry
	observer       resolution.Observer
	tracers        *resolution.Tracers
	configuredLoc  *time.Location
	tzCache        tzCache
	identityOnce   sync.Once
	identityDigest string
	synthIDs       atomic.Pointer[[2]string]
	name           string
	persist        bool
	configHash     string
	registryTTL    time.Duration
	// timeNow is the request reference clock, replaceable in tests
	timeNow func() time.Time
}

// Resolver returns the step resolver for this backend
func (c *Client) Resolver() *resolution.Resolver { return c.resolver }

// Registry returns the resolution registry for this backend
func (c *Client) Registry() *resolution.Registry { return c.registry }

// SetCache attaches the backend's cache; caches are wired after construction,
// so this is also where a persisted resolution registry is connected
func (c *Client) SetCache(cc cache.Cache) {
	c.TimeseriesBackend.SetCache(cc)
	if cc == nil || !c.persist || c.registry == nil {
		return
	}
	c.registry.SetStore(cc)
	c.checkPersistedConfig(cc)
}

// a static_retentions change across a reload or restart can change what a
// leaf resolves to, so bump the generation and record the new configuration
func (c *Client) checkPersistedConfig(store resolution.Store) {
	if store == nil || c.registry == nil {
		return
	}
	key := c.name + ".graphite.resolution.config"
	h := c.configHash + "." + c.effectiveIdentityDigest()
	b, st, err := store.Retrieve(key)
	if err == nil && st == status.LookupStatusHit && string(b) == h {
		return
	}
	if st == status.LookupStatusHit {
		c.registry.BumpGeneration()
	}
	// stored for as long as the entries it guards: a cache that refuses a zero
	// TTL (the filesystem provider does) would otherwise never record it
	_ = store.Store(key, []byte(h), c.registryTTL)
}

// selects the path config a synthetic GET to path is issued under, with the
// same path and method precedence the proxy router applies
func (c *Client) synthPathConfig(path string) *po.Options {
	o := c.Configuration()
	if o == nil {
		return nil
	}
	return o.Paths.Match(http.MethodGet, path)
}

func (c *Client) synthPathOptions(path string) (map[string]string, map[string]string) {
	pc := c.synthPathConfig(path)
	if pc == nil {
		return nil, nil
	}
	return pc.RequestHeaders, pc.RequestParams
}

// paths are immutable once routes are registered and a reload builds a new
// Client, so the /render and /metrics/expand digests are resolved just once
func (c *Client) synthIdentities() (render, expand string) {
	if p := c.synthIDs.Load(); p != nil {
		return p[0], p[1]
	}
	ids := [2]string{
		c.synthPathConfig(renderPath).IdentityKeyPart(),
		c.synthPathConfig(expandPath).IdentityKeyPart(),
	}
	c.synthIDs.Store(&ids)
	return ids[0], ids[1]
}

// reports whether accelerating a request served under pc would mix upstream
// identities with the backend-wide synthetic resolution requests
func (c *Client) resolutionIdentityMismatch(pc *po.Options) (string, bool) {
	render, expand := c.synthIdentities()
	if render != expand {
		return "render_expand_identity", true
	}
	if pc.IdentityKeyPart() != render {
		return "request_identity", true
	}
	return "", false
}

func (c *Client) graphiteOptions() *gro.Options {
	if o := c.Configuration(); o != nil && o.Graphite != nil {
		return o.Graphite
	}
	return gro.New()
}

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
		// Fast Forward is not supported for Graphite
		o.FastForwardDisable = true
		if o.Graphite == nil {
			o.Graphite = gro.New()
		}
	}
	c := &Client{timeNow: time.Now}
	b, err := backends.NewTimeseriesBackend(name, o, c.RegisterHandlers, router,
		cache, model.NewModeler())
	c.TimeseriesBackend = b
	if err != nil {
		return c, err
	}
	err = c.initResolution(name, o, cache)
	return c, err
}

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
	// a configured origin credential rides on every path's request_headers,
	// so keys, purge, synthetic identity and rotation all key on it uniformly
	auth, err := originAuthHeader(g)
	if err != nil {
		return fmt.Errorf("graphite backend %s: %w", name, err)
	}
	if o != nil {
		if err := injectOriginAuth(o.Paths, auth); err != nil {
			return fmt.Errorf("graphite backend %s: %w", name, err)
		}
	}
	c.configuredLoc = time.UTC
	if g.TimeZone != "" && g.TimeZone != "UTC" {
		loc, err := time.LoadLocation(g.TimeZone)
		if err != nil {
			return fmt.Errorf("graphite backend %s: invalid time_zone %q: %w", name, g.TimeZone, err)
		}
		c.configuredLoc = loc
	}
	c.name = name
	c.persist = g.ResolutionRegistry.Persist
	c.configHash = configHash(g)
	var store resolution.Store
	if c.persist && cache != nil {
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
	obs := newObserver(name)
	c.observer = obs
	c.registry = resolution.NewRegistry(ro, store)
	c.registry.Observer = obs
	c.registryTTL = ro.TTL
	c.checkPersistedConfig(store)
	var timeout time.Duration
	if o != nil {
		timeout = time.Duration(o.Timeout)
	}
	origin := &resolution.Origin{
		Base: c.BaseUpstreamURL(), Client: c.HTTPClient(), Timeout: timeout,
		PathOptions: c.synthPathOptions,
	}
	c.tracers = &resolution.Tracers{}
	expander := &resolution.Expander{
		Origin: origin, Registry: c.registry, Observer: obs,
		TTL:          time.Duration(g.FindCacheTTL),
		MaxLeaves:    g.MaxExpandedLeaves,
		MaxLeafBytes: g.MaxExpansionBytes,
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
	h := sha256.New()
	for _, r := range g.StaticRetentions {
		h.Write([]byte(r.Pattern))
		h.Write([]byte{0})
		h.Write([]byte(r.Retentions))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Client) effectiveIdentityDigest() string {
	c.identityOnce.Do(func() {
		h := sha256.New()
		var sizes [binary.MaxVarintLen64]byte
		writeStr := func(s string) {
			// length-prefixed, so no content can shift an element boundary
			n := binary.PutUvarint(sizes[:], uint64(len(s)))
			h.Write(sizes[:n])
			h.Write([]byte(s))
		}
		o := c.Configuration()
		if o != nil {
			// hashed directly because the digest is recorded at SetCache,
			// before the default paths are overlaid and injected
			if auth, err := originAuthHeader(o.Graphite); err == nil && auth != "" {
				writeStr("origin_authorization")
				writeStr(auth)
			}
			for _, pc := range o.Paths {
				ik := pc.IdentityKeyPart()
				if ik == "" {
					continue
				}
				writeStr(pc.Path)
				writeStr(ik)
			}
		}
		c.identityDigest = hex.EncodeToString(h.Sum(nil))[:16]
	})
	return c.identityDigest
}
