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

// Package grafana provides a Grafana origin backend provider.
package grafana

import (
	"net/http"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"

	"golang.org/x/sync/singleflight"
)

var (
	_ backends.Backend           = (*Client)(nil)
	_ types.NewBackendClientFunc = NewClient
)

// Client proxies Grafana normally and dispatches recognized data source proxy
// paths through the corresponding Trickster backend provider.
type Client struct {
	backends.Backend

	clients   backends.Backends
	factories types.Lookup

	mu          sync.RWMutex
	dataSources map[string]dataSourceCacheEntry
	dispatchers map[dataSourceDispatcherKey]*dataSourceDispatcher
	lookupGroup singleflight.Group
	preloadOnce sync.Once
}

// NewClient returns a new Grafana origin client.
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, clients backends.Backends, factories types.Lookup,
) (backends.Backend, error) {
	c := &Client{
		clients:     clients,
		factories:   factories,
		dataSources: make(map[string]dataSourceCacheEntry),
		dispatchers: make(map[dataSourceDispatcherKey]*dataSourceDispatcher),
	}
	b, err := backends.New(name, o, c.RegisterHandlers, router, cache)
	c.Backend = b
	return c, err
}
