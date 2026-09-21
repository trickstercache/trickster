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

// Package static provides the Static File Server backend provider, which
// serves the content of a local directory rather than proxying to an origin.
package static

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

// Client implements the Backend interface for the Static File Server
type Client struct {
	backends.Backend
	server  *server
	handler http.Handler
}

var _ types.NewBackendClientFunc = NewClient

// NewClient returns a new Static File Server client. Its fileserver cache stays
// idle until Start is called, so a client built only to validate holds nothing.
func NewClient(name string, o *bo.Options, router http.Handler,
	_ cache.Cache, _ backends.Backends, _ types.Lookup,
) (backends.Backend, error) {
	if o == nil || o.Static == nil {
		return nil, ErrMissingOptions
	}
	s, err := newServer(name, o.Static, o.CompressibleTypes)
	if err != nil {
		return nil, err
	}
	c := &Client{server: s, handler: s}
	if o.Static.DirectoryListing {
		c.handler = withDirectoryListing(s, c.handler)
	}
	c.handler = withResponseHeaders(o.Static.ResponseHeaders, c.handler)
	b, err := backends.New(name, o, c.RegisterHandlers, router, nil)
	c.Backend = b
	return c, err
}

// Start enables the client's fileserver cache and its filesystem watcher
func (c *Client) Start() {
	c.server.start()
}

// Stop disables the client's fileserver cache and its filesystem watcher
func (c *Client) Stop() {
	c.server.stop()
}

// StartClients starts every Static File Server client in the collection. It is called with
// all of a configuration's clients, so that what the last one's left behind can be cleaned up.
func StartClients(clients backends.Backends) {
	for _, c := range clients {
		if sc, ok := c.(*Client); ok {
			sc.Start()
		}
	}
	sweepSeries()
}

// StopClients stops every Static File Server client in the collection
func StopClients(clients backends.Backends) {
	for _, c := range clients {
		if sc, ok := c.(*Client); ok {
			sc.Stop()
		}
	}
}

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *Client) DefaultPathConfigs(_ *bo.Options) po.List {
	return po.List{
		{
			Path:          "/",
			HandlerName:   providers.Static,
			Methods:       methods.AllHTTPMethods(),
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: matching.PathMatchNamePrefix,
		},
	}
}

// RegisterHandlers registers the client's handlers with the backend
func (c *Client) RegisterHandlers(handlers.Lookup) {
	c.Backend.RegisterHandlers(
		handlers.Lookup{
			providers.Static: c.handler,
		},
	)
}
