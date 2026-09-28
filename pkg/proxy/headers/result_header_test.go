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

package headers

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestSetResultsHeader(t *testing.T) {
	h := http.Header{}
	SetResultsHeader(h, "test-engine", "test-status", "test-ffstatus",
		timeseries.ExtentList{timeseries.Extent{Start: time.Unix(1, 0), End: time.Unix(2, 0)}}, nil)
	const expected = "engine=test-engine; status=test-status; fetched=[1000-2000]; ffstatus=test-ffstatus"
	if h.Get(NameTricksterResult) != expected {
		t.Errorf("expected %s got %s", expected, h.Get(NameTricksterResult))
	}
}

func TestSetResultsHeaderEmtpy(t *testing.T) {
	h := http.Header{}
	SetResultsHeader(h, "", "test-status", "test-ffstatus",
		timeseries.ExtentList{timeseries.Extent{Start: time.Unix(1, 0), End: time.Unix(2, 0)}}, nil)
	if len(h) > 0 {
		t.Errorf("Expected header length of %d", 0)
	}
}

func TestMergeResultHeaderVals(t *testing.T) {
	const h1 = "status=kmiss; ffstatus=kmiss"
	const h2 = "engine=ObjectProxyCache; status=phit; fetched=[1612804980000-1612804980000]; ffstatus=hit"
	const ex2 = "engine=ObjectProxyCache; status=phit; fetched=[1612804980000-1612804980000]; ffstatus=phit"

	if res := MergeResultHeaderVals("", h2); res != h2 {
		t.Errorf("unexpected merged header: %s", res)
	}

	if res := MergeResultHeaderVals("x", h2); res != h2 {
		t.Errorf("unexpected merged header: %s", res)
	}

	if res := MergeResultHeaderVals(h1, h2); res != ex2 {
		t.Errorf("unexpected merged header: %s", res)
	}

	if res := MergeResultHeaderVals(h2, h2); res != h2 {
		t.Errorf("unexpected merged header: %s", res)
	}
}

func TestMergeResultHeaderValsFetchedDisjoint(t *testing.T) {
	const h1 = "engine=ObjectProxyCache; status=hit; fetched=[1000-2000]; ffstatus=hit"
	const h2 = "engine=ObjectProxyCache; status=hit; fetched=[5000-6000]; ffstatus=hit"
	const expected = "engine=ObjectProxyCache; status=hit; fetched=[1000-2000;5000-6000]; ffstatus=hit"
	if res := MergeResultHeaderVals(h1, h2); res != expected {
		t.Errorf("expected %q got %q", expected, res)
	}
}

func TestMergeResultHeaderValsFailedDisjoint(t *testing.T) {
	const h1 = "engine=DeltaProxyCache; status=proxy-error; failed=[1000-2000]"
	const h2 = "engine=DeltaProxyCache; status=proxy-error; failed=[5000-6000]"
	const expected = "engine=DeltaProxyCache; status=proxy-error; failed=[1000-2000;5000-6000]"
	if res := MergeResultHeaderVals(h1, h2); res != expected {
		t.Errorf("expected %q got %q", expected, res)
	}
}

func TestParseResultHeaderVals(t *testing.T) {
	const h1 = "engine=ObjectProxyCache; status=phit; fetched=[aaa-bbb]; ffstatus=hit"
	const h2 = "engine=ObjectProxyCache; status=phit; fetched=[11-bbb]; ffstatus=hit"

	const expected = "engine=ObjectProxyCache; status=phit; ffstatus=hit"
	res := parseResultHeaderVals(h1).String()

	if res != expected {
		t.Errorf("unexpected parsed header: %s", res)
	}

	res = parseResultHeaderVals(h2).String()
	if res != expected {
		t.Errorf("unexpected parsed header: %s", res)
	}

	// t.Error()
}

func TestParseResultEngineStatus(t *testing.T) {
	engine, status := ParseResultEngineStatus(
		"engine=DeltaProxyCache; status=phit; fetched=[1000-2000]; ffstatus=hit")
	if engine != "DeltaProxyCache" || status != cachestatus.StatusPartialHit {
		t.Errorf("engine/status = %q/%q", engine, status)
	}
}

