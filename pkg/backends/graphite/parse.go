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
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/model"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// Common URL Parameter Names
const (
	upTarget = "target"
)

// FallbackError is returned by ParseTimeRangeQuery when a render request is
// valid but must not be accelerated; Reason is a frozen parsing.Reason* value.
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
	// Age is now - from before any retention clamp (selects the archive rung);
	// EffectiveAge is Age capped at the leaves' maxRetention (see SetExtent)
	Age, EffectiveAge time.Duration
	// Fallback is the frozen reason when the request was declined, or empty
	Fallback string
	// Confidence is the weakest resolution confidence across the request's
	// targets, and Source where it came from; both are frozen label values
	Confidence resolution.Confidence
	Source     string

	mismatch  atomic.Pointer[stepMismatch]
	ambiguous atomic.Pointer[string]
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

// NoteAmbiguousStep records that a response series was too short to
// confirm the predicted step (implements model.StepAmbiguityNoter)
func (rq *RenderQuery) NoteAmbiguousStep(seriesName string, _ time.Duration) {
	rq.ambiguous.CompareAndSwap(nil, &seriesName)
}

// AmbiguousStep reports the first response series that could not confirm
// the predicted step, if any
func (rq *RenderQuery) AmbiguousStep() (string, bool) {
	if p := rq.ambiguous.Load(); p != nil {
		return *p, true
	}
	return "", false
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

// ParseTimeRangeQuery parses a TimeRangeQuery from the inbound render request,
// returning a non-nil error with canOPC=true when it must not be accelerated.
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error,
) {
	trq := &timeseries.TimeRangeQuery{}
	qp, body, isBody := params.GetRequestValues(r)
	if isBody {
		trq.OriginalBody = body
	}
	rp := parsing.ParseRenderParams(qp)
	loc, tzOK := c.location(rp.TZ)
	rq := &RenderQuery{Params: rp, Location: loc, Now: c.timeNow().Truncate(time.Second)}
	trq.ParsedQuery = rq

	// resolution components outlive any single request but tracers are
	// per-request; publish the first one seen for probe and learning spans
	var pc *po.Options
	if rsc := request.GetResources(r); rsc != nil {
		c.tracers.Set(rsc.Tracer)
		pc = rsc.PathConfig
	}

	if detail, dependent := c.requestDependentIdentity(r, qp); dependent {
		d := RouteDecision{
			Lane: LaneObject, Confidence: resolution.Unknown,
			Source: resolution.SourceNone, Reason: parsing.ReasonClientIdentity, Detail: detail,
		}
		rq.Confidence, rq.Source = d.Confidence, d.Source
		c.logDecision(rq, d)
		return trq, nil, true, c.decline(rq, trq, qp, pc, d.Reason, d.Detail, nil)
	}

	// synthetic resolution requests carry the backend's one static identity;
	// a request whose identity differs must not share backend-wide state
	if detail, mismatched := c.resolutionIdentityMismatch(pc); mismatched {
		d := RouteDecision{
			Lane: LaneObject, Confidence: resolution.Unknown,
			Source: resolution.SourceNone, Reason: parsing.ReasonResolutionIdentity, Detail: detail,
		}
		rq.Confidence, rq.Source = d.Confidence, d.Source
		c.logDecision(rq, d)
		return trq, nil, true, c.decline(rq, trq, qp, pc, d.Reason, d.Detail, nil)
	}

	// an undetermined tz fails closed to the object lane, where the original
	// tz reaches graphite-web, rather than being reparsed in another zone
	if !tzOK {
		d := RouteDecision{
			Lane: LaneObject, Confidence: resolution.Unknown,
			Source: resolution.SourceNone, Reason: parsing.ReasonTZUnavailable, Detail: rp.TZ,
		}
		rq.Confidence, rq.Source = d.Confidence, d.Source
		c.logDecision(rq, d)
		return trq, nil, true, c.decline(rq, trq, qp, pc, d.Reason, d.Detail, nil)
	}

	// one decision, from the confidence routing table
	d := c.route(r.Context(), rq)
	rq.Confidence, rq.Source = d.Confidence, d.Source
	c.logDecision(rq, d)
	if d.Lane == LaneObject {
		return trq, nil, true, c.decline(rq, trq, qp, pc, d.Reason, d.Detail, d.Err)
	}

	// targets cannot contain a newline (the grammar admits printable ASCII
	// only), so it is a safe separator for the statement
	canonical := make([]string, len(rq.Targets))
	keys := make([]string, len(rq.Targets))
	for i, t := range rq.Targets {
		canonical[i], keys[i] = t.Canonical, t.Resolution.ExpansionID
	}
	trq.Statement = strings.Join(canonical, "\n")
	trq.Extent, trq.Step = d.Extent, d.Step

	// key on canonical target, leaf set, step and registry generation, so a
	// relearned ladder or changed expansion misses rather than collides
	trq.CacheKeyElements = map[string]string{
		upTarget: trq.Statement,
		"leaves": strings.Join(keys, ","),
		"step":   d.Step.String(),
		"gen":    strconv.FormatUint(c.registry.Generation(), 10),
	}

	// carbon writes are not instantaneous and the newest rung is fine-grained,
	// so unless configured, tolerate two steps and at least 30s of rewrites
	if o := c.Configuration(); o != nil && o.BackfillTolerance == 0 && o.BackfillTolerancePoints == 0 {
		trq.BackfillTolerance = max(2*d.Step, DefaultBackfillTolerance)
	}

	// marshal-time parameters are deliberately not in the cache key, so
	// requests differing only in those must each be marshaled for themselves
	rlo := &timeseries.RequestOptions{
		FastForwardDisable: true, ProviderRequest: rq,
		MarshalVariesByRequest: true,
	}
	return trq, rlo, true, nil
}

