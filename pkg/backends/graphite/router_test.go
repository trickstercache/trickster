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
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/parsing"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/testutil/graphite/mockserver"
)

func newRouterClient(t *testing.T) *Client {
	t.Helper()
	o := bo.New()
	o.Graphite = gro.New()
	o.Graphite.StaticRetentions = []gro.StaticRetention{{Pattern: `^cfg\.`, Retentions: "60s:2d,5m:30d"}}
	origin := mockserver.New()
	t.Cleanup(origin.Close)
	for m, r := range map[string]string{
		"cfg.a":     "60s:2d,5m:30d",
		"learned.a": "10s:6h,60s:7d,10m:5y",
		"learned.b": "10s:6h,60s:7d,10m:5y",
		"short.a":   "5m:90d",
		"other.a":   "10s:6h,60s:7d,10m:5y",
	} {
		origin.Add(m, r)
	}
	u, _ := url.Parse(origin.URL)
	o.Scheme, o.Host = u.Scheme, u.Host
	b, err := NewClient("router", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	c.learner.Concurrency = -1 // no background learning: confidence stays deterministic
	t.Cleanup(c.Close)
	for _, leaf := range []string{"learned.a", "learned.b", "short.a"} {
		if _, err := c.learner.Learn(context.Background(), leaf, nil); err != nil {
			t.Fatalf("learn %s: %v", leaf, err)
		}
	}
	return c
}

func TestRouteTable(t *testing.T) {
	c := newRouterClient(t)
	tests := []struct {
		name       string
		query      string
		lane       Lane
		reason     string
		confidence resolution.Confidence
		source     string
		step       time.Duration
	}{
		// declined on parameters alone
		{"no target", "from=-1h&format=json", LaneObject, parsing.ReasonMissingTarget, resolution.Unknown, resolution.SourceNone, 0},
		{"image format (the default)", "target=learned.a&from=-1h", LaneObject, parsing.ReasonNonSeriesFormat, resolution.Unknown, resolution.SourceNone, 0},
		{"pie", "target=learned.a&from=-1h&format=json&graphType=pie", LaneObject, parsing.ReasonNonSeriesFormat, resolution.Unknown, resolution.SourceNone, 0},
		{"maxStep", "target=learned.a&from=-1h&format=json&maxStep=60", LaneObject, parsing.ReasonUnknownStep, resolution.Unknown, resolution.SourceNone, 0},
		// declined on the time grammar
		{"bad now", "target=learned.a&now=bogus&format=json", LaneObject, parsing.ReasonParseError, resolution.Unknown, resolution.SourceNone, 0},
		{"bad from", "target=learned.a&from=-5m&format=json", LaneObject, parsing.ReasonParseError, resolution.Unknown, resolution.SourceNone, 0},
		{"empty range", "target=learned.a&from=now&until=now&format=json", LaneObject, parsing.ReasonParseError, resolution.Unknown, resolution.SourceNone, 0},
		// declined on the target grammar and the function allowlist
		{"unparsable target", "target=sumSeries(&from=-1h&format=json", LaneObject, parsing.ReasonParseError, resolution.Unknown, resolution.SourceNone, 0},
		{"movingAverage", "target=movingAverage(learned.a,'5min')&from=-1h&format=json", LaneObject, parsing.ReasonFunctionNotAllowlisted, resolution.Unknown, resolution.SourceNone, 0},
		{"highestMax", "target=highestMax(learned.*,1)&from=-1h&format=json", LaneObject, parsing.ReasonFunctionNotAllowlisted, resolution.Unknown, resolution.SourceNone, 0},
		{"generator", "target=constantLine(5)&from=-1h&format=json", LaneObject, parsing.ReasonFunctionNotAllowlisted, resolution.Unknown, resolution.SourceNone, 0},
		{"template", "target=template(learned.$1,'a')&from=-1h&format=json", LaneObject, parsing.ReasonFunctionNotAllowlisted, resolution.Unknown, resolution.SourceNone, 0},
		// declined on resolution
		{"unresolvable step", "target=other.a&from=-1h&format=json", LaneObject, parsing.ReasonUnknownStep, resolution.Unknown, resolution.SourceNone, 0},
		{"no such metric", "target=nonexistent.*&from=-1h&format=json", LaneObject, parsing.ReasonMissingTarget, resolution.Unknown, resolution.SourceNone, 0},
		{"targets disagree on step", "target=learned.a&target=short.a&from=-1h&format=json", LaneObject, parsing.ReasonMultiTargetMismatch, resolution.Unknown, resolution.SourceNone, 0},
		{"wholly beyond maxRetention", "target=short.a&from=-120d&until=-100d&format=json", LaneObject, parsing.ReasonUnknownStep, resolution.Unknown, resolution.SourceNone, 0},
		// accelerated
		{"learned leaf", "target=learned.a&from=-1h&format=json", LaneDelta, "", resolution.Exact, resolution.SourceRegistry, 10 * time.Second},
		{"learned leaf, older rung", "target=learned.a&from=-7h&format=json", LaneDelta, "", resolution.Exact, resolution.SourceRegistry, time.Minute},
		{"wildcard over learned leaves", "target=aliasByNode(learned.*,0)&from=-1h&format=json", LaneDelta, "", resolution.Derived, resolution.SourceRegistry, 10 * time.Second},
		{"static config only", "target=cfg.a&from=-1h&format=json", LaneDelta, "", resolution.Configured, resolution.SourceStatic, time.Minute},
		{"step-fixing function", "target=summarize(learned.a,'1h')&from=-1h&format=json", LaneObject, parsing.ReasonFunctionNotAllowlisted, resolution.Unknown, resolution.SourceNone, 0},
		{"clamped to maxRetention", "target=short.a&from=-120d&format=json", LaneDelta, "", resolution.Exact, resolution.SourceRegistry, 5 * time.Minute},
	}
	seen := map[string]bool{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			trq, rlo, canOPC, err := c.ParseTimeRangeQuery(getReq(tc.query))
			rq, ok := trq.ParsedQuery.(*RenderQuery)
			if !ok {
				t.Fatal("expected a render query on every outcome")
			}
			if rq.Confidence != tc.confidence || rq.Source != tc.source {
				t.Errorf("confidence %v/%s, want %v/%s", rq.Confidence, rq.Source, tc.confidence, tc.source)
			}
			if tc.lane == LaneObject {
				var fe *FallbackError
				if !errors.As(err, &fe) || !errors.Is(err, ErrNotAccelerable) {
					t.Fatalf("expected a FallbackError, got %v", err)
				}
				if !canOPC {
					t.Error("every decline must offer the object cache lane")
				}
				if rlo != nil {
					t.Error("a declined request has no request options")
				}
				if fe.Reason != tc.reason || rq.Fallback != tc.reason {
					t.Errorf("reason %s / %s, want %s", fe.Reason, rq.Fallback, tc.reason)
				}
				if len(trq.CacheKeyElements) == 0 {
					t.Error("a declined request must still key its object-cached response")
				}
				seen[tc.reason] = true
				return
			}
			if err != nil {
				t.Fatalf("unexpected decline: %v", err)
			}
			if trq.Step != tc.step {
				t.Errorf("step %v, want %v", trq.Step, tc.step)
			}
			if rq.Fallback != "" || rlo == nil || !rlo.MarshalVariesByRequest {
				t.Errorf("unexpected accelerated state: fallback=%q rlo=%v", rq.Fallback, rlo)
			}
		})
	}
	// every frozen reason the router can produce must be exercised above
	for _, reason := range []string{
		parsing.ReasonMissingTarget, parsing.ReasonNonSeriesFormat, parsing.ReasonParseError,
		parsing.ReasonFunctionNotAllowlisted, parsing.ReasonUnknownStep, parsing.ReasonMultiTargetMismatch,
	} {
		if !seen[reason] {
			t.Errorf("routing table row for %q is not covered", reason)
		}
	}
}

