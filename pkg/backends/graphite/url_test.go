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
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func TestSetExtent(t *testing.T) {
	c := newTestClient(t, nil)
	// a 7h-old query on the static ladder resolves to the 60s rung
	r := getReq("target=aliasByNode(a.b, 1)&from=-7h&until=now&format=csv&maxDataPoints=100&noNullPoints=1&jsonp=cb&pretty=1&tz=UTC&xFilesFactor=0.5")
	trq, _, _, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatal(err)
	}
	rq := trq.ParsedQuery.(*RenderQuery)
	if trq.Step != time.Minute {
		t.Fatalf("expected the 60s rung, got %v", trq.Step)
	}
	// a gap in the middle of the window
	gap := timeseries.Extent{Start: rq.Now.Add(-3 * time.Hour), End: rq.Now.Add(-2 * time.Hour)}
	up, _ := http.NewRequest(http.MethodGet, r.URL.String(), nil)
	if err := c.SetExtent(up, trq, &gap); err != nil {
		t.Fatal(err)
	}
	v, _, _ := params.GetRequestValues(up)
	from, _ := strconv.ParseInt(v.Get("from"), 10, 64)
	until, _ := strconv.ParseInt(v.Get("until"), 10, 64)
	now, _ := strconv.ParseInt(v.Get("now"), 10, 64)
	// from sits one step before the first bucket so whisper's +step rounding
	// lands on it; now is pinned so now-from keeps the original age and rung
	if from != gap.Start.Add(-time.Minute).Unix() || until != gap.End.Unix() {
		t.Errorf("from/until: %d %d want %d %d", from, until, gap.Start.Add(-time.Minute).Unix(), gap.End.Unix())
	}
	if time.Duration(now-from)*time.Second != rq.EffectiveAge || rq.EffectiveAge != 7*time.Hour {
		t.Errorf("now must be pinned to from + age: now-from=%ds age=%v", now-from, rq.EffectiveAge)
	}
	if v.Get("format") != "json" || v.Get("target") != "aliasByNode(a.b, 1)" || len(v["target"]) != 1 {
		t.Errorf("format/target: %v", v)
	}
	for _, p := range []string{"maxDataPoints", "noNullPoints", "jsonp", "pretty", "tz"} {
		if v.Get(p) != "" {
			t.Errorf("%s must be stripped upstream", p)
		}
	}
	if v.Get("xFilesFactor") != "0.5" {
		t.Error("xFilesFactor must be forwarded")
	}
	// POST form requests are rewritten in the body
	pr := postReq(url.Values{"target": {"a.b"}, "from": {"-1h"}, "format": {"json"}})
	trq2, _, _, err := c.ParseTimeRangeQuery(pr)
	if err != nil {
		t.Fatal(err)
	}
	up2 := postReq(url.Values{"target": {"a.b"}, "from": {"-1h"}, "format": {"json"}})
	if err := c.SetExtent(up2, trq2, &trq2.Extent); err != nil {
		t.Fatal(err)
	}
	v2, _, isBody := params.GetRequestValues(up2)
	if !isBody || v2.Get("now") == "" || v2.Get("from") == "" {
		t.Errorf("POST body not rewritten: %v", v2)
	}
	// errors
	if err := c.SetExtent(nil, trq, &gap); err == nil {
		t.Error("nil request")
	}
	if err := c.SetExtent(up, &timeseries.TimeRangeQuery{Step: time.Minute}, &gap); err == nil {
		t.Error("missing render query")
	}
	if err := c.SetExtent(up, &timeseries.TimeRangeQuery{ParsedQuery: rq}, &gap); err == nil {
		t.Error("zero step")
	}
	if err := c.SetExtent(up, trq, &timeseries.Extent{Start: gap.End, End: gap.Start}); err == nil {
		t.Error("inverted extent")
	}
}

func TestRenderWidenedFetchTrimmed(t *testing.T) {
	const leaf = "dev.medium.orders.us-east.count"
	h := newHarness(t)
	h.learn(leaf)
	// with now on a 5m boundary, -604801s adds exactly one 5m bucket ahead of
	// what -604799s and -604800s cached: a one-bucket head gap
	h.now = time.Now().Truncate(5 * time.Minute)
	for _, w := range []struct{ from, until string }{
		{"-3d", "-1d"},
		{"-604799s", "-5min"},
		{"-604800s", "-5min"},
		{"-604801s", "-5min"},
		// a one-bucket window on a cold 60s entry: a key miss, served uncropped
		{"-360s", "-5min"},
	} {
		for _, format := range []string{"json", "raw"} {
			q := h.query(url.Values{"target": {leaf}, "from": {w.from}, "until": {w.until}, "format": {format}})
			for pass := range 2 {
				h.same(fmt.Sprintf("%s..%s %s (pass %d)", w.from, w.until, format, pass), q)
				if h.lastRQ == nil || h.lastRQ.Fallback != "" {
					t.Fatalf("%s: must be accelerated", w.from)
				}
				if pass == 0 && format == "json" && w.from == "-604801s" && h.fetches == 0 {
					t.Fatal("vacuous: -604801s was a full cache hit, not a one-bucket head gap")
				}
			}
		}
	}
}

func TestTrimToExtent(t *testing.T) {
	pts := func(secs ...int64) dataset.Points {
		out := make(dataset.Points, len(secs))
		for i, s := range secs {
			out[i] = dataset.Point{Epoch: epoch.FromSecs(s), Size: 24, Values: []any{float64(s)}}
		}
		return out
	}
	s := &dataset.Series{Points: pts(100, 110, 120, 130), PointSize: 96}
	ds := &dataset.DataSet{Results: []*dataset.Result{nil, {SeriesList: []*dataset.Series{nil, s}}}}
	trimToExtent(ds, timeseries.Extent{})
	if len(s.Points) != 4 {
		t.Fatal("a zero extent must not trim")
	}
	trimToExtent(ds, timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(130, 0)})
	if len(s.Points) != 4 || s.PointSize != 96 {
		t.Fatal("points inside the extent must be kept")
	}
	trimToExtent(ds, timeseries.Extent{Start: time.Unix(110, 0), End: time.Unix(120, 0)})
	if len(s.Points) != 2 || s.Points[0].Epoch != epoch.FromSecs(110) || s.PointSize != 48 {
		t.Errorf("head and tail must be trimmed: %v (size %d)", s.Points, s.PointSize)
	}
	trimToExtent(ds, timeseries.Extent{Start: time.Unix(200, 0), End: time.Unix(300, 0)})
	if len(s.Points) != 0 || s.PointSize != 0 {
		t.Errorf("a disjoint extent must trim every point: %v (size %d)", s.Points, s.PointSize)
	}

	trq := &timeseries.TimeRangeQuery{
		Step:   10 * time.Second,
		Extent: timeseries.Extent{Start: time.Unix(110, 0), End: time.Unix(120, 0)},
	}
	ts, err := unmarshalFetch(strings.NewReader(`[{"target":"a","datapoints":[[1,100],[2,110],[3,120]]}]`), trq)
	if err != nil {
		t.Fatal(err)
	}
	if p := ts.(*dataset.DataSet).Results[0].SeriesList[0].Points; len(p) != 2 || p[0].Epoch != epoch.FromSecs(110) {
		t.Errorf("a fetch must be trimmed to the request's extent: %v", p)
	}
	if _, err := unmarshalFetch(strings.NewReader(`[{`), trq); err == nil {
		t.Error("an invalid body must fail")
	}
}
