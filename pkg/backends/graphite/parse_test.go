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
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/model"
	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	"github.com/trickstercache/trickster/v2/pkg/testutil/graphite/mockserver"
)

func newTestClient(t testing.TB, o *bo.Options) *Client {
	t.Helper()
	if o == nil {
		o = bo.New()
	}
	if o.Graphite == nil {
		o.Graphite = gro.New()
	}
	if o.Graphite.StaticRetentions == nil {
		o.Graphite.StaticRetentions = []gro.StaticRetention{
			// ordered, first match wins: slow.* sits on a coarser ladder so
			// that a multi-target request can disagree on the step
			{Pattern: `^slow\.`, Retentions: "5m:90d"},
			{Pattern: `.`, Retentions: "10s:6h,60s:7d,10m:5y"},
		}
	}
	origin := mockserver.New()
	t.Cleanup(origin.Close)
	for _, m := range []string{"a.b", "c.d", "dev.fast.requests.api.count", "dev.fast.requests.web.count", "c.e"} {
		origin.Add(m, "10s:6h,60s:7d,10m:5y")
	}
	// a metric on a different ladder, for the multi-target mismatch row
	origin.Add("slow.a", "5m:90d")
	u, _ := url.Parse(origin.URL)
	o.Scheme, o.Host = u.Scheme, u.Host
	c, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// keep the confirming probes from running: a negative cap refuses
	// every Schedule
	c.(*Client).learner.Concurrency = -1
	t.Cleanup(c.(*Client).Close)
	return c.(*Client)
}

func getReq(q string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "http://graphite/render?"+q, nil)
	return r
}

func postReq(v url.Values) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "http://graphite/render", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestParseTimeRangeQuery(t *testing.T) {
	c := newTestClient(t, nil)
	before := time.Now()
	r := getReq("target=aliasByNode(dev.fast.requests.*.count,%203)&from=-6h&until=now&format=json&maxDataPoints=743")
	trq, rlo, canOPC, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatal(err)
	}
	if !canOPC {
		t.Error("expected canOPC")
	}
	if trq.Statement != "aliasByNode(dev.fast.requests.*.count, 3)" {
		t.Errorf("unexpected statement %q", trq.Statement)
	}
	if trq.Step != 10*time.Second {
		t.Errorf("expected the resolved 10s step, got %v", trq.Step)
	}
	if trq.CacheKeyElements["target"] != trq.Statement || trq.CacheKeyElements["step"] != "10s" ||
		trq.CacheKeyElements["gen"] != "0" || trq.CacheKeyElements["leaves"] == "" {
		t.Errorf("unexpected cache key elements %v", trq.CacheKeyElements)
	}
	if trq.BackfillTolerance != DefaultBackfillTolerance {
		t.Errorf("expected the default backfill tolerance, got %v", trq.BackfillTolerance)
	}
	// the extent is the buckets whisper returns: (from, until] step-aligned
	if d := trq.Extent.End.Sub(trq.Extent.Start); d != 6*time.Hour-10*time.Second {
		t.Errorf("unexpected extent width %v", d)
	}
	if trq.Extent.End.After(time.Now()) || trq.Extent.End.Before(before.Add(-10*time.Second).Truncate(10*time.Second)) ||
		trq.Extent.Start.Unix()%10 != 0 {
		t.Errorf("unexpected extent end %v", trq.Extent.End)
	}
	if !rlo.FastForwardDisable {
		t.Error("fast forward must be disabled")
	}
	rq, ok := trq.ParsedQuery.(*RenderQuery)
	if !ok || rlo.ProviderRequest != rq {
		t.Fatalf("expected RenderQuery on both trq and rlo, got %T / %T", trq.ParsedQuery, rlo.ProviderRequest)
	}
	if len(rq.Targets) != 1 || rq.Targets[0].Source != "aliasByNode(dev.fast.requests.*.count, 3)" ||
		rq.Targets[0].Class.Step != parsing.StepInherit || len(rq.Targets[0].Class.Leaves) != 1 ||
		rq.Targets[0].Resolution.Confidence != resolution.Configured {
		t.Errorf("unexpected targets %+v", rq.Targets)
	}
	if rq.Age != 6*time.Hour || rq.EffectiveAge != 6*time.Hour || rq.Fallback != "" {
		t.Errorf("unexpected age %v/%v fallback %q", rq.Age, rq.EffectiveAge, rq.Fallback)
	}
	if rq.Params.MaxDataPoints != 743 || rq.Params.Format != "json" {
		t.Errorf("unexpected params %+v", rq.Params)
	}
	if rq.Location != time.UTC {
		t.Errorf("expected UTC, got %v", rq.Location)
	}
}

