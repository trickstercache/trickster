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

// Package clickhouse provides the ClickHouse origin type
package clickhouse

import (
	"net/http"
	"net/url"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy"
	"github.com/Comcast/trickster/internal/proxy/errors"
	tt "github.com/Comcast/trickster/internal/proxy/timeconv"
	"github.com/Comcast/trickster/internal/proxy/urls"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/regexp/matching"
)

// Client Implements the Proxy Client Interface
type Client struct {
	name               string
	config             *config.OriginConfig
	cache              cache.Cache
	webClient          *http.Client
	handlers           map[string]http.Handler
	handlersRegistered bool
	healthURL          *url.URL
	healthMethod       string
	healthHeaders      http.Header
	logUpstreamRequest bool
}

// NewClient returns a new Client Instance
func NewClient(name string, oc *config.OriginConfig, cache cache.Cache) (*Client, error) {
	c, err := proxy.NewHTTPClient(oc)
	return &Client{name: name, config: oc, cache: cache, webClient: c}, err
}

// Configuration returns the upstream Configuration for this Client
func (c *Client) Configuration() *config.OriginConfig {
	return c.config
}

// HTTPClient returns the HTTP Transport the client is using
func (c *Client) HTTPClient() *http.Client {
	return c.webClient
}

// Cache returns and handle to the Cache instance used by the Client
func (c *Client) Cache() cache.Cache {
	return c.cache
}

// Name returns the name of the upstream Configuration proxied by the Client
func (c *Client) Name() string {
	return c.name
}

// SetCache sets the Cache object the client will use for caching origin content
func (c *Client) SetCache(cc cache.Cache) {
	c.cache = cc
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery, error) {

	trq := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{}}
	trq.TemplateURL = urls.Clone(r.URL)
	qi := trq.TemplateURL.Query()
	if p, ok := qi[upQuery]; ok {
		trq.Statement = p[0]
	} else {
		return nil, errors.MissingURLParam(upQuery)
	}

	mp := []string{"step", "timeField"}
	found := matching.GetNamedMatches(reTimeFieldAndStep, trq.Statement, mp)

	for _, f := range mp {
		v, ok := found[f]
		if !ok || v == "" {
			return nil, errors.ErrNotTimeRangeQuery
		}
		switch f {
		case "timeField":
			trq.TimestampFieldName = v
		case "step":
			trq.Step, _ = tt.ParseDuration(v + "s")
		}
	}

	var err error
	trq.Statement, trq.Extent, _, err = getQueryParts(trq.Statement, trq.TimestampFieldName)
	if err != nil {
		return nil, err
	}

	// Swap in the Tokenzed Query in the Url Params
	qi.Set(upQuery, trq.Statement)
	trq.TemplateURL.RawQuery = qi.Encode()
	return trq, nil
}
