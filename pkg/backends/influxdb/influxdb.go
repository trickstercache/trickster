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

	"github.com/trickstercache/trickster/v2/pkg/backends"
	modelflux "github.com/trickstercache/trickster/v2/pkg/backends/influxdb/model"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
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
	c cache.Cache, _ backends.Backends, _ types.Lookup) (backends.Backend, error) {
	if o != nil {
		o.FastForwardDisable = true
	}
	cli := &Client{}
	b, err := backends.NewTimeseriesBackend(name, o, cli.RegisterHandlers,
		router, c, modelflux.NewModeler())
	cli.TimeseriesBackend = b
	return cli, err
}