func TestParseTimeRangeQueryPOST(t *testing.T) {
	c := newTestClient(t, nil)
	v := url.Values{
		"target": {`aliasSub(aliasByNode(dev.fast.requests.*.count, 3), "(^.*$)", "\1 A")`,
			`aliasSub(alias(sumSeries(dev.fast.requests.*.count), 'total'), "(^.*$)", "\1 B")`},
		"from": {"1787322096"}, "until": {"1787343696"}, "format": {"json"}, "maxDataPoints": {"533"},
	}
	trq, _, _, err := c.ParseTimeRangeQuery(postReq(v))
	if err != nil {
		t.Fatal(err)
	}
	if trq.OriginalBody == nil {
		t.Error("expected the original body to be retained")
	}
	want := `aliasSub(aliasByNode(dev.fast.requests.*.count, 3), '(^.*$)', '\1 A')` + "\n" +
		`aliasSub(alias(sumSeries(dev.fast.requests.*.count), 'total'), '(^.*$)', '\1 B')`
	if trq.Statement != want {
		t.Errorf("unexpected statement %q", trq.Statement)
	}
	// absolute epochs far in the past resolve to the coarse rung and align
	first, afterLast := resolution.AlignInterval(time.Unix(1787322096, 0), time.Unix(1787343696, 0), trq.Step)
	if !trq.Extent.Start.Equal(first) || !trq.Extent.End.Equal(afterLast.Add(-trq.Step)) || trq.Step < time.Minute {
		t.Errorf("unexpected extent %v (step %v)", trq.Extent, trq.Step)
	}
	if len(trq.ParsedQuery.(*RenderQuery).Targets) != 2 {
		t.Error("expected 2 targets")
	}
}

func TestParseTimeRangeQueryClamp(t *testing.T) {
	c := newTestClient(t, nil)
	// a from beyond maxRetention is clamped to the oldest point the origin
	// holds, and the effective age pins every upstream fetch to that edge
	trq, _, _, err := c.ParseTimeRangeQuery(getReq("target=a.b&from=-10y&until=now&format=json"))
	if err != nil {
		t.Fatal(err)
	}
	rq := trq.ParsedQuery.(*RenderQuery)
	oldest := rq.Now.Add(-5 * 365 * 24 * time.Hour)
	if rq.Age <= 5*365*24*time.Hour || rq.EffectiveAge != 5*365*24*time.Hour ||
		trq.Extent.Start.Before(oldest) || trq.Extent.Start.Sub(oldest) > trq.Step {
		t.Errorf("expected a clamp to maxRetention, got age %v effective %v start %v", rq.Age, rq.EffectiveAge, trq.Extent.Start)
	}
	// absent from/until default to -1d..now
	trq, _, _, err = c.ParseTimeRangeQuery(getReq("target=a.b&format=json"))
	if err != nil {
		t.Fatal(err)
	}
	if d := trq.Extent.End.Sub(trq.Extent.Start); d != 24*time.Hour-trq.Step {
		t.Errorf("unexpected default extent width %v (step %v)", d, trq.Step)
	}
}

