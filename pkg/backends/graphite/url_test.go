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
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
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
