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

package graphite

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Common URL Parameter Names
const (
	upTarget = "target"
)

// FallbackError is returned by ParseTimeRangeQuery when a render request is
// valid but must not be accelerated. Reason is one of the frozen
// parsing.Reason* values and becomes the `reason` label on
// trickster_graphite_fallbacks_total; Detail names the offending construct.
type FallbackError struct {
	Reason string
	Detail string
	Err    error
}

func (e *FallbackError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("graphite: not accelerable (%s, %s): %v", e.Reason, e.Detail, e.Err)
	}
	return fmt.Sprintf("graphite: not accelerable (%s, %s)", e.Reason, e.Detail)
}

func (e *FallbackError) Unwrap() error { return e.Err }

// ErrNotAccelerable is the sentinel every FallbackError matches with errors.Is
var ErrNotAccelerable = errors.New("graphite: not accelerable")

// Is lets errors.Is(err, ErrNotAccelerable) match any FallbackError
func (e *FallbackError) Is(target error) bool { return target == ErrNotAccelerable }

// Target is one parsed, classified render target
type Target struct {
	// Source is the target exactly as the client sent it
	Source string
	// Canonical is the cache-key form (parsing.Format)
	Canonical string
	AST       parsing.Node
	Class     parsing.Classification
}

// RenderQuery is the provider-specific parsed form of a render request,
// carried on TimeRangeQuery.ParsedQuery and RequestOptions.ProviderRequest
type RenderQuery struct {
	Params  parsing.RenderParams
	Targets []Target
	// Location is the effective time zone (the tz parameter when valid,
	// else the backend's configured time_zone)
	Location *time.Location
	// Now is the reference time the extent was computed against
	Now time.Time
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the
// inbound render request. It returns a non-nil error with canOPC=true for
// every reason the request must not be accelerated, so that the Delta Proxy
// Cache hands the request to the Object Proxy Cache rather than raw proxy
// (design note §6). The step is not set here: it is not knowable from the
// request, and is resolved by the render handler before the DPC runs.
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error,
) {
	trq := &timeseries.TimeRangeQuery{}
	qp, body, isBody := params.GetRequestValues(r)
	if isBody {
		trq.OriginalBody = body
	}
	rp := parsing.ParseRenderParams(qp)
	if len(rp.Targets) == 0 {
		return trq, nil, true, &FallbackError{Reason: parsing.ReasonMissingTarget, Detail: upTarget}
	}
	if rp.Declined != "" {
		return trq, nil, true, &FallbackError{Reason: rp.Declined, Detail: rp.DeclinedParam}
	}
	if o := c.Configuration(); o != nil && o.Graphite != nil &&
		o.Graphite.PassthroughMaxDataPoints && rp.MaxDataPoints > 0 {
		return trq, nil, true, &FallbackError{
			Reason: parsing.ReasonPassthroughMaxPoints,
			Detail: "maxDataPoints",
		}
	}
	rq := &RenderQuery{Params: rp, Location: c.location(rp.TZ), Now: time.Now()}
	if rp.Now != "" {
		n, err := parsing.ParseATTime(rp.Now, rq.Location, rq.Now)
		if err != nil {
			return trq, nil, true, &FallbackError{Reason: parsing.ReasonParseError, Detail: "now", Err: err}
		}
		rq.Now = n
	}
	ext, err := parsing.ParseTimeRange(rp.From, rp.Until, rq.Location, rq.Now)
	if err != nil {
		return trq, nil, true, &FallbackError{Reason: parsing.ReasonParseError, Detail: "from/until", Err: err}
	}
	trq.Extent = ext

	rq.Targets = make([]Target, len(rp.Targets))
	canonical := make([]string, len(rp.Targets))
	var fixed time.Duration
	for i, src := range rp.Targets {
		ast, err := parsing.ParseTarget(src)
		if err != nil {
			return trq, nil, true, &FallbackError{Reason: parsing.ReasonParseError, Detail: upTarget, Err: err}
		}
		cl := parsing.Classify(ast)
		if !cl.Accelerable() {
			return trq, nil, true, &FallbackError{Reason: cl.Reason, Detail: cl.Offender}
		}
		if cl.Step == parsing.StepFixed {
			if fixed != 0 && fixed != cl.FixedStep {
				// D5 option B splits per target in the render handler; until
				// then, heterogeneous fixed steps cannot share one query
				return trq, nil, true, &FallbackError{
					Reason: parsing.ReasonMultiTargetMismatch,
					Detail: "summarize",
				}
			}
			fixed = cl.FixedStep
		}
		canonical[i] = parsing.Format(ast)
		rq.Targets[i] = Target{Source: src, Canonical: canonical[i], AST: ast, Class: cl}
	}
	// targets cannot contain a newline (the grammar admits printable ASCII
	// only), so it is a safe separator for the statement
	trq.Statement = strings.Join(canonical, "\n")
	trq.ParsedQuery = rq
	if fixed != 0 {
		trq.Step = fixed
	}
	rlo := &timeseries.RequestOptions{FastForwardDisable: true, ProviderRequest: rq}
	return trq, rlo, true, nil
}

// location returns the time zone for parsing date-anchored from/until
// values: the request's tz parameter when it names a known zone (graphite-web
// ignores an unknown one), otherwise the backend's configured time_zone,
// otherwise UTC
func (c *Client) location(tz string) *time.Location {
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	if o := c.Configuration(); o != nil && o.Graphite != nil && o.Graphite.TimeZone != "" {
		if loc, err := time.LoadLocation(o.Graphite.TimeZone); err == nil {
			return loc
		}
	}
	return time.UTC
}