func TestParseTimeRangeQueryFallbacks(t *testing.T) {
	c := newTestClient(t, nil)
	pt := bo.New()
	ptc := newTestClient(t, pt)
	pt.Graphite.PassthroughMaxDataPoints = true
	tests := []struct {
		name   string
		c      *Client
		r      *http.Request
		reason string
		detail string
	}{
		{"no target", c, getReq("from=-1h&format=json"), parsing.ReasonMissingTarget, "target"},
		{"image format", c, getReq("target=a.b"), parsing.ReasonNonSeriesFormat, "format"},
		{"pie", c, getReq("target=a.b&format=json&graphType=pie"), parsing.ReasonNonSeriesFormat, "graphType"},
		{"maxStep", c, getReq("target=a.b&format=json&maxStep=60"), parsing.ReasonUnknownStep, "maxStep"},
		{"bad target", c, getReq("target=sumSeries(a.b&format=json"), parsing.ReasonParseError, "target"},
		{"bad from", c, getReq("target=a.b&from=-5m&format=json"), parsing.ReasonParseError, "from/until"},
		{"empty range", c, getReq("target=a.b&from=now&until=now&format=json"), parsing.ReasonParseError, "from/until"},
		{"bad now", c, getReq("target=a.b&now=bogus&format=json"), parsing.ReasonParseError, "now"},
		{"not allowlisted", c, getReq("target=movingAverage(a.b,'5min')&format=json"), parsing.ReasonFunctionNotAllowlisted, "movingAverage"},
		{"second target not allowlisted", c, getReq("target=a.b&target=highestMax(a.*,1)&format=json"), parsing.ReasonFunctionNotAllowlisted, "highestMax"},
		{"template", c, getReq("target=template(a.$1,'x')&format=json"), parsing.ReasonFunctionNotAllowlisted, "template"},
		{"generator", c, getReq("target=constantLine(1)&format=json"), parsing.ReasonFunctionNotAllowlisted, "constantLine"},
		{"targets on different ladders", c, getReq("target=a.b&target=slow.a&from=-1h&format=json"), parsing.ReasonMultiTargetMismatch, "slow.a"},
		{"beyond retention", c, getReq("target=a.b&from=-10y&until=-9y&format=json"), parsing.ReasonUnknownStep, "beyond maxRetention"},
		{"passthrough maxDataPoints", ptc, getReq("target=a.b&format=json&maxDataPoints=100"), parsing.ReasonPassthroughMaxPoints, "maxDataPoints"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			trq, rlo, canOPC, err := tc.c.ParseTimeRangeQuery(tc.r)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !canOPC || rlo != nil || trq == nil {
				t.Errorf("expected (trq, nil, true, err), got (%v, %v, %t)", trq, rlo, canOPC)
			}
			var fe *FallbackError
			if !errors.As(err, &fe) || !errors.Is(err, ErrNotAccelerable) {
				t.Fatalf("expected a FallbackError, got %T %v", err, err)
			}
			if fe.Reason != tc.reason || fe.Detail != tc.detail {
				t.Errorf("got reason=%s detail=%s want %s/%s", fe.Reason, fe.Detail, tc.reason, tc.detail)
			}
			if rq, ok := trq.ParsedQuery.(*RenderQuery); !ok || rq.Fallback != tc.reason {
				t.Errorf("the render query must record the fallback reason: %+v", trq.ParsedQuery)
			}
			// an object-cached fallback is keyed on every parameter
			if _, ok := trq.CacheKeyElements["from"]; !ok && tc.r.URL.Query().Get("from") != "" {
				t.Errorf("fallback key must include the window: %v", trq.CacheKeyElements)
			}
			if !strings.Contains(fe.Error(), fe.Reason) {
				t.Errorf("error text should name the reason: %s", fe.Error())
			}
		})
	}
	// passthrough off: the same request is accelerable
	if _, _, _, err := c.ParseTimeRangeQuery(getReq("target=a.b&format=json&maxDataPoints=100")); err != nil {
		t.Error(err)
	}
	// passthrough on but no maxDataPoints: still accelerable
	if _, _, _, err := ptc.ParseTimeRangeQuery(getReq("target=a.b&format=json")); err != nil {
		t.Error(err)
	}
}