func TestRoutePassthroughMaxDataPoints(t *testing.T) {
	c := newRouterClient(t)
	c.Configuration().Graphite.PassthroughMaxDataPoints = true
	// with passthrough configured, a request carrying maxDataPoints is
	// served by the origin so its consolidation is byte-identical
	trq, _, canOPC, err := c.ParseTimeRangeQuery(getReq("target=learned.a&from=-1h&format=json&maxDataPoints=100"))
	var fe *FallbackError
	if !errors.As(err, &fe) || fe.Reason != parsing.ReasonPassthroughMaxPoints || !canOPC {
		t.Errorf("expected a passthrough decline, got %v", err)
	}
	if rq := trq.ParsedQuery.(*RenderQuery); rq.Fallback != parsing.ReasonPassthroughMaxPoints {
		t.Errorf("unexpected fallback %q", rq.Fallback)
	}
	// without maxDataPoints the same request is still accelerated
	if _, _, _, err := c.ParseTimeRangeQuery(getReq("target=learned.a&from=-1h&format=json")); err != nil {
		t.Errorf("unexpected decline: %v", err)
	}
}

func TestLaneString(t *testing.T) {
	if LaneDelta.String() != "delta" || LaneObject.String() != "object" {
		t.Error("lane names")
	}
	if confidenceRank(resolution.Exact) <= confidenceRank(resolution.Derived) ||
		confidenceRank(resolution.Derived) <= confidenceRank(resolution.Configured) ||
		confidenceRank(resolution.Configured) <= confidenceRank(resolution.Unknown) {
		t.Error("confidence ranks must order Exact > Derived > Configured > Unknown")
	}
}

