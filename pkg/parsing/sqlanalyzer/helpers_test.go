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

package sqlanalyzer

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestObjectAnalysis(t *testing.T) {
	err := errors.New("nope")
	analysis := ObjectAnalysis(ReasonUnsupportedBucket, err)
	if analysis.Mode != CacheModeObject || analysis.Reason != ReasonUnsupportedBucket ||
		!errors.Is(analysis.Err, err) || analysis.Plan != nil {
		t.Fatalf("unexpected analysis: %+v", analysis)
	}
}

type conjunctionNode struct {
	op          string
	left, right *conjunctionNode
	leaf        string
	wrapped     *conjunctionNode
}

func splitConjunctionNode(n *conjunctionNode) (*conjunctionNode, *conjunctionNode, bool) {
	if n != nil && n.op == "AND" {
		return n.left, n.right, true
	}
	return nil, nil, false
}

func unwrapConjunctionNode(n *conjunctionNode) *conjunctionNode {
	for n != nil && n.wrapped != nil {
		n = n.wrapped
	}
	return n
}

func TestFlattenConjunction(t *testing.T) {
	a := &conjunctionNode{leaf: "a"}
	b := &conjunctionNode{leaf: "b"}
	c := &conjunctionNode{leaf: "c"}
	tree := &conjunctionNode{op: "AND",
		left:  &conjunctionNode{op: "AND", left: a, right: &conjunctionNode{wrapped: b}},
		right: &conjunctionNode{wrapped: &conjunctionNode{op: "AND", left: c, right: a}},
	}

	got := FlattenConjunction(tree, nil, splitConjunctionNode, nil)
	if len(got) != 3 || got[0] != a || got[1].wrapped != b {
		t.Fatalf("without normalize: %+v", got)
	}

	got = FlattenConjunction(tree, unwrapConjunctionNode, splitConjunctionNode, nil)
	want := []string{"a", "b", "c", "a"}
	if len(got) != len(want) {
		t.Fatalf("with normalize: %+v", got)
	}
	for i, leaf := range got {
		if leaf.leaf != want[i] {
			t.Fatalf("leaf %d = %q, want %q", i, leaf.leaf, want[i])
		}
	}

	seed := []*conjunctionNode{c}
	got = FlattenConjunction(a, nil, splitConjunctionNode, seed)
	if len(got) != 2 || got[0] != c || got[1] != a {
		t.Fatalf("seeded flatten: %+v", got)
	}
}

func TestBucketMath(t *testing.T) {
	step := time.Minute
	aligned := time.Unix(120, 0)
	unaligned := time.Unix(150, 500)

	if !AlignedToBucket(aligned, step, 0) || AlignedToBucket(unaligned, step, 0) {
		t.Fatal("alignment misclassified")
	}
	if AlignedToBucket(aligned, 0, 0) {
		t.Fatal("nonpositive step must never align")
	}
	if AlignedToBucket(aligned, step, 30*time.Second) {
		t.Fatal("phase offset ignored")
	}
	if !AlignedToBucket(time.Unix(150, 0), step, 30*time.Second) {
		t.Fatal("phase-aligned value misclassified")
	}

	if got := FloorBucket(unaligned, step, 0); !got.Equal(time.Unix(120, 0)) {
		t.Fatalf("floor = %v", got)
	}
	if got := FloorBucket(unaligned, step, 30*time.Second); !got.Equal(time.Unix(150, 0)) {
		t.Fatalf("phased floor = %v", got)
	}
	negative := time.Unix(-61, 500)
	if got := FloorBucket(negative, step, 0); !got.Equal(time.Unix(-120, 0)) {
		t.Fatalf("negative floor = %v", got)
	}
	if got := FloorBucket(unaligned, 0, 0); !got.Equal(unaligned) {
		t.Fatalf("nonpositive step floor = %v", got)
	}
	loc := time.FixedZone("test", 3600)
	if got := FloorBucket(unaligned.In(loc), step, 0); got.Location() != loc {
		t.Fatal("floor must preserve location")
	}

	if got := CeilBucket(aligned, step, 0); !got.Equal(aligned) {
		t.Fatalf("aligned ceil = %v", got)
	}
	if got := CeilBucket(unaligned, step, 0); !got.Equal(time.Unix(180, 0)) {
		t.Fatalf("ceil = %v", got)
	}
}

func TestUnixTime(t *testing.T) {
	tests := []struct {
		unit  timeseries.FieldDataType
		value int64
		want  time.Time
	}{
		{timeseries.DateTimeUnixSecs, 120, time.Unix(120, 0)},
		{timeseries.DateTimeUnixMilli, 120_500, time.Unix(120, 500_000_000)},
		{timeseries.DateTimeUnixMicro, 120_000_500, time.Unix(120, 500_000)},
		{timeseries.DateTimeUnixNano, 120_000_000_500, time.Unix(120, 500)},
		{timeseries.DateTimeSQL, 120, time.Unix(120, 0)},
	}
	for _, test := range tests {
		if got := UnixTime(test.value, test.unit); !got.Equal(test.want) {
			t.Errorf("UnixTime(%d, %v) = %v, want %v", test.value, test.unit, got, test.want)
		}
	}
}

