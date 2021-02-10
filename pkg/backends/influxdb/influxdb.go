/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/tricksterproxy/trickster/pkg/backends"
	bo "github.com/tricksterproxy/trickster/pkg/backends/options"
	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

var _ backends.Backend = (*Client)(nil)

// Client Implements the Proxy Client Interface
type Client struct {
	backends.TimeseriesBackend

	healthURL        *url.URL
	healthHeaders    http.Header
	healthMethod     string
	healthBody       io.Reader
	healthHeaderLock sync.Mutex
}

// NewClient returns a new Client Instance
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, modeler *timeseries.Modeler) (backends.TimeseriesBackend, error) {
	if o != nil {
		o.FastForwardDisable = true
	}
	c := &Client{}
	b, err := backends.NewTimeseriesBackend(name, o, c.RegisterHandlers, router, cache, modeler)
	c.TimeseriesBackend = b
	return c, err
}
