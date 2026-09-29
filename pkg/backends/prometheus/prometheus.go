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
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	modelprom "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	tt "github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/directives"
)

var (
	_ backends.TimeseriesBackend          = (*Client)(nil)
	_ backends.MergeableTimeseriesBackend = (*Client)(nil)
)

// Prometheus API
const (
	APIPath           = "/api/v1/"
	mnQueryRange      = "query_range"
	mnQuery           = "query"
	mnLabels          = "labels"
	mnLabel           = "label"
	mnSeries          = "series"
	mnTargets         = "targets"
	mnTargetsMeta     = "targets/metadata"
	mnRules           = "rules"
	mnAlerts          = "alerts"
	mnAlertManagers   = "alertmanagers"
	mnStatus          = "status"
	mnQueryExemplars  = "query_exemplars"
	mnMetadata        = "metadata"
	mnFormatQuery     = "format_query"
	mnParseQuery      = "parse_query"
	mnScrapePools     = "scrape_pools"
	mnFeatures        = "features"
	mnNotificationsLv = "notifications/live"
)

const whitespace = " \t\r\n"

var startEndModifiers = [...]string{"start", "end"}

// Common URL Parameter Names
const (
	upQuery = "query"
	upStart = "start"
	upEnd   = "end"
	upStep  = "step"
	upTime  = "time"
	upMatch = "match[]"
)

// containsOffsetKeyword reports whether stmt contains the PromQL " offset "
// keyword outside of curly-brace selectors and double-quoted strings.
// This avoids false positives from UTF-8 metric/label names like
// {"metric offset name"} that became valid in Prometheus 3.0+.
func containsOffsetKeyword(stmt string) bool {
	const target = " offset "
	depth := 0
	inQuote := false
	for i := 0; i < len(stmt); i++ {
		switch {
		case stmt[i] == '\\' && inQuote:
			i++ // skip escaped character
		case stmt[i] == '"':
			inQuote = !inQuote
		case inQuote:
			continue
		case stmt[i] == '{':
			depth++
		case stmt[i] == '}' && depth > 0:
			depth--
		case depth == 0 && i+len(target) <= len(stmt) &&
			stmt[i:i+len(target)] == target:
			return true
		}
	}
	return false
}

func containsStartEndModifier(stmt string) bool {
	// finds @ start() or @ end() outside string literals; a match inside a comment only
	// costs caching, since the request is then proxied
	var quote byte
	for i := 0; i < len(stmt); i++ {
		c := stmt[i]
		switch {
		case quote != 0:
			if c == '\\' && quote != '`' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '@':
			rest := strings.TrimLeft(stmt[i+1:], whitespace)
			for _, fn := range startEndModifiers {
				if strings.HasPrefix(rest, fn) &&
					strings.HasPrefix(strings.TrimLeft(rest[len(fn):], whitespace), "(") {
					return true
				}
			}
		}
	}
	return false
}

// roundTimestampParameterToMinute rounds a specific timestamp parameter down to the minute for cacheability
func roundTimestampParameterToMinute(qp url.Values, paramName string) {
	if p := qp.Get(paramName); p != "" {
		if i, err := strconv.ParseInt(p, 10, 64); err == nil {
			qp.Set(paramName, strconv.FormatInt(time.Unix(i, 0).Truncate(time.Second*time.Duration(60)).Unix(), 10))
		}
	}
}

// roundEndTimestampParameterToMinute rounds an end-of-window timestamp up to the
// next minute boundary. Rounding down can silently drop up to 59s of the newest
// data when the caller's `end` is mid-minute, which surfaces as empty /series
// and /labels responses immediately after Prometheus starts scraping. Rounding
// up keeps recent samples in-window while still producing stable cache keys.
func roundEndTimestampParameterToMinute(qp url.Values, paramName string) {
	if p := qp.Get(paramName); p != "" {
		if i, err := strconv.ParseInt(p, 10, 64); err == nil {
			t := time.Unix(i, 0)
			rounded := t.Truncate(time.Minute)
			if !rounded.Equal(t) {
				rounded = rounded.Add(time.Minute)
			}
			qp.Set(paramName, strconv.FormatInt(rounded.Unix(), 10))
		}
	}
}

