/*
 * Copyright 2020 Comcast Cable Communications Management, LLC
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
	"github.com/tricksterproxy/trickster/pkg/proxy/request"
	"net/http"
	"net/url"
	"time"

	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/proxy"
	"github.com/tricksterproxy/trickster/pkg/proxy/errors"
	"github.com/tricksterproxy/trickster/pkg/proxy/origins"
	oo "github.com/tricksterproxy/trickster/pkg/proxy/origins/options"
	"github.com/tricksterproxy/trickster/pkg/proxy/urls"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

var _ origins.Client = (*Client)(nil)

// Client Implements the Proxy Client Interface
type Client struct {
	name               string
	config             *oo.Options
	cache              cache.Cache
	webClient          *http.Client
	handlers           map[string]http.Handler
	handlersRegistered bool
	baseUpstreamURL    *url.URL
	healthURL          *url.URL
	healthMethod       string
	healthHeaders      http.Header
	router             http.Handler
}

// NewClient returns a new Client Instance
func NewClient(name string, oc *oo.Options, router http.Handler,
	cache cache.Cache) (origins.Client, error) {
	c, err := proxy.NewHTTPClient(oc)
	bur := urls.FromParts(oc.Scheme, oc.Host, oc.PathPrefix, "", "")
	return &Client{name: name, config: oc, router: router, cache: cache,
		baseUpstreamURL: bur, webClient: c}, err
}

// Configuration returns the upstream Configuration for this Client
func (c *Client) Configuration() *oo.Options {
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

// Router returns the http.Handler that handles request routing for this Client
func (c *Client) Router() http.Handler {
	return c.router
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery, error) {
	qi := r.URL.Query()
	var rawQuery string
	if p, ok := qi[upQuery]; ok {
		rawQuery = p[0]
	} else {
		return nil, errors.MissingURLParam(upQuery)
	}

	var bf time.Duration
	res := request.GetResources(r)
	if res == nil {
		bf = 60 * time.Second
	} else {
		bf = res.OriginConfig.BackfillTolerance
	}

	// Force gzip compression since Brotli is broken on CH 20.3
	// See https://github.com/ClickHouse/ClickHouse/issues/9969
	// Clients that don't understand gzip are going to break, but oh well
	r.Header.Set("Accept-Encoding", "gzip")

	trq := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{}, BackfillTolerance: bf}
	if err := parseRawQuery(rawQuery, trq); err != nil {
		return nil, err
	}

	trq.TemplateURL = urls.Clone(r.URL)
	// Swap in the Tokenized Query in the Url Params
	qi.Set(upQuery, trq.Statement)
	trq.TemplateURL.RawQuery = qi.Encode()
	return trq, nil
}