func TestParseTimeRangeQueryTimeZone(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	o := bo.New()
	o.Graphite = gro.New()
	o.Graphite.TimeZone = "America/New_York"
	c := newTestClient(t, o)
	// configured zone applies to date-anchored values
	trq, _, _, err := c.ParseTimeRangeQuery(getReq("target=a.b&from=midnight&until=now&format=json"))
	if err != nil {
		t.Fatal(err)
	}
	y, m, d := time.Now().In(ny).Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, ny)
	if trq.Extent.Start.Before(midnight) || trq.Extent.Start.Sub(midnight) > trq.Step {
		t.Errorf("expected the first bucket after New York midnight, got %v (step %v)", trq.Extent.Start, trq.Step)
	}
	// a valid tz parameter overrides it
	trq, _, _, err = c.ParseTimeRangeQuery(getReq("target=a.b&from=midnight&until=now&format=json&tz=UTC"))
	if err != nil {
		t.Fatal(err)
	}
	if trq.ParsedQuery.(*RenderQuery).Location != time.UTC {
		t.Error("expected the tz parameter to win")
	}
	// an unknown tz is ignored, as graphite-web ignores it
	trq, _, _, err = c.ParseTimeRangeQuery(getReq("target=a.b&from=midnight&until=now&format=json&tz=Not/AZone"))
	if err != nil {
		t.Fatal(err)
	}
	if trq.ParsedQuery.(*RenderQuery).Location.String() != ny.String() {
		t.Error("expected the configured zone when tz is unknown")
	}
	// an unknown tz is cached as a miss and still resolves to the
	// configured zone on repetition
	trq, _, _, err = c.ParseTimeRangeQuery(getReq("target=a.b&from=midnight&until=now&format=json&tz=Not/AZone"))
	if err != nil {
		t.Fatal(err)
	}
	if trq.ParsedQuery.(*RenderQuery).Location.String() != ny.String() {
		t.Error("expected the configured zone on a cached tz miss")
	}
	// an unknown configured zone is a construction error
	bad := bo.New()
	bad.Graphite = gro.New()
	bad.Graphite.TimeZone = "Not/AZone"
	if _, err := NewClient("bad-tz", bad, nil, nil, nil, nil); err == nil {
		t.Error("expected an error for an invalid configured time_zone")
	}
	// an oversized tz value is not searched for
	if loc, ok := c.location(strings.Repeat("A/", 100)); !ok || loc.String() != ny.String() {
		t.Error("expected the configured zone for an oversized tz")
	}
	// the now parameter anchors relative ranges
	trq, _, _, err = c.ParseTimeRangeQuery(getReq("target=a.b&from=-1h&now=1787343600&format=json"))
	if err != nil {
		t.Fatal(err)
	}
	if trq.Extent.End.Unix() != 1787343600 || trq.Extent.Start.Unix() != 1787340000+int64(trq.Step/time.Second) {
		t.Errorf("unexpected extent %v", trq.Extent)
	}
}

func TestFallbackErrorUnwrap(t *testing.T) {
	inner := errors.New("inner")
	fe := &FallbackError{Reason: "x", Detail: "y", Err: inner}
	if !errors.Is(fe, inner) || !strings.Contains(fe.Error(), "inner") {
		t.Error("expected the wrapped error to be exposed")
	}
	if !errors.Is(fe, ErrNotAccelerable) {
		t.Error("expected sentinel match")
	}
}

