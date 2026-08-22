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
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

func newTestClient(t *testing.T, o *bo.Options) *Client {
	t.Helper()
	if o == nil {
		o = bo.New()
	}
	c, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
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
	if trq.Step != 0 {
		t.Errorf("step must not be set by the parser, got %v", trq.Step)
	}
	if d := trq.Extent.End.Sub(trq.Extent.Start); d != 6*time.Hour {
		t.Errorf("unexpected extent width %v", d)
	}
	if trq.Extent.End.Before(before.Truncate(time.Second)) || trq.Extent.End.After(time.Now()) {
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
		rq.Targets[0].Class.Step != parsing.StepInherit || len(rq.Targets[0].Class.Leaves) != 1 {
		t.Errorf("unexpected targets %+v", rq.Targets)
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
	if trq.Extent.Start.Unix() != 1787322096 || trq.Extent.End.Unix() != 1787343696 {
		t.Errorf("unexpected extent %v", trq.Extent)
	}
	if len(trq.ParsedQuery.(*RenderQuery).Targets) != 2 {
		t.Error("expected 2 targets")
	}
}

func TestParseTimeRangeQueryFixedStep(t *testing.T) {
	c := newTestClient(t, nil)
	trq, _, _, err := c.ParseTimeRangeQuery(getReq("target=summarize(a.b,'1h')&target=summarize(c.d,'1h','max')&format=json"))
	if err != nil {
		t.Fatal(err)
	}
	if trq.Step != time.Hour {
		t.Errorf("expected a 1h fixed step, got %v", trq.Step)
	}
	// absent from/until default to -1d..now
	if d := trq.Extent.End.Sub(trq.Extent.Start); d != 24*time.Hour {
		t.Errorf("unexpected default extent width %v", d)
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
		{"mixed fixed steps", c, getReq("target=summarize(a.b,'1h')&target=summarize(c.d,'1d')&format=json"), parsing.ReasonMultiTargetMismatch, "summarize"},
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
	c := newTestClient(t, o)
	o.Graphite.TimeZone = "America/New_York"
	// configured zone applies to date-anchored values
	trq, _, _, err := c.ParseTimeRangeQuery(getReq("target=a.b&from=midnight&until=now&format=json"))
	if err != nil {
		t.Fatal(err)
	}
	y, m, d := time.Now().In(ny).Date()
	if !trq.Extent.Start.Equal(time.Date(y, m, d, 0, 0, 0, 0, ny)) {
		t.Errorf("expected New York midnight, got %v", trq.Extent.Start)
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
	// an unknown configured zone falls back to UTC
	o.Graphite.TimeZone = "Not/AZone"
	if c.location("") != time.UTC {
		t.Error("expected UTC fallback")
	}
	// the now parameter anchors relative ranges
	trq, _, _, err = c.ParseTimeRangeQuery(getReq("target=a.b&from=-1h&now=1787343600&format=json"))
	if err != nil {
		t.Fatal(err)
	}
	if trq.Extent.End.Unix() != 1787343600 || trq.Extent.Start.Unix() != 1787340000 {
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
