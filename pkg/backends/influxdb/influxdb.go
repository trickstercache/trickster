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

// Package influxdb provides the InfluxDB Backend provider
package influxdb

import (
	"net/http"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/flight"
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
	c cache.Cache, _ backends.Backends, _ types.Lookup,
) (backends.Backend, error) {
	if o != nil {
		o.FastForwardDisable = true
	}
	client := &Client{}
	b, err := backends.NewTimeseriesBackend(name, o, client.RegisterHandlers,
		router, c, NewModeler())
	client.TimeseriesBackend = b
	return client, err
}

// flightCacheAdapter adapts a Trickster cache.Cache to the flight.Cache
// interface (Get/Set vs Retrieve/Store). Without this, Flight SQL requests
// pass through to upstream unconditionally and the caching path advertised
// in docs/influxdb.md never engages.
type flightCacheAdapter struct{ c cache.Cache }

func newFlightCache(c cache.Cache) flight.Cache {
	if c == nil {
		return nil
	}
	return &flightCacheAdapter{c: c}
}

func (a *flightCacheAdapter) Get(key string) ([]byte, bool) {
	b, _, err := a.c.Retrieve(key)
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}

func (a *flightCacheAdapter) Set(key string, data []byte, ttl time.Duration) {
	_ = a.c.Store(key, data, ttl)
}