// roundTimestampsToMinute aligns start and end timestamps to minute boundaries
// for cacheability: start rounds down, end rounds up so the queried window
// still covers all samples the caller asked about.
func roundTimestampsToMinute(qp url.Values) {
	roundTimestampParameterToMinute(qp, upStart)
	roundEndTimestampParameterToMinute(qp, upEnd)
}

// Client Implements Proxy Client Interface
type Client struct {
	backends.TimeseriesBackend
	hooks              Hooks
	instantRounder     time.Duration
	hasTransformations bool
	injectLabels       map[string]string
}

// captureLimit returns the per-response capture buffer cap for c. Reads
// max_capture_bytes from the backend's configuration; falls back to the
// package-level default when unset.
func captureLimit(c *Client) int {
	if c != nil {
		if o := c.Configuration(); o != nil && o.MaxCaptureBytes > 0 {
			return o.MaxCaptureBytes
		}
	}
	return capture.DefaultMaxBytes
}

var _ types.NewBackendClientFunc = NewClient

// NewClient returns a new Client Instance
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, _ backends.Backends,
	_ types.Lookup,
) (backends.Backend, error) {
	return NewClientWithHooks(name, o, router, cache, Hooks{})
}

// NewClientWithHooks constructs the Prometheus client for a compatible provider.
func NewClientWithHooks(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, hooks Hooks,
) (*Client, error) {
	hooks.CacheKeyParams = slices.Clone(hooks.CacheKeyParams)
	hooks.CacheKeyHeaders = slices.Clone(hooks.CacheKeyHeaders)
	c := &Client{hooks: hooks}
	b, err := backends.NewTimeseriesBackend(name, o, c.RegisterHandlers, router,
		cache, modelprom.NewModeler())
	c.TimeseriesBackend = b

	rounder := tt.Duration(po.DefaultInstantRound)
	if o != nil {
		if o.Prometheus == nil {
			o.Prometheus = &po.Options{InstantRound: tt.Duration(po.DefaultInstantRound)}
		} else {
			rounder = o.Prometheus.InstantRound
			c.injectLabels = o.Prometheus.Labels
			c.hasTransformations = len(c.injectLabels) > 0
		}
	}
	c.instantRounder = time.Duration(rounder)

	return c, err
}

const stepAlignments = timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate |
	timeseries.StepAlignmentDrop | timeseries.StepAlignmentPartialEnd

// StepAlignments returns the modes a range query supports and its default: partial_end, whose live
// end is Fast Forward, or truncate when fast_forward_disable is set
func (c *Client) StepAlignments() (supported, def timeseries.StepAlignment) {
	if c.TimeseriesBackend != nil {
		if o := c.Configuration(); o != nil && o.FastForwardDisable {
			return stepAlignments, timeseries.StepAlignmentTruncate
		}
	}
	return stepAlignments, timeseries.StepAlignmentPartialEnd
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
	// v is in seconds and keeps its fraction, as Prometheus does; a step Prometheus would
	// reject is refused here too
	d := math.Round(v * float64(time.Second))
	if math.IsNaN(d) || d <= 0 || d > math.MaxInt64 {
		return 0, fmt.Errorf("cannot parse %q to a valid step", input)
	}
	return time.Duration(d), nil
}

