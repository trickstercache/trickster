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

// Package irondb provides proxy origin support for IRONdb databases.
package irondb

import (
	"net/http"
	"net/url"

	"github.com/trickstercache/trickster/pkg/cache"
	"github.com/trickstercache/trickster/pkg/proxy"
	"github.com/trickstercache/trickster/pkg/proxy/origins"
	oo "github.com/trickstercache/trickster/pkg/proxy/origins/options"
	"github.com/trickstercache/trickster/pkg/proxy/urls"
	"github.com/trickstercache/trickster/pkg/timeseries"
)

var _ origins.Client = (*Client)(nil)

// IRONdb API path segments.
const (
	mnRaw       = "raw"
	mnRollup    = "rollup"
	mnFetch     = "fetch"
	mnRead      = "read"
	mnHistogram = "histogram"
	mnFind      = "find"
	mnCAQL      = "extension/lua/caql_v1"
	mnCAQLPub   = "extension/lua/public/caql_v1"
	mnState     = "state"
)

// Common IRONdb URL query parameter names.
const (
	upQuery      = "query"
	upStart      = "start_ts"
	upEnd        = "end_ts"
	upSpan       = "rollup_span"
	upEngine     = "get_engine"
	upType       = "type"
	upCAQLQuery  = "q"
	upCAQLStart  = "start"
	upCAQLEnd    = "end"
	upCAQLPeriod = "period"
)

// IRONdb request body field names.
const (
	rbStart  = "start"
	rbCount  = "count"
	rbPeriod = "period"
)

type trqParser func(*http.Request) (*timeseries.TimeRangeQuery, error)
type extentSetter func(*http.Request, *timeseries.TimeRangeQuery, *timeseries.Extent)

// Client values provide access to IRONdb and implement the Trickster proxy
// client interface.
type Client struct {
	name               string
	config             *oo.Options
	cache              cache.Cache
	webClient          *http.Client
	handlers           map[string]http.Handler
	handlersRegistered bool
	baseUpstreamURL    *url.URL
	healthURL          *url.URL
	healthHeaders      http.Header
	healthMethod       string
	trqParsers         map[string]trqParser
	extentSetters      map[string]extentSetter
	router             http.Handler
}

// NewClient returns a new Client Instance
func NewClient(name string, oc *oo.Options, router http.Handler,
	cache cache.Cache) (origins.Client, error) {
	c, err := proxy.NewHTTPClient(oc)
	bur := urls.FromParts(oc.Scheme, oc.Host, oc.PathPrefix, "", "")
	client := &Client{name: name, config: oc, router: router, cache: cache,
		baseUpstreamURL: bur, webClient: c}
	client.makeTrqParsers()
	client.makeExtentSetters()
	return client, err
}

func (c *Client) makeTrqParsers() {
	c.trqParsers = map[string]trqParser{
		"RawHandler":       c.rawHandlerParseTimeRangeQuery,
		"RollupHandler":    c.rollupHandlerParseTimeRangeQuery,
		"FetchHandler":     c.fetchHandlerParseTimeRangeQuery,
		"TextHandler":      c.textHandlerParseTimeRangeQuery,
		"HistogramHandler": c.histogramHandlerParseTimeRangeQuery,
		"CAQLHandler":      c.caqlHandlerParseTimeRangeQuery,
	}
}

func (c *Client) makeExtentSetters() {
	c.extentSetters = map[string]extentSetter{
		"RawHandler":       c.rawHandlerSetExtent,
		"RollupHandler":    c.rollupHandlerSetExtent,
		"FetchHandler":     c.fetchHandlerSetExtent,
		"TextHandler":      c.textHandlerSetExtent,
		"HistogramHandler": c.histogramHandlerSetExtent,
		"CAQLHandler":      c.caqlHandlerSetExtent,
	}
}

// Configuration returns the upstream Configuration for this Client.
func (c *Client) Configuration() *oo.Options {
	return c.config
}

// HTTPClient returns the HTTP Transport this client is using.
func (c *Client) HTTPClient() *http.Client {
	return c.webClient
}

// Cache returns a handle to the Cache instance used by this Client.
func (c *Client) Cache() cache.Cache {
	return c.cache
}

// Name returns the name of the origin Configuration proxied by the Client.
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
