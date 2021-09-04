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

package irondb

import (
	"errors"
	"fmt"
	"net/http"

	terr "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// SetExtent will change the upstream request query to use the provided Extent.
func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery, extent *timeseries.Extent) {

	rsc := request.GetResources(r)
	if rsc == nil || rsc.PathConfig == nil {
		return
	}

	if f, ok := c.extentSetters[rsc.PathConfig.HandlerName]; ok {
		f(r, trq, extent)
	}
}

// FastForwardRequest returns an *http.Request crafted to collect Fast Forward
// data from the Origin, based on the provided HTTP Request
func (c *Client) FastForwardRequest(r *http.Request) (*http.Request, error) {

	rsc := request.GetResources(r)
	if rsc == nil || rsc.PathConfig == nil {
		return nil, errors.New("missing path config")
	}

	switch rsc.PathConfig.HandlerName {
	case "RollupHandler":
		return c.rollupHandlerFastForwardRequest(r)
	case "HistogramHandler":
		return c.histogramHandlerFastForwardRequest(r)
	case "CAQLHandler":
		return c.caqlHandlerFastForwardRequest(r)
	}

	return nil, fmt.Errorf("unknown handler name: %s", rsc.PathConfig.HandlerName)
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the
// inbound HTTP Request.
func (c *Client) ParseTimeRangeQuery(
	r *http.Request) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions,
	bool, error) {

	rsc := request.GetResources(r)
	if rsc == nil || rsc.PathConfig == nil {
		return nil, nil, false, errors.New("missing path config")
	}

	var trq *timeseries.TimeRangeQuery
	var err error

	if f, ok := c.trqParsers[rsc.PathConfig.HandlerName]; ok {
		trq, err = f(r)
	} else {
		trq = nil
		err = terr.ErrNotTimeRangeQuery
	}
	rsc.TimeRangeQuery = trq
	return trq, nil, true, err
}
