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

package cockroach

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"
)

const (
	dialectEpochSelect  = "SELECT c - c % 600 AS time, avg(v) FROM m WHERE "
	dialectEpochBounds  = "c >= 1788998400 AND c <= 1789002000"
	dialectGrouped      = " GROUP BY 1 ORDER BY 1"
	dialectBucketSelect = "SELECT date_bin(INTERVAL '5 minutes', ts) AS time, max(v::int) FROM m WHERE "
	dialectUnnamed      = "?column?"
)

var dialectExtent = timeseries.Extent{
	Start: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 18, 11, 0, 0, 0, time.UTC),
}

func moduloBucket(expr tree.Expr) (BucketMatch, bool) {
	// matches c - c % N, an epoch-seconds bucket that is no function call.
	difference, ok := expr.(*tree.BinaryExpr)
	if !ok {
		return BucketMatch{}, false
	}
	column, ok := ColumnName(difference.Left)
	if !ok {
		return BucketMatch{}, false
	}
	return BucketMatch{
		TimeColumn: column, Step: 10 * time.Minute, OutputUnit: timeseries.DateTimeUnixSecs,
		ColumnUnit: timeseries.DateTimeUnixSecs, OutputColumn: dialectUnnamed,
	}, true
}

func TestExprBucketMatcher(t *testing.T) {
	a := NewAnalyzer(Options{
		BucketMatchers: DataFusionBucketMatchers(), ExprBucketMatchers: []ExprBucketMatcher{moduloBucket},
	})
	got := a.Analyze(dialectEpochSelect+dialectEpochBounds+dialectGrouped, time.Time{})
	if got.Mode != sqlanalyzer.CacheModeDelta || got.Plan.OutputUnit != timeseries.DateTimeUnixSecs ||
		got.Plan.TimeColumn != "c" || got.Plan.OutputColumn != "time" {
		t.Fatalf("got %v / %v / %+v", got.Mode, got.Err, got.Plan)
	}
	unaliased := a.Analyze("SELECT c - c % 600, avg(v) FROM m WHERE "+dialectEpochBounds+" GROUP BY 1", time.Time{})
	if unaliased.Plan == nil || unaliased.Plan.OutputColumn != dialectUnnamed {
		t.Fatalf("the matcher names an unaliased bucket: %+v", unaliased)
	}
	// a function bucket still reports timestamps, and wins over the expression matchers
	function := a.Analyze("SELECT date_bin(INTERVAL '1 hour', ts) AS time, avg(v) FROM m "+
		"WHERE ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00Z'"+dialectGrouped, time.Time{})
	if function.Plan == nil || function.Plan.OutputUnit != timeseries.DateTimeRFC3339Nano {
		t.Fatalf("got %+v", function)
	}
	for name, where := range map[string]string{
		"timestamp bounds on an epoch column":    "c >= '2026-09-18T08:00:00Z' AND c < '2026-09-18T11:00:00Z'",
		"millisecond bounds on a seconds column": "c >= 1788998400000 AND c < 1789002000000",
		"mixed upper bound":                      "c >= 1788998400 AND c < 1789002000000",
	} {
		rejected := a.Analyze(dialectEpochSelect+where+dialectGrouped, time.Time{})
		if rejected.Mode != sqlanalyzer.CacheModeObject || rejected.Reason != sqlanalyzer.ReasonUnsafePredicate {
			t.Fatalf("%s: got %v / %v", name, rejected.Mode, rejected.Reason)
		}
	}
	two := a.Analyze("SELECT c - c % 600 AS time, d - d % 600 AS other FROM m WHERE "+dialectEpochBounds+" GROUP BY 1, 2", time.Time{})
	if two.Reason != sqlanalyzer.ReasonUnsupportedBucket || !errors.Is(two.Err, ErrAmbiguousTimeAxis) {
		t.Fatalf("two buckets are ambiguous: %v / %v", two.Reason, two.Err)
	}
}

func TestBoundPrecision(t *testing.T) {
	for name, test := range map[string]struct {
		precision time.Duration
		where     string
		want      string
	}{
		"nanosecond tick by default":           {0, "ts BETWEEN '2026-09-18T08:00:00Z' AND '2026-09-18T11:00:00Z'", "AND '2026-09-18T11:04:59.999999999Z'"},
		"microsecond tick":                     {time.Microsecond, "ts BETWEEN '2026-09-18T08:00:00Z' AND '2026-09-18T11:00:00Z'", "AND '2026-09-18T11:04:59.999999Z'"},
		"sql timestamps":                       {time.Microsecond, "ts >= '2026-09-18 08:00:00' AND ts <= '2026-09-18 11:00:00'", "<= '2026-09-18 11:04:59.999999'"},
		"a coarser literal keeps its own tick": {time.Microsecond, "ts >= 1788998400 AND ts <= 1789002000", "<= 1789729499"},
		"exclusive bounds are unaffected":      {time.Microsecond, "ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00Z'", "< '2026-09-18T11:05:00Z'"},
	} {
		a := NewAnalyzer(Options{BucketMatchers: DataFusionBucketMatchers(), BoundPrecision: test.precision})
		got := a.Analyze(dialectBucketSelect+test.where+dialectGrouped, time.Time{})
		if got.Plan == nil {
			t.Fatalf("%s: got %v / %v", name, got.Reason, got.Err)
		}
		rendered, err := got.Plan.RenderExtent(dialectExtent)
		if err != nil || !strings.Contains(rendered, test.want) {
			t.Fatalf("%s: %q is missing from %s (%v)", name, test.want, rendered, err)
		}
	}
	// a bucket finer than the engine can address has no tick below its boundary
	a := NewAnalyzer(Options{BucketMatchers: DataFusionBucketMatchers(), BoundPrecision: time.Hour})
	got := a.Analyze(dialectBucketSelect+"ts BETWEEN '2026-09-18T08:00:00Z' AND '2026-09-18T11:00:00Z'"+dialectGrouped, time.Time{})
	if got.Reason != sqlanalyzer.ReasonUnsafePredicate {
		t.Fatalf("got %v / %v", got.Reason, got.Err)
	}
}