func TestResultHeaderPartialBuckets(t *testing.T) {
	start := timeseries.Extent{Start: time.UnixMilli(1612804950000), End: time.UnixMilli(1612804980000)}
	end := timeseries.Extent{Start: time.UnixMilli(1612808580000), End: time.UnixMilli(1612808595000)}
	p := ResultHeaderParts{
		Engine: "DeltaProxyCache", Status: "hit",
		PartialBuckets: []PartialBucketResult{
			{Extent: start, Edge: timeseries.BucketEdgeStart, Status: "hit"},
			{Extent: end, Edge: timeseries.BucketEdgeEnd, Status: "kmiss"},
		},
	}
	const want = "engine=DeltaProxyCache; status=hit; partial_buckets=[1612804950000-1612804980000:start:hit;" +
		"1612808580000-1612808595000:end:kmiss]"
	if got := p.String(); got != want {
		t.Fatalf("got %s", got)
	}
	parsed := ParseResultHeader(want)
	if !reflect.DeepEqual(parsed.PartialBuckets, p.PartialBuckets) {
		t.Errorf("round trip: got %+v", parsed.PartialBuckets)
	}
	if parsed.String() != want {
		t.Errorf("reformatted: got %s", parsed.String())
	}

	t.Run("malformed entries are skipped", func(t *testing.T) {
		got := ParseResultHeader("engine=DeltaProxyCache; partial_buckets=[1-2:start;x-2:end:hit;1-y:end:hit;" +
			"1-2:middle:hit;1:end:hit;3-4:end:err]").PartialBuckets
		want := []PartialBucketResult{{
			Extent: timeseries.Extent{Start: time.UnixMilli(3), End: time.UnixMilli(4)},
			Edge:   timeseries.BucketEdgeEnd, Status: "err",
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("merge concatenates, and same bucket entries merge like ffstatus", func(t *testing.T) {
		h1 := "engine=DeltaProxyCache; status=hit; partial_buckets=[1-2:start:hit;5-6:end:hit]"
		h2 := "engine=DeltaProxyCache; status=hit; partial_buckets=[1-2:start:hit;5-6:end:kmiss;7-8:end:err]"
		got := ParseResultHeader(MergeResultHeaderVals(h1, h2)).PartialBuckets
		want := []PartialBucketResult{
			{Extent: timeseries.Extent{Start: time.UnixMilli(1), End: time.UnixMilli(2)}, Edge: timeseries.BucketEdgeStart, Status: "hit"},
			{Extent: timeseries.Extent{Start: time.UnixMilli(5), End: time.UnixMilli(6)}, Edge: timeseries.BucketEdgeEnd, Status: "phit"},
			{Extent: timeseries.Extent{Start: time.UnixMilli(7), End: time.UnixMilli(8)}, Edge: timeseries.BucketEdgeEnd, Status: "err"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
		if got := MergeResultHeaderVals("engine=DeltaProxyCache; status=hit", h2); !strings.Contains(got,
			"partial_buckets=[1-2:start:hit;5-6:end:kmiss;7-8:end:err]") {
			t.Errorf("one-sided merge: got %s", got)
		}
	})
}

func TestResultHeaderPreEpochRanges(t *testing.T) {
	ext := func(start, end int64) timeseries.Extent {
		return timeseries.Extent{Start: time.UnixMilli(start), End: time.UnixMilli(end)}
	}
	before, crossing := ext(-2000, -1000), ext(-1000, 1000)
	p := ResultHeaderParts{
		Engine: "DeltaProxyCache", Status: "phit",
		Fetched: timeseries.ExtentList{before, crossing},
		PartialBuckets: []PartialBucketResult{
			{Extent: before, Edge: timeseries.BucketEdgeStart, Status: "hit"},
			{Extent: crossing, Edge: timeseries.BucketEdgeEnd, Status: "kmiss"},
		},
	}
	h := p.String()
	if !strings.Contains(h, "partial_buckets=[-2000--1000:start:hit;-1000-1000:end:kmiss]") {
		t.Fatalf("unexpected header %s", h)
	}
	parsed := ParseResultHeader(h)
	if !reflect.DeepEqual(parsed.PartialBuckets, p.PartialBuckets) {
		t.Errorf("partial buckets round trip: got %+v", parsed.PartialBuckets)
	}
	if len(parsed.Fetched) != 2 || !parsed.Fetched[0].Start.Equal(before.Start) ||
		!parsed.Fetched[0].End.Equal(before.End) || !parsed.Fetched[1].Start.Equal(crossing.Start) {
		t.Errorf("fetched round trip: got %v", parsed.Fetched)
	}
	if parsed.String() != h {
		t.Errorf("reformatted: got %s want %s", parsed.String(), h)
	}

	t.Run("tsm merge", func(t *testing.T) {
		other := ResultHeaderParts{
			Engine: "DeltaProxyCache", Status: "hit",
			PartialBuckets: []PartialBucketResult{
				{Extent: before, Edge: timeseries.BucketEdgeStart, Status: "kmiss"},
				{Extent: ext(-5000, -4000), Edge: timeseries.BucketEdgeStart, Status: "hit"},
			},
		}
		got := ParseResultHeader(MergeResultHeaderVals(h, other.String())).PartialBuckets
		want := []PartialBucketResult{
			{Extent: before, Edge: timeseries.BucketEdgeStart, Status: "phit"},
			{Extent: crossing, Edge: timeseries.BucketEdgeEnd, Status: "kmiss"},
			{Extent: ext(-5000, -4000), Edge: timeseries.BucketEdgeStart, Status: "hit"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("malformed signed ranges are skipped", func(t *testing.T) {
		for _, rng := range []string{"-", "--1000", "-1000-", "-1000--", "1000", "-a-1"} {
			if _, ok := parseMillisRange(rng); ok {
				t.Errorf("%q should not parse", rng)
			}
		}
	})
}
