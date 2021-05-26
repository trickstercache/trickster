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

// Package prometheus provides the Prometheus Origin Type
package prometheus

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/pkg/cache"
	"github.com/trickstercache/trickster/pkg/proxy"
	"github.com/trickstercache/trickster/pkg/proxy/errors"
	"github.com/trickstercache/trickster/pkg/proxy/origins"
	oo "github.com/trickstercache/trickster/pkg/proxy/origins/options"
	"github.com/trickstercache/trickster/pkg/proxy/params"
	tt "github.com/trickstercache/trickster/pkg/proxy/timeconv"
	"github.com/trickstercache/trickster/pkg/proxy/urls"
	"github.com/trickstercache/trickster/pkg/timeseries"
)

var _ origins.Client = (*Client)(nil)

// Prometheus API
const (
	APIPath         = "/api/v1/"
	mnQueryRange    = "query_range"
	mnQuery         = "query"
	mnLabels        = "labels"
	mnLabel         = "label"
	mnSeries        = "series"
	mnTargets       = "targets"
	mnTargetsMeta   = "targets/metadata"
	mnRules         = "rules"
	mnAlerts        = "alerts"
	mnAlertManagers = "alertmanagers"
	mnStatus        = "status"
)

// Common URL Parameter Names
const (
	upQuery = "query"
	upStart = "start"
	upEnd   = "end"
	upStep  = "step"
	upTime  = "time"
	upMatch = "match[]"
)

// Client Implements Proxy Client Interface
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
	router             http.Handler
}

// NewClient returns a new Client Instance
func NewClient(name string, oc *oo.Options, router http.Handler,
	cache cache.Cache) (origins.Client, error) {
	c, err := proxy.NewHTTPClient(oc)
	bur := urls.FromParts(oc.Scheme, oc.Host, oc.PathPrefix, "", "")
	return &Client{name: name, config: oc, router: router, cache: cache,
		webClient: c, baseUpstreamURL: bur}, err
}

// SetCache sets the Cache object the client will use for caching origin content
func (c *Client) SetCache(cc cache.Cache) {
	c.cache = cc
}

// Configuration returns the upstream Configuration for this Client
func (c *Client) Configuration() *oo.Options {
	return c.config
}

// HTTPClient returns the HTTP Client for this origin
func (c *Client) HTTPClient() *http.Client {
	return c.webClient
}

// Name returns the name of the upstream Configuration proxied by the Client
func (c *Client) Name() string {
	return c.name
}

// Cache returns and handle to the Cache instance used by the Client
func (c *Client) Cache() cache.Cache {
	return c.cache
}

// Router returns the http.Handler that handles request routing for this Client
func (c *Client) Router() http.Handler {
	return c.router
}

// parseTime converts a query time URL parameter to time.Time.
// Copied from https://github.com/prometheus/prometheus/blob/master/web/api/v1/api.go
func parseTime(s string) (time.Time, error) {
	if t, err := strconv.ParseFloat(s, 64); err == nil {
		s, ns := math.Modf(t)
		ns = math.Round(ns*1000) / 1000
		return time.Unix(int64(s), int64(ns*float64(time.Second))), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cannot parse %q to a valid timestamp", s)
}

// parseDuration parses prometheus step parameters, which can be float64 or durations like 1d, 5m, etc
// the proxy.ParseDuration handles the second kind, and the float64's are handled here
func parseDuration(input string) (time.Duration, error) {
	v, err := strconv.ParseFloat(input, 64)
	if err != nil {
		return tt.ParseDuration(input)
	}
	// assume v is in seconds
	return time.Duration(int64(v)) * time.Second, nil
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery, error) {

	trq := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{}}

	qp, _, _ := params.GetRequestValues(r)

	trq.Statement = qp.Get(upQuery)
	if trq.Statement == "" {
		return nil, errors.MissingURLParam(upQuery)
	}

	if p := qp.Get(upStart); p != "" {
		t, err := parseTime(p)
		if err != nil {
			return nil, err
		}
		trq.Extent.Start = t
	} else {
		return nil, errors.MissingURLParam(upStart)
	}

	if p := qp.Get(upEnd); p != "" {
		t, err := parseTime(p)
		if err != nil {
			return nil, err
		}
		trq.Extent.End = t
	} else {
		return nil, errors.MissingURLParam(upEnd)
	}

	if p := qp.Get(upStep); p != "" {
		step, err := parseDuration(p)
		if err != nil {
			return nil, err
		}
		trq.Step = step
	} else {
		return nil, errors.MissingURLParam(upStep)
	}

	if strings.Contains(trq.Statement, " offset ") {
		trq.IsOffset = true
		trq.FastForwardDisable = true
	}

	if strings.Contains(trq.Statement, timeseries.FastForwardUserDisableFlag) {
		trq.FastForwardDisable = true
	}

	return trq, nil
}