func TestRenderOptions(t *testing.T) {
	c := newTestClient(t, nil)
	r := getReq("target=a.b&target=sumSeries(c.*)&from=-1h&format=csv&maxDataPoints=50&noNullPoints=1&jsonp=cb&pretty=1&xFilesFactor=0.5&tz=America/New_York")
	_, rlo, _, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := rlo.ProviderRequest.(model.RenderOptionsProvider)
	if !ok {
		t.Fatal("provider request must expose render options")
	}
	ro := p.RenderOptions()
	if ro.Format != "csv" || ro.MaxDataPoints != 50 || !ro.NoNullPoints || ro.JSONP != "cb" || !ro.Pretty ||
		ro.XFilesFactor != 0.5 || ro.Location.String() != "America/New_York" ||
		len(ro.PathExpressions) != 2 || ro.PathExpressions[1] != "sumSeries(c.*)" {
		t.Errorf("unexpected render options %+v", ro)
	}
	// an unparsable xFilesFactor is ignored
	_, rlo, _, _ = c.ParseTimeRangeQuery(getReq("target=a.b&format=json&xFilesFactor=x"))
	if rlo.ProviderRequest.(model.RenderOptionsProvider).RenderOptions().XFilesFactor != 0 {
		t.Error("bad xFilesFactor")
	}
}

func TestClientIdentityDeclinesAcceleration(t *testing.T) {
	c := newTestClient(t, nil)
	pc := &po.Options{Path: "/render", RequestHeaders: map[string]string{}, CacheKeyHeaders: []string{"X-Tenant"}}
	serve := func(hdrs map[string]string, pathConfig *po.Options) (*RenderQuery, bool, error) {
		r := getReq("target=a.b&from=-1h&format=json")
		for k, v := range hdrs {
			r.Header.Set(k, v)
		}
		rsc := request.NewResources(nil, pathConfig, nil, nil, nil, nil)
		r = request.SetResources(r, rsc)
		trq, rlo, canOPC, err := c.ParseTimeRangeQuery(r)
		rq, _ := trq.ParsedQuery.(*RenderQuery)
		_ = rlo
		return rq, canOPC, err
	}

	// no identity: accelerated
	rq, _, err := serve(nil, pc)
	if err != nil || rq.Fallback != "" {
		t.Fatalf("plain request must accelerate: %v %q", err, rq.Fallback)
	}
	// a client Authorization declines to the object lane
	rq, canOPC, err := serve(map[string]string{"Authorization": "Bearer tenant-a"}, pc)
	if err == nil || !canOPC || rq.Fallback != parsing.ReasonClientIdentity {
		t.Fatalf("client auth must decline: err=%v canOPC=%t fallback=%q", err, canOPC, rq.Fallback)
	}
	// so does a header the path declares result-affecting
	rq, _, err = serve(map[string]string{"X-Tenant": "a"}, pc)
	if err == nil || rq.Fallback != parsing.ReasonClientIdentity {
		t.Fatalf("cache_key_headers must decline: %v %q", err, rq.Fallback)
	}
	// a static override pins the upstream identity and lifts the decline,
	// provided the synthetic resolution paths carry the same identity
	hdrs := map[string]string{"Authorization": "Basic static", "X-Tenant": "static"}
	pinned := &po.Options{Path: "/render", Methods: methods.GetAndPost(),
		RequestHeaders: hdrs, CacheKeyHeaders: []string{"X-Tenant"}}
	pinnedExpand := &po.Options{Path: "/metrics/expand", Methods: methods.GetAndPost(),
		RequestHeaders: hdrs}
	c2 := newTestClient(t, nil)
	c2.Configuration().Paths = po.List{pinned, pinnedExpand}
	r := getReq("target=a.b&from=-1h&format=json")
	r.Header.Set("Authorization", "Bearer tenant-a")
	r.Header.Set("X-Tenant", "a")
	r = request.SetResources(r, request.NewResources(nil, pinned, nil, nil, nil, nil))
	trq, _, _, err := c2.ParseTimeRangeQuery(r)
	rq, _ = trq.ParsedQuery.(*RenderQuery)
	if err != nil || rq.Fallback != "" {
		t.Fatalf("statically pinned identity must accelerate: %v %q", err, rq.Fallback)
	}
	// a request rewriter on the path declines outright
	rewriting := &po.Options{Path: "/render",
		ReqRewriter: rewriter.RewriteInstructions{nil}}
	rq, _, err = serve(nil, rewriting)
	if err == nil || rq.Fallback != parsing.ReasonClientIdentity {
		t.Fatalf("a rewriter must decline: %v %q", err, rq.Fallback)
	}
}

