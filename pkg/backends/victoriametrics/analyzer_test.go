/*
 * Copyright 2026 The Trickster Authors
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

package victoriametrics

import (
	"hash/maphash"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		query  string
		mode   cacheMode
		reason string
	}{
		// split-invariant shapes, verified against VictoriaMetrics
		{`sum(trips_total) - sum(trips_total offset 5m)`, modeDelta, ""},
		{`sum by (borough) (rate(trips_total[5m]))`, modeDelta, ""},
		{`rate(trips_total)`, modeDelta, ""},
		{`WITH (d(m) = increase(m[1h]) keep_metric_names) sum by (__name__) (d({__name__=~"a|b"}))`, modeDelta, ""},
		{`histogram_quantiles("q", 0.5, 0.9, sum by (le) (rate(h_bucket)))`, modeDelta, ""},
		{`max_over_time(rate(m[5m])[30m:1m])`, modeDelta, ""},
		{`topk(3, m)`, modeDelta, ""},
		{`rollup(m[15m])`, modeDelta, ""},
		{`m @ 1700000000`, modeDelta, ""},
		{`clamp(m, 0, 1) # trickster-step-align:drop`, modeDelta, ""},
		{`label_set(time(), "a", "b")`, modeDelta, ""},
		// whole-range shapes are exact-request objects
		{`range_avg(m)`, modeObject, reasonRangeFunc},
		{`running_sum(m)`, modeObject, reasonRangeFunc},
		{`keep_last_value(m)`, modeObject, reasonRangeFunc},
		{`sort_desc(m)`, modeObject, reasonRangeFunc},
		{`m - start()`, modeObject, reasonRangeFunc},
		{`topk_avg(1, m)`, modeObject, reasonRangeAggr},
		{`limitk(1, m)`, modeObject, reasonRangeAggr},
		{`sum(m) by (a) limit 3`, modeObject, reasonLimit},
		{`m @ end()`, modeObject, reasonRangeFunc},
		{`m @ time()`, modeObject, reasonEvaluateAt},
		// volatile and unparsable statements are relayed
		{`now()`, modeProxy, reasonVolatile},
		{`range_avg(m) * rand()`, modeProxy, reasonVolatile},
		{`sum(`, modeProxy, reasonParse},
		{``, modeProxy, reasonParse},
		{`no_such_function(m)`, modeProxy, reasonParse},
	}
	for _, test := range tests {
		got := classify(test.query)
		if got.mode != test.mode || got.reason != test.reason || got.query != test.query {
			t.Errorf("%q: got (%d, %q) want (%d, %q)", test.query, got.mode, got.reason, test.mode, test.reason)
		}
	}
}

func TestAnalyzerRemembers(t *testing.T) {
	a := newAnalyzer()
	first := a.analyze("rate(m[5m])")
	if a.analyze("rate(m[5m])") != first {
		t.Error("a repeated statement was parsed again")
	}
	if other := a.analyze("range_avg(m)"); other == first || other.mode != modeObject {
		t.Errorf("distinct statements share an analysis: %+v", other)
	}
	// an entry stored under a statement's key for another statement is never returned for it
	a.table.Put(maphash.String(a.seed, "rate(m[5m])"), &analysis{query: "now()", mode: modeProxy}, time.Now().UnixNano())
	if got := a.analyze("rate(m[5m])"); got.mode != modeDelta {
		t.Errorf("got %+v", got)
	}
}
