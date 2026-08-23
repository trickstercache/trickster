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
	"context"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Lane is where a render request is served
type Lane uint8

const (
	// LaneObject is the unaccelerated lane: the whole response is cached by
	// request URL with a TTL (design note §6). It is always correct because
	// it makes no claim about the response's internal structure.
	LaneObject Lane = iota
	// LaneDelta is the Delta Proxy Cache: the response is modeled, cached
	// per time range at the origin's native step, and stitched from cached
	// and freshly fetched extents
	LaneDelta
)

func (l Lane) String() string {
	if l == LaneDelta {
		return "delta"
	}
	return "object"
}

// RouteDecision is the confidence router's verdict for one render request:
// which lane serves it, and — when it is the unaccelerated lane — the frozen
// reason why, so that the fallback is a named value rather than an inference.
type RouteDecision struct {
	Lane Lane
	// Confidence is the weakest confidence across the request's targets;
	// Unknown whenever Lane is LaneObject
	Confidence resolution.Confidence
	// Source is the resolution source of the target that set Confidence
	Source string
	// Step is the resolved step every target shares (LaneDelta only)
	Step time.Duration
	// Extent is the requested time range, clamped to what the origin holds
	Extent timeseries.Extent
	// MaxRetention is the shortest maxRetention across the resolved leaves,
	// 0 when unknown
	MaxRetention time.Duration
	// Reason is the frozen `reason` label value when Lane is LaneObject
	Reason string
	// Detail names the offending target, parameter or construct
	Detail string
	// Err is the underlying error, when the reason came from one
	Err error
}

// object returns an unaccelerated verdict
func object(reason, detail string, err error) RouteDecision {
	return RouteDecision{
		Lane: LaneObject, Confidence: resolution.Unknown,
		Source: resolution.SourceNone, Reason: reason, Detail: detail, Err: err,
	}
}

// confidenceRank orders confidences by how much they are trusted, so that
// the weakest across a multi-target request is the one reported
func confidenceRank(c resolution.Confidence) int {
	switch c {
	case resolution.Exact:
		return 3
	case resolution.Derived:
		return 2
	case resolution.Configured:
		return 1
	}
	return 0
}

// route applies the confidence routing table of design note §6 to a parsed
// render request, one decision per row and in order. It fills in the
// request's targets, age and effective age as it goes. Every row that
// declines names one of the frozen `reason` label values (plan item 3.4),
// so a fallback is never inferred from an absence.
//
//	row                                     lane    reason
//	--------------------------------------- ------- ---------------------------
//	no target parameter                     object  missing_target
//	non-series format, graphType, maxStep   object  non_series_format/unknown_step
//	maxDataPoints passthrough configured    object  passthrough_max_data_points
//	unparsable now / from / until           object  parse_error
//	unparsable target expression            object  parse_error
//	function not on the D4 allowlist        object  function_not_allowlisted
//	target's step not predictable           object  unknown_step / missing_target
//	targets resolve to different steps      object  multi_target_step_mismatch
//	window wholly beyond maxRetention       object  unknown_step
//	otherwise                               delta   —
func (c *Client) route(ctx context.Context, rq *RenderQuery) RouteDecision {
	rp := rq.Params

	// parameters that make a request non-accelerable on their own
	if len(rp.Targets) == 0 {
		return object(parsing.ReasonMissingTarget, upTarget, nil)
	}
	if rp.Declined != "" {
		return object(rp.Declined, rp.DeclinedParam, nil)
	}
	if o := c.Configuration(); o != nil && o.Graphite != nil &&
		o.Graphite.PassthroughMaxDataPoints && rp.MaxDataPoints > 0 {
		return object(parsing.ReasonPassthroughMaxPoints, "maxDataPoints", nil)
	}

	// the reference time and the requested window
	if rp.Now != "" {
		n, err := parsing.ParseATTime(rp.Now, rq.Location, rq.Now)
		if err != nil {
			return object(parsing.ReasonParseError, "now", err)
		}
		rq.Now = n
	}
	ext, err := parsing.ParseTimeRange(rp.From, rp.Until, rq.Location, rq.Now)
	if err != nil {
		return object(parsing.ReasonParseError, "from/until", err)
	}
	rq.Age = rq.Now.Sub(ext.Start)
	rq.EffectiveAge = rq.Age

	// every target must parse and pass the two-property allowlist (D4)
	rq.Targets = make([]Target, len(rp.Targets))
	for i, src := range rp.Targets {
		ast, err := parsing.ParseTarget(src)
		if err != nil {
			return object(parsing.ReasonParseError, upTarget, err)
		}
		cl := parsing.Classify(ast)
		if !cl.Accelerable() {
			return object(cl.Reason, cl.Offender, nil)
		}
		rq.Targets[i] = Target{Source: src, Canonical: parsing.Format(ast), AST: ast, Class: cl}
	}

	// every target's step must be predictable, and they must agree: one
	// TimeRangeQuery carries one step. A multi-target request whose targets
	// resolve differently is declined here and split per target by the
	// render handler (D5).
	d := RouteDecision{Lane: LaneDelta, Confidence: resolution.Exact, Source: resolution.SourceRegistry}
	for i := range rq.Targets {
		t := &rq.Targets[i]
		// graphite-web normalizes a mixed-step series list to its LCM only
		// inside a function; a bare path expression returns each series at
		// its own native step
		_, normalized := t.AST.(*parsing.Call)
		t.Resolution = c.resolver.Resolve(ctx, t.Class.Leaves, t.Class.FixedStep, rq.Age, normalized)
		if t.Resolution.Confidence == resolution.Unknown {
			return object(t.Resolution.Reason, t.Source, nil)
		}
		if d.Step != 0 && t.Resolution.Step != d.Step {
			return object(parsing.ReasonMultiTargetMismatch, t.Source, nil)
		}
		d.Step = t.Resolution.Step
		if confidenceRank(t.Resolution.Confidence) < confidenceRank(d.Confidence) {
			d.Confidence, d.Source = t.Resolution.Confidence, t.Resolution.Source
		}
		if mr := t.Resolution.MaxRetention; mr > 0 && (d.MaxRetention == 0 || mr < d.MaxRetention) {
			d.MaxRetention = mr
		}
	}

	// whisper's range clamp (§2.3): a window wholly beyond retention holds
	// nothing to cache; a from beyond retention is moved to the oldest point
	d.Extent = ext
	if d.MaxRetention > 0 {
		start, end, ok := resolution.Clamp(ext.Start, ext.End, rq.Now, d.MaxRetention)
		if !ok {
			return object(parsing.ReasonUnknownStep, "beyond maxRetention", nil)
		}
		d.Extent.Start, d.Extent.End = start, end
		rq.EffectiveAge = min(rq.Age, d.MaxRetention)
	}
	// the extent is the set of buckets the origin returns for this window
	// (§2.2): the first bucket strictly after from and the last at or before
	// until, both step-aligned; SetExtent inverts this per fetch
	first, afterLast := resolution.AlignInterval(d.Extent.Start, d.Extent.End, d.Step)
	d.Extent.Start, d.Extent.End = first, afterLast.Add(-d.Step)
	return d
}