func formatTime(t time.Time) string {
	// Unix seconds, with the millisecond fraction Prometheus accepts only when present
	ms := t.UnixMilli()
	if ms%1000 == 0 {
		return strconv.FormatInt(ms/1000, 10)
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64)
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error,
) {
	trq := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{}}
	rlo := &timeseries.RequestOptions{}
	qp, b, isBody := params.GetRequestValues(r)
	if isBody {
		trq.OriginalBody = b
	}

	trq.Statement = qp.Get(upQuery)
	if trq.Statement == "" {
		return nil, nil, false, errors.MissingURLParam(upQuery)
	}
	if containsStartEndModifier(trq.Statement) {
		// each delta fetch would resolve start() and end() against its own sub-range, and the
		// object cache key omits the range, so the request is proxied
		return trq, rlo, false, errors.ErrStartEndModifier
	}
	p := qp.Get(upStart)
	if p == "" {
		return nil, nil, false, errors.MissingURLParam(upStart)
	}
	parse := parseTime
	if c.hooks.PreserveQueryGrid {
		parse = parseGridTime
	}
	t, err := parse(p)
	if err != nil {
		return nil, nil, false, err
	}
	trq.Extent.Start = t

	p = qp.Get(upEnd)
	if p == "" {
		return nil, nil, false, errors.MissingURLParam(upEnd)
	}
	t, err = parse(p)
	if err != nil {
		return nil, nil, false, err
	}
	trq.Extent.End = t

	p = qp.Get(upStep)
	if p == "" {
		return nil, nil, false, errors.MissingURLParam(upStep)
	}
	step, err := parseDuration(p)
	if c.hooks.PreserveQueryGrid {
		step, err = time.ParseDuration(p + "s")
		if err != nil {
			step, err = tt.ParseDuration(p)
		}
		if err == nil && (step <= 0 || step%time.Millisecond != 0) {
			err = timeseries.ErrUnknownFormat
		}
	}
	if err != nil {
		return nil, nil, false, err
	}
	trq.Step = step
	// the range as the client sent it, before any grid alignment below
	requested := timeseries.RequestedRange{Start: trq.Extent.Start, End: trq.Extent.End, EndInclusive: true}
	if c.hooks.PreserveQueryGrid {
		if trq.Extent.End.Before(trq.Extent.Start) {
			return nil, nil, false, timeseries.ErrUnknownFormat
		}
		if c.hooks.AlignQueryGrid {
			for _, at := range []*time.Time{&trq.Extent.Start, &trq.Extent.End} {
				remainder := at.UnixNano() % int64(step)
				if remainder < 0 {
					remainder += int64(step)
				}
				*at = at.Add(-time.Duration(remainder))
				if !at.Equal(time.Unix(0, at.UnixNano())) {
					return nil, nil, false, timeseries.ErrUnknownFormat
				}
			}
		}
		trq.Phase = time.Duration(trq.Extent.Start.UnixNano() % step.Nanoseconds())
		if trq.Phase < 0 {
			trq.Phase += step
		}
		trq.CacheKeyElements = map[string]string{"grid_phase_ns": strconv.FormatInt(int64(trq.Phase), 10)}
		if trq.Phase != 0 {
			// Shared epoch-aligned sharding cannot preserve an offset grid.
			if o := c.Configuration(); o != nil && o.DoesShard {
				return nil, nil, false, timeseries.ErrUnknownFormat
			}
		}
	}

	if containsOffsetKeyword(trq.Statement) {
		trq.IsOffset = true
		rlo.FastForwardDisable = true
	}

	if c.hooks.PreserveQueryGrid && (trq.Phase != 0 || step%time.Second != 0) {
		rlo.FastForwardDisable = true
	}
	trq.Directives = directives.Parse(trq.Statement, directives.SyntaxPromQL)
	if keyed := directives.Strip(trq.Statement, directives.SyntaxPromQL); keyed != trq.Statement {
		trq.KeyParamValues = map[string]string{upQuery: keyed}
	}

	trq.Requested = requested
	trq.StepAlignments, trq.StepAlignment = c.StepAlignments()

	return trq, rlo, true, nil
}

// parseGridTime accepts only the millisecond precision carried by Prometheus
// responses, within the common dataset's nanosecond epoch range.
func parseGridTime(value string) (time.Time, error) {
	if v, err := strconv.ParseFloat(value, 64); err == nil {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > float64(math.MaxInt64/int64(time.Second)) || math.Round(v*1000)/1000 != v {
			return time.Time{}, timeseries.ErrUnknownFormat
		}
	} else if v, err := time.Parse(time.RFC3339Nano, value); err == nil && v.Nanosecond()%int(time.Millisecond) != 0 {
		return time.Time{}, timeseries.ErrUnknownFormat
	}
	t, err := parseTime(value)
	if err == nil && (t.Year() < 1678 || t.Year() > 2261) {
		err = timeseries.ErrUnknownFormat
	}
	return t, err
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

	if containsOffsetKeyword(trq.Statement) {
		trq.IsOffset = true
	}

	return trq, nil
}
