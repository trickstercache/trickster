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
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/model"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
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
	// Resolution is the resolver's verdict for this target
	Resolution resolution.Resolution
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
	// Age is now - from as the client asked it (before any retention
	// clamp), which is what selects the origin's archive rung; EffectiveAge
	// is Age capped at the leaves' maxRetention, and is the age every
	// upstream fetch is pinned to (see SetExtent)
	Age, EffectiveAge time.Duration
	// Fallback is the frozen reason when the request was declined, or empty
	Fallback string

	mismatch atomic.Pointer[stepMismatch]
}

type stepMismatch struct {
	target              string
	predicted, observed time.Duration
}

// NoteStepMismatch records that an origin response contradicted the
// predicted step (implements model.StepMismatchNoter)
func (rq *RenderQuery) NoteStepMismatch(target string, predicted, observed time.Duration) {
	rq.mismatch.Store(&stepMismatch{target: target, predicted: predicted, observed: observed})
}

// Mispredicted reports whether any response contradicted the predicted step
func (rq *RenderQuery) Mispredicted() (target string, predicted, observed time.Duration, ok bool) {
	if m := rq.mismatch.Load(); m != nil {
		return m.target, m.predicted, m.observed, true
	}
	return "", 0, 0, false
}

// Leaves returns the resolved leaf paths of every target
func (rq *RenderQuery) Leaves() []string {
	var out []string
	for _, t := range rq.Targets {
		out = append(out, t.Resolution.Leaves...)
	}
	return out
}

