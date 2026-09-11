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

	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Lane is where a render request is served
type Lane uint8

const (
	// LaneObject is the unaccelerated lane: the whole response is cached by
	// request URL with a TTL, making no claim about its internal structure
	LaneObject Lane = iota
	// LaneDelta is the Delta Proxy Cache: the response is modeled, cached at
	// the origin's native step, and stitched from cached and fetched extents
	LaneDelta
)

func (l Lane) String() string {
	if l == LaneDelta {
		return "delta"
	}
	return "object"
}

// RouteDecision is the confidence router's verdict for one render request:
// the lane, and — for the unaccelerated lane — the frozen reason why
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

func object(reason, detail string, err error) RouteDecision {
	return RouteDecision{
		Lane: LaneObject, Confidence: resolution.Unknown,
		Source: resolution.SourceNone, Reason: reason, Detail: detail, Err: err,
	}
}

// ranks confidences by trust, so the weakest across a request is reported
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

// decides the lane for a parsed render request, filling in its targets, age
// and effective age; every decline names a frozen `reason` label value
func (c *Client) route(ctx context.Context, rq *RenderQuery) RouteDecision {
	rp := rq.Params

	// parameters that make a request non-accelerable on their own
	if len(rp.Targets) == 0 {
		return object(parsing.ReasonMissingTarget, upTarget, nil)
	}
	if rp.Declined != "" {
		return object(rp.Declined, rp.DeclinedParam, nil)
	}
	g := c.graphiteOptions()
	if g.PassthroughMaxDataPoints && rp.MaxDataPoints > 0 {
		return object(parsing.ReasonPassthroughMaxPoints, "maxDataPoints", nil)
	}

	maxTargets := g.MaxTargetsPerRequest
	if maxTargets <= 0 {
		maxTargets = gro.DefaultMaxTargetsPerRequest
	}
	if len(rp.Targets) > maxTargets {
		return object(parsing.ReasonParseError, "too many targets", nil)
	}
	maxTargetLen := g.MaxTargetLength
	if maxTargetLen <= 0 {
		maxTargetLen = gro.DefaultMaxTargetLength
	}
	for _, src := range rp.Targets {
		if len(src) > maxTargetLen {
			return object(parsing.ReasonParseError, "target exceeds max_target_length", nil)
		}
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

	// every target must parse and pass the classification allowlist
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

	// every target's step must be predictable and all must agree (one
	// TimeRangeQuery, one step); mismatches are split per target by the handler
	d := RouteDecision{Lane: LaneDelta, Confidence: resolution.Exact, Source: resolution.SourceRegistry}
	var mismatch string
	for i := range rq.Targets {
		t := &rq.Targets[i]
		// graphite-web normalizes a mixed-step series list to its LCM only in
		// a function; a bare path returns each series at its own native step
		_, normalized := t.AST.(*parsing.Call)
		t.Resolution = c.resolver.Resolve(ctx, t.Class.Leaves, t.Class.FixedStep, rq.Age, normalized)
		if t.Resolution.Confidence == resolution.Unknown {
			return object(t.Resolution.Reason, t.Source, nil)
		}
		if d.Step == 0 {
			d.Step = t.Resolution.Step
		} else if t.Resolution.Step != d.Step && mismatch == "" {
			mismatch = t.Source
		}
		if confidenceRank(t.Resolution.Confidence) < confidenceRank(d.Confidence) {
			d.Confidence, d.Source = t.Resolution.Confidence, t.Resolution.Source
		}
		if mr := t.Resolution.MaxRetention; mr > 0 && (d.MaxRetention == 0 || mr < d.MaxRetention) {
			d.MaxRetention = mr
		}
	}
	if mismatch != "" {
		return object(parsing.ReasonMultiTargetMismatch, mismatch, nil)
	}

	// whisper's range clamp: a window wholly beyond retention holds nothing
	// to cache; a from beyond retention is moved to the oldest point
	d.Extent = ext
	if d.MaxRetention > 0 {
		start, end, ok := resolution.Clamp(ext.Start, ext.End, rq.Now, d.MaxRetention)
		if !ok {
			return object(parsing.ReasonUnknownStep, "beyond maxRetention", nil)
		}
		d.Extent.Start, d.Extent.End = start, end
		rq.EffectiveAge = min(rq.Age, d.MaxRetention)
	}
	// the origin returns the first bucket strictly after from and the last at
	// or before until, both step-aligned; SetExtent inverts this per fetch
	first, afterLast := resolution.AlignInterval(d.Extent.Start, d.Extent.End, d.Step)
	d.Extent.Start, d.Extent.End = first, afterLast.Add(-d.Step)
	return d
}
