/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package prometheus

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/common/model"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy"
)

// Prometheus API
const (
	APIPath      = "/api/v1/"
	mnQueryRange = "query_range"
	mnQuery      = "query"
	mnLabels     = "label/" + model.MetricNameLabel + "/values"
	mnSeries     = "series"
	mnHealth     = "health"
)

// Prometheus Response Values
const (
	rvSuccess = "success"
	rvMatrix  = "matrix"
	rvVector  = "vector"
)

// Common URL Parameter Names
const (
	upQuery   = "query"
	upStart   = "start"
	upEnd     = "end"
	upStep    = "step"
	upTimeout = "timeout"
	upTime    = "time"
)

// Origin Types
const (
	otPrometheus = "prometheus"
)

// Client Implements Proxy Client Interface
type Client struct {
	Name   string
	User   string
	Pass   string
	Config config.OriginConfig
	Cache  cache.Cache
}

// Configuration ...
func (c *Client) Configuration() config.OriginConfig {
	return c.Config
}

// OriginName ...
func (c *Client) OriginName() string {
	return c.Name
}

// CacheInstance ...
func (c *Client) CacheInstance() cache.Cache {
	return c.Cache
}

// ProxyHandler sends a request through the basic reverse proxy to the origin, and services non-cacheable Prometheus API calls.
func (c *Client) ProxyHandler(w http.ResponseWriter, r *http.Request) {
	proxy.ProxyRequest(proxy.NewRequest(c.Name, otPrometheus, "APIProxyHandler", r.Method, c.BuildUpstreamURL(r), r.Header, c.Config.Timeout, r), w)
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

// parseDuration converts a duration URL parameter to time.Duration.
// Copied from https://github.com/prometheus/prometheus/blob/v2.2.1/web/api/v1/api.go#L809-L821
func parseDuration1(s string) (time.Duration, error) {
	if d, err := strconv.ParseFloat(s, 64); err == nil {
		ts := d * float64(time.Second)
		if ts > float64(math.MaxInt64) || ts < float64(math.MinInt64) {
			return 0, fmt.Errorf("cannot parse %q to a valid duration. It overflows int64", s)
		}
		return time.Duration(ts), nil
	}
	if d, err := model.ParseDuration(s); err == nil {
		return time.Duration(d), nil
	}
	return 0, fmt.Errorf("cannot parse %q to a valid duration", s)
}