func TestRouteRequestShapeLimits(t *testing.T) {
	c := newTestClient(t, nil)
	ctx := context.Background()

	// too many targets: declined, and nothing was parsed or resolved
	many := make([]string, gro.DefaultMaxTargetsPerRequest+1)
	for i := range many {
		many[i] = "a.b"
	}
	rq := &RenderQuery{Params: parsing.RenderParams{Targets: many, From: "-1h", Format: "json"},
		Location: time.UTC, Now: time.Now()}
	d := c.route(ctx, rq)
	if d.Lane != LaneObject || d.Reason != parsing.ReasonParseError {
		t.Fatalf("count limit: lane %v reason %q", d.Lane, d.Reason)
	}
	if len(rq.Targets) != 0 {
		t.Fatal("targets must not be parsed once the limit is hit")
	}

	// an oversized single target: declined before ParseTarget sees it
	huge := strings.Repeat("a", gro.DefaultMaxTargetLength+1)
	rq = &RenderQuery{Params: parsing.RenderParams{Targets: []string{huge}, From: "-1h", Format: "json"},
		Location: time.UTC, Now: time.Now()}
	if d := c.route(ctx, rq); d.Lane != LaneObject || d.Reason != parsing.ReasonParseError {
		t.Fatalf("length limit: lane %v reason %q", d.Lane, d.Reason)
	}

	// raised limits admit the same shapes
	o := bo.New()
	o.Graphite = gro.New()
	o.Graphite.MaxTargetsPerRequest = len(many) + 1
	o.Graphite.MaxTargetLength = len(huge) + 1
	c2 := newTestClient(t, o)
	rq = &RenderQuery{Params: parsing.RenderParams{Targets: many, From: "-1h", Format: "json"},
		Location: time.UTC, Now: time.Now()}
	if d := c2.route(ctx, rq); d.Reason == parsing.ReasonParseError && len(rq.Targets) == 0 {
		t.Fatal("raised count limit still declined on shape")
	}
}

func BenchmarkRouteMultiTarget(b *testing.B) {
	c := newTestClient(b, nil)
	targets := make([]string, 128)
	for i := range targets {
		targets[i] = "a.b"
	}
	ctx := context.Background()
	// warm: resolve once so the registry answers every member
	warm := &RenderQuery{Params: parsing.RenderParams{Targets: targets[:1], From: "-1h", Format: "json"},
		Location: time.UTC, Now: time.Now()}
	c.route(ctx, warm)
	b.ReportAllocs()
	for b.Loop() {
		rq := &RenderQuery{Params: parsing.RenderParams{Targets: targets, From: "-1h", Format: "json"},
			Location: time.UTC, Now: time.Now()}
		if d := c.route(ctx, rq); d.Lane != LaneDelta {
			b.Fatalf("lane %v reason %s", d.Lane, d.Reason)
		}
	}
}