func TestParseSQLTime(t *testing.T) {
	tests := []struct {
		value string
		want  int64
		ok    bool
	}{
		{"2020-01-02 03:04:05", 1_577_934_245, true},
		{"2020-01-02", 1_577_923_200, true},
		{"2020-01-02T03:04:05Z", 1_577_934_245, true},
		{"not-a-time", 0, false},
		{"it''s not a time", 0, false},
	}
	for _, test := range tests {
		got, ok := ParseSQLTime(test.value)
		if ok != test.ok || (ok && got.Unix() != test.want) {
			t.Errorf("ParseSQLTime(%q) = (%v, %t), want (%d, %t)", test.value, got, ok, test.want, test.ok)
		}
	}
}

func TestSafeUnixSeconds(t *testing.T) {
	limit := int64(1<<63-1) / int64(time.Second)
	for value, want := range map[int64]bool{
		0: true, limit: true, -limit: true, limit + 1: false, -limit - 1: false,
	} {
		if got := SafeUnixSeconds(value); got != want {
			t.Errorf("SafeUnixSeconds(%d) = %t, want %t", value, got, want)
		}
	}
}

func TestNewTimeRangeQuery(t *testing.T) {
	trq := NewTimeRangeQuery("SELECT 1")
	if trq.Statement != "SELECT 1" || trq.CacheKeyElements["query"] != "SELECT 1" {
		t.Fatalf("unexpected query: %+v", trq)
	}
}

func TestApplyToQuery(t *testing.T) {
	plan := &QueryPlan{
		CanonicalSQL: "SELECT <$TS1$>",
		TimeColumn:   "ts", OutputColumn: "t",
		Step: time.Minute, Phase: 4 * 24 * time.Hour,
		OutputUnit: timeseries.DateTimeUnixSecs, InputUnit: timeseries.DateTimeUnixMilli,
		GroupColumns: []string{"host", "region"}, BackfillTolerance: 30 * time.Second,
	}
	trq := NewTimeRangeQuery("SELECT raw")
	plan.ApplyToQuery(trq)
	if trq.Statement != plan.CanonicalSQL || trq.CacheKeyElements["query"] != plan.CanonicalSQL {
		t.Fatalf("canonical not applied: %+v", trq)
	}
	if trq.Step != time.Minute || trq.StepNS != time.Minute.Nanoseconds() || trq.Phase != plan.Phase ||
		trq.BackfillTolerance != plan.BackfillTolerance {
		t.Fatalf("cadence not applied: %+v", trq)
	}
	ts := trq.TimestampDefinition
	if ts.Name != "t" || ts.DataType != timeseries.DateTimeUnixSecs ||
		ts.Role != timeseries.RoleTimestamp || ts.ProviderData1 != byte(timeseries.DateTimeUnixMilli) {
		t.Fatalf("timestamp definition = %+v", ts)
	}
	if len(trq.TagFieldDefintions) != 2 || trq.TagFieldDefintions[0].Name != "host" ||
		trq.TagFieldDefintions[1].Role != timeseries.RoleTag {
		t.Fatalf("tag definitions = %+v", trq.TagFieldDefintions)
	}
	if trq.ParsedQuery != plan {
		t.Fatal("plan not attached")
	}

	bare := &timeseries.TimeRangeQuery{}
	plan.ApplyToQuery(bare)
	if bare.CacheKeyElements["query"] != plan.CanonicalSQL {
		t.Fatal("nil cache key elements not initialized")
	}
}

func TestRequestExtent(t *testing.T) {
	now := time.Unix(600, 0)
	step := time.Minute
	tests := []struct {
		name       string
		plan       QueryPlan
		start, end time.Time
	}{
		{"inclusive bounds",
			QueryPlan{Step: step,
				LowerBound: &Bound{Value: time.Unix(120, 0), Inclusive: true},
				UpperBound: &Bound{Value: time.Unix(300, 0), Inclusive: true}},
			time.Unix(120, 0), time.Unix(300, 0)},
		{"exclusive bounds",
			QueryPlan{Step: step,
				LowerBound: &Bound{Value: time.Unix(120, 0)},
				UpperBound: &Bound{Value: time.Unix(300, 0)}},
			time.Unix(180, 0), time.Unix(240, 0)},
		{"missing upper defaults to now",
			QueryPlan{Step: step, LowerBound: &Bound{Value: time.Unix(120, 0), Inclusive: true}},
			time.Unix(120, 0), now},
		{"missing lower leaves zero start",
			QueryPlan{Step: step, UpperBound: &Bound{Value: time.Unix(300, 0), Inclusive: true}},
			time.Time{}, time.Unix(300, 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extent := test.plan.RequestExtent(now)
			if !extent.Start.Equal(test.start) || !extent.End.Equal(test.end) {
				t.Fatalf("extent = %v, want [%v, %v]", extent, test.start, test.end)
			}
		})
	}
}

func TestCacheModeStringsAreLowCardinality(t *testing.T) {
	for _, mode := range []CacheMode{CacheModeNone, CacheModeObject, CacheModeDelta, CacheMode(99)} {
		if s := mode.String(); s == "" || strings.ContainsAny(s, " \t") {
			t.Fatalf("mode %d string = %q", mode, s)
		}
	}
}
