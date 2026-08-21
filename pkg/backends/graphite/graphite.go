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
// phases of trickster-data/todos/graphite-backend-implementation.md. Until
// then the inherited ParseTimeRangeQuery returns (nil, nil, false, nil), so no
// request reaches the DPC.
package graphite

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
)

var _ backends.TimeseriesBackend = (*Client)(nil)

// Client Implements the Proxy Client Interface
type Client struct {
	backends.TimeseriesBackend
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
	// the modeler is nil until the timeseries model lands (Phase 6); nothing
	// routes to the DPC before then, so it is never dereferenced.
	b, err := backends.NewTimeseriesBackend(name, o, c.RegisterHandlers, router,
		cache, nil)
	c.TimeseriesBackend = b
	return c, err
}
