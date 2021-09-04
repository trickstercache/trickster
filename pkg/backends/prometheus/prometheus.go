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

// Package prometheus provides the Prometheus Backend provider
package prometheus

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	modelprom "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	tt "github.com/trickstercache/trickster/v2/pkg/util/timeconv"
)

var _ backends.TimeseriesBackend = (*Client)(nil)
var _ backends.MergeableTimeseriesBackend = (*Client)(nil)

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
	backends.TimeseriesBackend
	instantRounder     time.Duration
	hasTransformations bool
	injectLabels       map[string]string
}

var _ types.NewBackendClientFunc = NewClient

// NewClient returns a new Client Instance
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, _ backends.Backends,
	_ types.Lookup) (backends.Backend, error) {

	c := &Client{}
	b, err := backends.NewTimeseriesBackend(name, o, c.RegisterHandlers, router,
		cache, modelprom.NewModeler())
	c.TimeseriesBackend = b

	rounder := time.Duration(po.DefaultInstantRoundMS) * time.Millisecond
	if o != nil {
		if o.Prometheus == nil {
			o.Prometheus = &po.Options{InstantRoundMS: po.DefaultInstantRoundMS}
		} else {
			rounder = time.Duration(o.Prometheus.InstantRoundMS) * time.Millisecond
			c.injectLabels = o.Prometheus.Labels
			c.hasTransformations = len(c.injectLabels) > 0
		}
	}
	c.instantRounder = rounder

	return c, err
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
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error) {

	trq := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{}}
	rlo := &timeseries.RequestOptions{}
	qp, _, _ := params.GetRequestValues(r)

	trq.Statement = qp.Get(upQuery)
	if trq.Statement == "" {
		return nil, nil, false, errors.MissingURLParam(upQuery)
	}

	if p := qp.Get(upStart); p != "" {
		t, err := parseTime(p)
		if err != nil {
			return nil, nil, false, err
		}
		trq.Extent.Start = t
	} else {
		return nil, nil, false, errors.MissingURLParam(upStart)
	}

	if p := qp.Get(upEnd); p != "" {
		t, err := parseTime(p)
		if err != nil {
			return nil, nil, false, err
		}
		trq.Extent.End = t
	} else {
		return nil, nil, false, errors.MissingURLParam(upEnd)
	}

	if p := qp.Get(upStep); p != "" {
		step, err := parseDuration(p)
		if err != nil {
			return nil, nil, false, err
		}
		trq.Step = step
	} else {
		return nil, nil, false, errors.MissingURLParam(upStep)
	}

	if strings.Contains(trq.Statement, " offset ") {
		trq.IsOffset = true
		rlo.FastForwardDisable = true
	}

	rlo.ExtractFastForwardDisabled(trq.Statement)
	trq.ExtractBackfillTolerance(trq.Statement)

	if x := strings.Index(trq.Statement, timeseries.BackfillToleranceFlag); x > 1 {
		x += 29
		y := x
		for ; y < len(trq.Statement); y++ {
			if trq.Statement[y] < 48 || trq.Statement[y] > 57 {
				break
			}
		}
		if i, err := strconv.Atoi(trq.Statement[x:y]); err == nil {
			trq.BackfillTolerance = time.Second * time.Duration(i)
		}
	}

	return trq, rlo, true, nil
}

// parseVectorQuery parses the key parts of an Instantaneous Query from the inbound HTTP Request
func parseVectorQuery(r *http.Request, rounder time.Duration) (*timeseries.TimeRangeQuery, error) {

	trq := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{}}
	qp, _, _ := params.GetRequestValues(r)

	trq.Statement = qp.Get(upQuery)
	if trq.Statement == "" {
		return nil, errors.MissingURLParam(upQuery)
	}

	if p := qp.Get(upTime); p != "" {
		t, err := parseTime(p)
		if err != nil {
			return nil, err
		}
		trq.Extent.Start = t
		trq.Extent.End = t
	} else {
		trq.Extent.Start = time.Now().Truncate(rounder)
	}

	if strings.Contains(trq.Statement, " offset ") {
		trq.IsOffset = true
	}

	return trq, nil
}