func TestBackendRewriterDeclinesAcceleration(t *testing.T) {
	origin := mockserver.New()
	t.Cleanup(origin.Close)
	origin.Add("a.b", "10s:6h,60s:7d")
	u, _ := url.Parse(origin.URL)
	o := bo.New()
	o.Scheme, o.Host = u.Scheme, u.Host
	// a backend-level rewriter can retarget host/path/headers/params for live
	// requests only, so acceleration must fail closed to the object lane
	o.ReqRewriter = rewriter.RewriteInstructions{nil}
	b, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	t.Cleanup(c.Close)

	r := getReq("target=a.b&from=-1h&format=json")
	r = request.SetResources(r, request.NewResources(o, nil, nil, nil, nil, nil))
	trq, _, canOPC, err := c.ParseTimeRangeQuery(r)
	rq, _ := trq.ParsedQuery.(*RenderQuery)
	if err == nil || !canOPC || rq.Fallback != parsing.ReasonClientIdentity {
		t.Fatalf("a backend rewriter must decline: err=%v canOPC=%t fallback=%q",
			err, canOPC, rq.Fallback)
	}
	if reqs := origin.Requests(); len(reqs) != 0 {
		t.Errorf("declined request must trigger no probe or expansion, got %v", reqs)
	}
}

func TestResolutionIdentityMismatchDeclines(t *testing.T) {
	serve := func(c *Client, r *http.Request, pc *po.Options) (*RenderQuery, error) {
		r = request.SetResources(r, request.NewResources(nil, pc, nil, nil, nil, nil))
		trq, _, _, err := c.ParseTimeRangeQuery(r)
		rq, _ := trq.ParsedQuery.(*RenderQuery)
		return rq, err
	}

	// GET and POST /render pinned to different tenants: probes are GETs under
	// tenant A, so a POST render under tenant B must not accelerate
	getRender := &po.Options{Path: "/render", Methods: []string{http.MethodGet},
		RequestHeaders: map[string]string{"Authorization": "Basic tenant-a"}}
	postRender := &po.Options{Path: "/render", Methods: []string{http.MethodPost},
		RequestHeaders: map[string]string{"Authorization": "Basic tenant-b"}}
	expand := &po.Options{Path: "/metrics/expand", Methods: methods.GetAndPost(),
		RequestHeaders: map[string]string{"Authorization": "Basic tenant-a"}}
	c := newTestClient(t, nil)
	c.Configuration().Paths = po.List{getRender, postRender, expand}

	rq, err := serve(c, postReq(url.Values{
		"target": {"a.b"}, "from": {"-1h"}, "format": {"json"}}), postRender)
	if err == nil || rq.Fallback != parsing.ReasonResolutionIdentity {
		t.Fatalf("POST under a different identity must decline: %v %q", err, rq.Fallback)
	}
	rq, err = serve(c, getReq("target=a.b&from=-1h&format=json"), getRender)
	if err != nil || rq.Fallback != "" {
		t.Fatalf("GET under the synthetic identity must accelerate: %v %q", err, rq.Fallback)
	}

	// /render and /metrics/expand pinned to different tenants: probes and
	// expansion would mix namespaces, so no render accelerates
	render2 := &po.Options{Path: "/render", Methods: methods.GetAndPost(),
		RequestHeaders: map[string]string{"Authorization": "Basic tenant-a"}}
	expand2 := &po.Options{Path: "/metrics/expand", Methods: methods.GetAndPost(),
		RequestHeaders: map[string]string{"Authorization": "Basic tenant-b"}}
	c2 := newTestClient(t, nil)
	c2.Configuration().Paths = po.List{render2, expand2}
	rq, err = serve(c2, getReq("target=a.b&from=-1h&format=json"), render2)
	if err == nil || rq.Fallback != parsing.ReasonResolutionIdentity {
		t.Fatalf("render/expand identity mismatch must decline: %v %q", err, rq.Fallback)
	}
}