func (c *Client) logDecision(rq *RenderQuery, d RouteDecision) {
	var target string
	if len(rq.Params.Targets) > 0 {
		target = rq.Params.Targets[0]
	}
	pairs := logging.Pairs{
		"backendName": c.Name(),
		"target":      target,
		"targets":     len(rq.Params.Targets),
		"ageBucket":   ageBucket(rq.Age),
		"lane":        d.Lane.String(),
		"confidence":  d.Confidence.String(),
		"source":      d.Source,
	}
	if d.Lane == LaneDelta {
		pairs["step"] = d.Step.String()
		if d.MaxRetention > 0 {
			pairs["maxRetention"] = d.MaxRetention.String()
		}
		logger.Debug("graphite resolution", pairs)
		return
	}
	pairs["reason"] = d.Reason
	pairs["detail"] = d.Detail
	if d.Err != nil {
		pairs["error"] = d.Err.Error()
	}
	logger.Debug("graphite resolution declined", pairs)
}

// coarsens a query age to typical whisper ladder boundaries for legible logs
func ageBucket(age time.Duration) string {
	for _, b := range []struct {
		limit time.Duration
		name  string
	}{
		{time.Hour, "<=1h"},
		{6 * time.Hour, "<=6h"},
		{24 * time.Hour, "<=1d"},
		{7 * 24 * time.Hour, "<=7d"},
		{30 * 24 * time.Hour, "<=30d"},
		{365 * 24 * time.Hour, "<=1y"},
	} {
		if age <= b.limit {
			return b.name
		}
	}
	return ">1y"
}

// DefaultBackfillTolerance is the minimum backfill tolerance applied when
// the backend configures none
const DefaultBackfillTolerance = 30 * time.Second

var deltaOwnedParams = sets.New([]string{
	upTarget, "target[]", "from", "until", "now", "tz", "format", "jsonp",
	"pretty", "maxDataPoints", "noNullPoints", "xFilesFactor", "rawData",
	"pickle", "graphType",
})

func (c *Client) requestDependentIdentity(r *http.Request, qp url.Values) (string, bool) {
	rsc := request.GetResources(r)
	var pc *po.Options
	if rsc != nil {
		pc = rsc.PathConfig
		if rsc.BackendOptions != nil && len(rsc.BackendOptions.ReqRewriter) > 0 {
			return "request_rewriter", true
		}
	}
	if pc != nil && len(pc.ReqRewriter) > 0 {
		return "request_rewriter", true
	}
	identityHeaders := []string{"Authorization"}
	if pc != nil {
		for _, h := range pc.CacheKeyHeaders {
			identityHeaders = append(identityHeaders, http.CanonicalHeaderKey(h))
		}
	}
	for _, h := range identityHeaders {
		if r.Header.Get(h) != "" && !pc.ReplacesHeader(h) {
			return h, true
		}
	}
	if pc == nil {
		return "", false
	}
	// "*" declares every present parameter result-affecting
	keyParams := pc.CacheKeyParams
	if len(keyParams) == 1 && keyParams[0] == "*" {
		keyParams = slices.Collect(maps.Keys(qp))
	}
	for _, p := range keyParams {
		if deltaOwnedParams.Contains(p) {
			continue
		}
		if len(qp[p]) > 0 && !pc.ReplacesParam(p) {
			return p, true
		}
	}
	for _, f := range pc.CacheKeyFormFields {
		if len(qp[f]) > 0 && !pc.ReplacesParam(f) {
			return f, true
		}
	}
	return "", false
}

func (c *Client) decline(rq *RenderQuery, trq *timeseries.TimeRangeQuery, qp url.Values,
	pc *po.Options, reason, detail string, err error,
) error {
	rq.Fallback = reason
	trq.CacheKeyElements = OPCKeyElements(qp, pc)
	if c.observer != nil {
		c.observer.Fallback(reason)
	}
	return &FallbackError{Reason: reason, Detail: detail, Err: err}
}

// OPCKeyElements keys an unaccelerated render on its effective parameters:
// one request_params replaces or removes contributes no client-varying value
func OPCKeyElements(qp url.Values, pc *po.Options) map[string]string {
	out := make(map[string]string, len(qp))
	for k, vals := range qp {
		if pc.ReplacesParam(k) {
			continue // the replacement is keyed via the path IdentityKeyPart
		}
		out[k] = strings.Join(vals, "\x00")
	}
	return out
}

// ok=false means tz validity could not be determined within the cold-load
// budget and the caller must decline
func (c *Client) location(tz string) (*time.Location, bool) {
	switch tz {
	case "":
		return c.configuredLoc, true
	case "UTC":
		return time.UTC, true
	}
	loc, res := c.tzCache.get(tz)
	switch res {
	case tzValid:
		return loc, true
	case tzUnavailable:
		return c.configuredLoc, false
	}
	return c.configuredLoc, true // known-invalid name
}
