/*
 * Copyright 2026 The Trickster Authors
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

// Package victoriametrics provides the VictoriaMetrics backend provider: MetricsQL through the
// Prometheus querying API, and the Graphite render, find and tags APIs, of single-node
// VictoriaMetrics or a tenant's vmselect paths.
package victoriametrics

import (
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	// the vmselect path segments that select a tenant's MetricsQL and Graphite APIs
	prometheusSegment = "/prometheus"
	graphiteSegment   = "/graphite"
	// a health check query that touches no series
	healthQuery = "query=1"
)

// Client serves one VictoriaMetrics backend. The Prometheus client is held as a plain time series
// backend, so its time series merging methods are not promoted: MetricsQL can't be merged by the
// PromQL planner, and TSM support for VictoriaMetrics is not implemented.
type Client struct {
	backends.TimeseriesBackend
	prom               *prometheus.Client
	analyzer           *analyzer
	graphitePath       string
	searchDisableCache bool
	// set once a response shows the origin writes isPartial, as vmselect does
	reportsPartial atomic.Bool
}

var (
	_ backends.Backend           = (*Client)(nil)
	_ backends.TimeseriesBackend = (*Client)(nil)
	_ types.NewBackendClientFunc = NewClient
)

// NewClient returns a VictoriaMetrics backend client.
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, _ backends.Backends, _ types.Lookup,
) (backends.Backend, error) {
	c := &Client{analyzer: newAnalyzer()}
	if o != nil && o.VictoriaMetrics != nil {
		c.graphitePath = o.VictoriaMetrics.GraphitePath
		c.searchDisableCache = o.VictoriaMetrics.SearchDisableCache
	}
	pc, err := prometheus.NewClientWithHooks(name, o, router, cache, prometheus.Hooks{
		CacheKeyParams:    resultParams,
		CacheKeyHeaders:   []string{},
		PrepareRequest:    c.prepareRequest,
		HealthCheckConfig: healthCheckConfig,
		PreserveQueryGrid: true,
	})
	c.prom, c.TimeseriesBackend = pc, pc
	if pc == nil {
		return c, err
	}
	if c.graphitePath == "" {
		c.graphitePath = derivedGraphitePath(pc.BaseUpstreamURL())
	}
	c.detectPartialResponses(pc.Modeler(), pc.HTTPClient())
	if err == nil {
		c.RegisterHandlers(nil)
	}
	return c, err
}

// derivedGraphitePath maps a vmselect tenant's /select/<tenant>/prometheus path to its sibling
// /select/<tenant>/graphite path; single-node VictoriaMetrics serves both under one path.
func derivedGraphitePath(u *url.URL) string {
	if u == nil {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	if base, ok := strings.CutSuffix(p, prometheusSegment); ok {
		return base + graphiteSegment
	}
	return p
}

func healthCheckConfig(u *url.URL) *ho.Options {
	o := ho.New()
	if u != nil {
		o.Scheme, o.Host = u.Scheme, u.Host
		o.Path = strings.TrimRight(u.Path, "/") + prometheus.APIPath + "query"
	}
	o.Query = healthQuery
	return o
}

const stepAlignments = timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate |
	timeseries.StepAlignmentDrop

// StepAlignments returns the modes MetricsQL range queries support and their default, truncate.
// Every range is served on the grid VictoriaMetrics evaluates it on, so truncate and drop answer
// alike; partial_end is not offered, since an instant query at the live edge isn't the point a
// VictoriaMetrics range query returns there.
func (c *Client) StepAlignments() (supported, def timeseries.StepAlignment) {
	return stepAlignments, timeseries.StepAlignmentTruncate
}

// ParseTimeRangeQuery parses a MetricsQL range query and applies this provider's cache contract:
// shapes that depend on the whole range are served as exact objects (step_alignment off), recent
// points stay volatile, and a response VictoriaMetrics marks partial is relayed uncached.
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error,
) {
	trq, rlo, ok, err := c.prom.ParseTimeRangeQuery(r)
	if trq == nil || rlo == nil {
		return trq, rlo, ok, err
	}
	rlo.FallbackToProxyOnError = true
	trq.StepAlignments, trq.StepAlignment = c.StepAlignments()
	if c.analyzer.analyze(trq.Statement).mode == modeObject {
		trq.StepAlignments, trq.StepAlignment = timeseries.StepAlignmentOff, timeseries.StepAlignmentOff
	}
	if o := c.Configuration(); o == nil || (o.VolatileWindow == 0 && o.VolatileWindowPoints == 0) {
		trq.VolatileWindow = defaultVolatileWindow
	}
	// points within a request's own latency_offset of now are provisional too
	values, _, _ := params.GetRequestValues(r)
	if ms, ok := stepMillis(values.Get(upLatencyOffset)); ok {
		if lo := time.Duration(ms) * time.Millisecond; lo > trq.GetVolatileWindow(c.volatileWindow(), 0) {
			trq.VolatileWindow = lo + defaultVolatileWindow/2
		}
	}
	return trq, rlo, ok, err
}

// GraphiteBaseURL returns the upstream URL of the Graphite APIs.
func (c *Client) GraphiteBaseURL() *url.URL {
	u := *c.BaseUpstreamURL()
	u.Path = c.graphitePath
	return &u
}

// volatileWindow is the backend's volatile window, or this provider's default when it sets none.
func (c *Client) volatileWindow() time.Duration {
	if o := c.Configuration(); o != nil && o.VolatileWindow > 0 {
		return time.Duration(o.VolatileWindow)
	}
	return defaultVolatileWindow
}