func TestWildcardCacheKeyParamsDecline(t *testing.T) {
	c := newTestClient(t, nil)
	pc := &po.Options{Path: "/render", CacheKeyParams: []string{"*"}}
	serve := func(q string) (*RenderQuery, error) {
		r := getReq(q)
		rsc := request.NewResources(nil, pc, nil, nil, nil, nil)
		r = request.SetResources(r, rsc)
		trq, _, _, err := c.ParseTimeRangeQuery(r)
		rq, _ := trq.ParsedQuery.(*RenderQuery)
		return rq, err
	}
	// delta-owned params under "*" do not decline
	rq, err := serve("target=a.b&from=-1h&format=json&maxDataPoints=100")
	if err != nil || rq.Fallback != "" {
		t.Fatalf("owned params must accelerate: %v %q", err, rq.Fallback)
	}
	// a view selector or arbitrary tenant param under "*" declines
	for _, q := range []string{
		"target=a.b&from=-1h&local=1",
		"target=a.b&from=-1h&tenant=acme",
	} {
		rq, err = serve(q)
		if err == nil || rq.Fallback != parsing.ReasonClientIdentity {
			t.Fatalf("%s: expected a client_identity decline, got %v %q", q, err, rq.Fallback)
		}
	}
	// a static request_params pin lifts it when the synthetic resolution
	// paths are pinned identically
	pinned := &po.Options{Path: "/render", CacheKeyParams: []string{"*"},
		Methods:       methods.GetAndPost(),
		RequestParams: map[string]string{"local": "1"}}
	pinnedExpand := &po.Options{Path: "/metrics/expand", Methods: methods.GetAndPost(),
		RequestParams: map[string]string{"local": "1"}}
	c2 := newTestClient(t, nil)
	c2.Configuration().Paths = po.List{pinned, pinnedExpand}
	r := getReq("target=a.b&from=-1h&format=json&local=1")
	rsc := request.NewResources(nil, pinned, nil, nil, nil, nil)
	r = request.SetResources(r, rsc)
	trq, _, _, err := c2.ParseTimeRangeQuery(r)
	rq, _ = trq.ParsedQuery.(*RenderQuery)
	if err != nil || rq.Fallback != "" {
		t.Fatalf("pinned local under \"*\" must accelerate: %v %q", err, rq.Fallback)
	}
}

func TestOPCKeyElementsEffectiveValues(t *testing.T) {
	qp := url.Values{
		"target": {"a.b"},
		"local":  {"junk"},
		"secret": {"c1"},
		"extra":  {"v1", "v2"},
	}
	pc := &po.Options{RequestParams: map[string]string{
		"local":   "1",
		"-secret": "",
		"+extra":  "cfg",
	}}
	out := OPCKeyElements(qp, pc)
	if _, ok := out["local"]; ok {
		t.Error("a replaced parameter must not contribute the discarded client value")
	}
	if _, ok := out["secret"]; ok {
		t.Error("a removed parameter must not contribute the discarded client value")
	}
	if v, ok := out["extra"]; !ok || v != "v1\x00v2" {
		t.Errorf("an appended parameter must key the client values, got %q", v)
	}
	if v, ok := out["target"]; !ok || v != "a.b" {
		t.Errorf("an untouched parameter must be keyed, got %q", v)
	}
	// nil path config keys everything
	if out := OPCKeyElements(qp, nil); len(out) != len(qp) {
		t.Errorf("nil path config must key all parameters, got %v", out)
	}
}
