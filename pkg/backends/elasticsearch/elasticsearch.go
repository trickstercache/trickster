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

// Package elasticsearch provides the Elasticsearch backend provider.
package elasticsearch

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	eso "github.com/trickstercache/trickster/v2/pkg/backends/elasticsearch/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
)

var _ backends.TimeseriesBackend = (*Client)(nil)

// Client implements the backend client interface for Elasticsearch.
type Client struct {
	backends.TimeseriesBackend
}

var _ types.NewBackendClientFunc = NewClient

// NewClient returns a new Client instance.
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, _ backends.Backends, _ types.Lookup,
) (backends.Backend, error) {
	if o != nil {
		o.FastForwardDisable = true
		if o.Elasticsearch == nil {
			o.Elasticsearch = eso.New()
		} else {
			_ = o.Elasticsearch.Initialize("")
		}
	}
	c := &Client{}
	b, err := backends.NewTimeseriesBackend(name, o, c.RegisterHandlers,
		router, cache, NewModeler())
	c.TimeseriesBackend = b
	return c, err
}

func (c *Client) elasticsearchOptions() *eso.Options {
	if c == nil || c.TimeseriesBackend == nil ||
		c.Configuration() == nil || c.Configuration().Elasticsearch == nil {
		return eso.New()
	}
	return c.Configuration().Elasticsearch
}