// RenderOptions exposes the marshal-time parameters to the modeler
func (rq *RenderQuery) RenderOptions() model.RenderOptions {
	ro := model.RenderOptions{
		Format:        rq.Params.Format,
		MaxDataPoints: rq.Params.MaxDataPoints,
		NoNullPoints:  rq.Params.NoNullPoints,
		JSONP:         rq.Params.JSONP,
		Pretty:        rq.Params.Pretty,
		Location:      rq.Location,
	}
	if rq.Params.XFilesFactor != "" {
		if f, err := strconv.ParseFloat(rq.Params.XFilesFactor, 64); err == nil {
			ro.XFilesFactor = f
		}
	}
	for _, t := range rq.Targets {
		ro.PathExpressions = append(ro.PathExpressions, t.Source)
	}
	return ro
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
	rq := &RenderQuery{Params: rp, Location: c.location(rp.TZ), Now: time.Now().Truncate(time.Second)}
	trq.ParsedQuery = rq
	if len(rp.Targets) == 0 {
		return trq, nil, true, c.decline(rq, trq, qp, parsing.ReasonMissingTarget, upTarget, nil)
	}
	if rp.Declined != "" {
		return trq, nil, true, c.decline(rq, trq, qp, rp.Declined, rp.DeclinedParam, nil)
	}
	if o := c.Configuration(); o != nil && o.Graphite != nil &&
		o.Graphite.PassthroughMaxDataPoints && rp.MaxDataPoints > 0 {
		return trq, nil, true, c.decline(rq, trq, qp, parsing.ReasonPassthroughMaxPoints, "maxDataPoints", nil)
	}
	if rp.Now != "" {
		n, err := parsing.ParseATTime(rp.Now, rq.Location, rq.Now)
		if err != nil {
			return trq, nil, true, c.decline(rq, trq, qp, parsing.ReasonParseError, "now", err)
		}
		rq.Now = n
	}
	ext, err := parsing.ParseTimeRange(rp.From, rp.Until, rq.Location, rq.Now)
	if err != nil {
		return trq, nil, true, c.decline(rq, trq, qp, parsing.ReasonParseError, "from/until", err)
	}
	trq.Extent = ext
	rq.Age = rq.Now.Sub(ext.Start)
	rq.EffectiveAge = rq.Age

	rq.Targets = make([]Target, len(rp.Targets))
	canonical := make([]string, len(rp.Targets))
	for i, src := range rp.Targets {
		ast, err := parsing.ParseTarget(src)
		if err != nil {
			return trq, nil, true, c.decline(rq, trq, qp, parsing.ReasonParseError, upTarget, err)
		}
		cl := parsing.Classify(ast)
		if !cl.Accelerable() {
			return trq, nil, true, c.decline(rq, trq, qp, cl.Reason, cl.Offender, nil)
		}
		canonical[i] = parsing.Format(ast)
		rq.Targets[i] = Target{Source: src, Canonical: canonical[i], AST: ast, Class: cl}
	}
	// targets cannot contain a newline (the grammar admits printable ASCII
	// only), so it is a safe separator for the statement
	trq.Statement = strings.Join(canonical, "\n")

	// resolve the step of every target (design note §6.1); one query has
	// one step, so a multi-target request whose targets resolve differently
	// is declined here and split per target by the render handler (D5)
	var step, maxRet time.Duration
	for i := range rq.Targets {
		t := &rq.Targets[i]
		_, normalized := t.AST.(*parsing.Call)
		t.Resolution = c.resolver.Resolve(r.Context(), t.Class.Leaves, t.Class.FixedStep, rq.Age, normalized)
		if t.Resolution.Confidence == resolution.Unknown {
			return trq, nil, true, c.decline(rq, trq, qp, t.Resolution.Reason, t.Source, nil)
		}
		if step != 0 && t.Resolution.Step != step {
			return trq, nil, true, c.decline(rq, trq, qp, parsing.ReasonMultiTargetMismatch, t.Source, nil)
		}
		step = t.Resolution.Step
		if mr := t.Resolution.MaxRetention; mr > 0 && (maxRet == 0 || mr < maxRet) {
			maxRet = mr
		}
	}
	trq.Step = step

	// whisper's range clamp (§2.3): a window wholly beyond retention has no
	// data to cache; a from beyond retention is moved to the oldest point
	if maxRet > 0 {
		start, end, ok := resolution.Clamp(ext.Start, ext.End, rq.Now, maxRet)
		if !ok {
			return trq, nil, true, c.decline(rq, trq, qp, parsing.ReasonUnknownStep, "beyond maxRetention", nil)
		}
		trq.Extent.Start, trq.Extent.End = start, end
		rq.EffectiveAge = min(rq.Age, maxRet)
	}
	// the extent is the set of buckets the origin returns for this window
	// (§2.2): the first bucket strictly after from and the last at or
	// before until, both step-aligned; SetExtent inverts this per fetch
	first, afterLast := resolution.AlignInterval(trq.Extent.Start, trq.Extent.End, step)
	trq.Extent.Start, trq.Extent.End = first, afterLast.Add(-step)

	// cache key (7.2): the canonical target, the resolved leaf set, the
	// effective step and the registry generation; a relearned ladder or a
	// changed wildcard expansion therefore misses rather than collides
	keys := make([]string, 0, len(rq.Targets))
	for _, t := range rq.Targets {
		keys = append(keys, t.Resolution.ExpansionID)
	}
	trq.CacheKeyElements = map[string]string{
		upTarget: trq.Statement,
		"leaves": strings.Join(keys, ","),
		"step":   step.String(),
		"gen":    strconv.FormatUint(c.registry.Generation(), 10),
	}

	// backfill tolerance (7.9): carbon writes are not instantaneous and the
	// newest rung is fine-grained, so unless configured, tolerate two steps
	// and at least thirty seconds of recent data being rewritten
	if o := c.Configuration(); o != nil && o.BackfillTolerance == 0 && o.BackfillTolerancePoints == 0 {
		trq.BackfillTolerance = max(2*step, DefaultBackfillTolerance)
	}

	// maxDataPoints, format, noNullPoints, jsonp, pretty and tz are applied
	// at marshal time and are deliberately not in the cache key (D3, plan
	// item 4.6), so concurrent requests differing only in those must each be
	// rendered for themselves rather than sharing one marshaled body
	rlo := &timeseries.RequestOptions{
		FastForwardDisable: true, ProviderRequest: rq,
		MarshalVariesByRequest: true,
	}
	return trq, rlo, true, nil
}

// DefaultBackfillTolerance is the minimum backfill tolerance applied when
// the backend configures none
const DefaultBackfillTolerance = 30 * time.Second

// decline records a fallback and returns the error the DPC routes to the
// Object Proxy Cache lane. The render path's cache_key_params are chosen
// for the delta cache (the extent is not part of a DPC key), so an
// object-cached response must instead be keyed on every parameter, or two
// unaccelerated renders of one target with different windows would collide.
func (c *Client) decline(rq *RenderQuery, trq *timeseries.TimeRangeQuery, qp url.Values,
	reason, detail string, err error,
) error {
	rq.Fallback = reason
	trq.CacheKeyElements = OPCKeyElements(qp)
	if c.observer != nil {
		c.observer.Fallback(reason)
	}
	return &FallbackError{Reason: reason, Detail: detail, Err: err}
}

// OPCKeyElements keys an unaccelerated render on all of its parameters
func OPCKeyElements(qp url.Values) map[string]string {
	out := make(map[string]string, len(qp))
	for k, vals := range qp {
		out[k] = strings.Join(vals, "\x00")
	}
	return out
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