func TestRejectZonelessBounds(t *testing.T) {
	a := NewAnalyzer(Options{BucketMatchers: DataFusionBucketMatchers(), RejectZonelessBounds: true})
	for where, mode := range map[string]sqlanalyzer.CacheMode{
		"ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00+02:00'":         sqlanalyzer.CacheModeDelta,
		"ts >= 1788998400 AND ts < 1789002000":                                      sqlanalyzer.CacheModeDelta,
		"ts >= '2026-09-18 08:00:00' AND ts < '2026-09-18 11:00:00'":                sqlanalyzer.CacheModeObject,
		"ts >= '2026-09-18T08:00:00Z' AND ts < TIMESTAMP '2026-09-18 11:00:00'":     sqlanalyzer.CacheModeObject,
		"ts >= '2026-09-18' AND ts < '2026-09-19T00:00:00Z'":                        sqlanalyzer.CacheModeObject,
		"ts >= '2026-09-18 08:00:00'::timestamp - INTERVAL '1 hour' AND ts < now()": sqlanalyzer.CacheModeObject,
	} {
		got := a.Analyze(dialectBucketSelect+where+dialectGrouped, dialectExtent.End)
		if got.Mode != mode || mode == sqlanalyzer.CacheModeObject && got.Reason != sqlanalyzer.ReasonUnsafePredicate {
			t.Fatalf("%s: got %v / %v / %v", where, got.Mode, got.Reason, got.Err)
		}
	}
}

func TestNakedIntIsInt4(t *testing.T) {
	const where = "ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00Z'"
	for narrow, want := range map[bool]string{true: "v::INT4", false: "v::INT8"} {
		a := NewAnalyzer(Options{BucketMatchers: DataFusionBucketMatchers(), NakedIntIsInt4: narrow})
		got := a.Analyze(dialectBucketSelect+where+dialectGrouped, time.Time{})
		if got.Plan == nil || !strings.Contains(got.Plan.CanonicalSQL, want) {
			t.Fatalf("int4=%t: got %+v", narrow, got)
		}
	}
}

func TestPostRender(t *testing.T) {
	const where = "ts >= '2026-09-18T08:00:00Z' AND ts < '2026-09-18T11:00:00Z'"
	a := NewAnalyzer(Options{
		BucketMatchers: DataFusionBucketMatchers(),
		PostRender: func(rendered string) (string, error) {
			if strings.Contains(rendered, "forbidden") {
				return "", errors.New("no faithful spelling")
			}
			return strings.ReplaceAll(rendered, "INT8", "bigint"), nil
		},
	})
	got := a.Analyze(dialectBucketSelect+where+dialectGrouped, time.Time{})
	if got.Plan == nil || !strings.Contains(got.Plan.CanonicalSQL, "v::bigint") {
		t.Fatalf("got %+v", got)
	}
	rendered, err := got.Plan.RenderExtent(dialectExtent)
	if err != nil || !strings.Contains(rendered, "v::bigint") || !strings.Contains(rendered, "'2026-09-18T10:00:00Z'") {
		t.Fatalf("got %q %v", rendered, err)
	}
	rejected := a.Analyze(strings.Replace(dialectBucketSelect, "max(", "max(forbidden + ", 1)+where+dialectGrouped, time.Time{})
	if rejected.Mode != sqlanalyzer.CacheModeObject || rejected.Reason != sqlanalyzer.ReasonUnsupportedFormat ||
		!errors.Is(rejected.Err, ErrUnsupportedStatement) {
		t.Fatalf("got %v / %v / %v", rejected.Mode, rejected.Reason, rejected.Err)
	}
}

func TestMaskPlaceholders(t *testing.T) {
	const sql = "ts >= <$TRICKSTER_TS1_0$> AND ts < <$TS2$> AND note = $$x$$"
	masked := MaskPlaceholders(sql)
	if len(masked) != len(sql) || strings.Contains(masked, "<$") || !strings.HasSuffix(masked, "$$x$$") {
		t.Fatalf("got %q", masked)
	}
}
