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

	"github.com/trickstercache/trickster/v2/pkg/backends"
	modeliron "github.com/trickstercache/trickster/v2/pkg/backends/irondb/model"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

var _ backends.TimeseriesBackend = (*Client)(nil)

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
	backends.TimeseriesBackend

	trqParsers    map[string]trqParser
	extentSetters map[string]extentSetter
}

var _ types.NewBackendClientFunc = NewClient

// NewClient returns a new Client Instance
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, _ backends.Backends,
	_ types.Lookup) (backends.Backend, error) {
	c := &Client{}
	b, err := backends.NewTimeseriesBackend(name, o, c.RegisterHandlers,
		router, cache, modeliron.NewModeler())
	c.TimeseriesBackend = b
	c.makeTrqParsers()
	c.makeExtentSetters()
	return c, err
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
