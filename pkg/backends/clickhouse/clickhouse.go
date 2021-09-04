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

// Package clickhouse provides the ClickHouse backend provider
package clickhouse

import (
	"net/http"
	"net/url"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	modelch "github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/model"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
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
	_ types.Lookup) (backends.Backend, error) {
	if o != nil {
		o.FastForwardDisable = true
	}
	c := &Client{}
	b, err := backends.NewTimeseriesBackend(name, o, c.RegisterHandlers, router, cache, modelch.NewModeler())
	c.TimeseriesBackend = b
	return c, err
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {

	var sqlQuery string
	var qi url.Values
	isBody := methods.HasBody(r.Method)
	if isBody {
		sqlQuery = string(request.GetBody(r))
	} else {
		qi = r.URL.Query()
		if p, ok := qi[upQuery]; ok {
			sqlQuery = p[0]
		} else {
			return nil, nil, false, errors.MissingURLParam(upQuery)
		}
	}

	trq, ro, canOPC, err := parse(sqlQuery)
	if err != nil {
		return nil, nil, canOPC, err
	}

	var bf time.Duration
	res := request.GetResources(r)
	if res == nil {
		// 60-second default backfill tolerance for ClickHouse
		bf = 60 * time.Second
	} else {
		bf = res.BackendOptions.BackfillTolerance
	}

	if trq.BackfillTolerance == 0 {
		trq.BackfillTolerance = bf
	}
	trq.BackfillToleranceNS = bf.Nanoseconds()

	trq.TemplateURL = urls.Clone(r.URL)

	if isBody {
		r = request.SetBody(r, []byte(trq.Statement))
	} else {
		// Swap in the Tokenized Query in the Url Params
		qi.Set(upQuery, trq.Statement)
		trq.TemplateURL.RawQuery = qi.Encode()
	}

	return trq, ro, canOPC, nil
}
