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

package sql

import (
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func groupedTRQ() *timeseries.TimeRangeQuery {
	return &timeseries.TimeRangeQuery{
		Statement: "test",
		Step:      time.Hour,
		TimestampDefinition: timeseries.FieldDefinition{
			Name: "time", Role: timeseries.RoleTimestamp,
		},
		TagFieldDefintions: timeseries.FieldDefinitions{
			{Name: "host", Role: timeseries.RoleTag},
		},
	}
}

const groupedJSON = `[` +
	`{"time":"2024-01-01T00:00:00Z","host":"a","usage":1},` +
	`{"time":"2024-01-01T00:00:00Z","host":"b","usage":2},` +
	`{"time":"2024-01-01T01:00:00Z","host":"a","usage":3},` +
	`{"time":"2024-01-01T01:00:00Z","host":"b","usage":4}]`

// TestGroupedRowsPartitionIntoSeries covers the core delta-merge requirement:
// rows for distinct GROUP BY tag values must land in distinct series so
// same-epoch points are never collapsed across tags.
func TestGroupedRowsPartitionIntoSeries(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(groupedJSON), groupedTRQ())
	if err != nil {
		t.Fatal(err)
	}
	ds := ts.(*dataset.DataSet)
	if len(ds.Results) != 1 || len(ds.Results[0].SeriesList) != 2 {
		t.Fatalf("expected 2 series, got %+v", ds.Results)
	}
	for i, wantHost := range []string{"a", "b"} {
		series := ds.Results[0].SeriesList[i]
		if series.Header.Tags["host"] != wantHost {
			t.Fatalf("series %d tags = %v, want host=%s", i, series.Header.Tags, wantHost)
		}
		if len(series.Points) != 2 {
			t.Fatalf("series %d has %d points, want 2", i, len(series.Points))
		}
		if len(series.Header.TagFieldsList) != 1 ||
			series.Header.TagFieldsList[0].Name != "host" {
			t.Fatalf("series %d tag fields = %v", i, series.Header.TagFieldsList)
		}
	}
	// distinct tags must yield distinct series identities for merging
	h0 := ds.Results[0].SeriesList[0].Header.CalculateHash()
	h1 := ds.Results[0].SeriesList[1].Header.CalculateHash()
	if h0 == h1 {
		t.Fatal("series with different tags share a header hash")
	}
}

// TestGroupedRoundTripPreservesTags verifies tags survive marshal in every
// output format with column order time, tags, values.
func TestGroupedRoundTripPreservesTags(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(groupedJSON), groupedTRQ())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		format byte
		want   []string
	}{
		{"json", iofmt.V3OutputJSON,
			[]string{`{"time":"2024-01-01T00:00:00","host":"a","usage":1}`, `"host":"b"`}},
		{"jsonl", iofmt.V3OutputJSONL,
			[]string{`{"time":"2024-01-01T00:00:00","host":"a","usage":1}`}},
		{"csv", iofmt.V3OutputCSV, []string{"time,host,usage", "2024-01-01T00:00:00,a,1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := MarshalTimeseries(ts,
				&timeseries.RequestOptions{OutputFormat: tc.format}, 200)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(string(out), want) {
					t.Fatalf("marshaled %s missing %q:\n%s", tc.name, want, out)
				}
			}
		})
	}
}

// TestColumnOrderPreserved verifies SELECT-list column order survives the
// round trip instead of being alphabetized or randomized.
func TestColumnOrderPreserved(t *testing.T) {
	body := `[{"time":"2024-01-01T00:00:00Z","zeta":1,"alpha":2,"mid":3}]`
	trq := &timeseries.TimeRangeQuery{Statement: "test",
		TimestampDefinition: timeseries.FieldDefinition{Name: "time"}}
	ts, err := UnmarshalTimeseries([]byte(body), trq)
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalTimeseries(ts, &timeseries.RequestOptions{}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out),
		`{"time":"2024-01-01T00:00:00","zeta":1,"alpha":2,"mid":3}`) {
		t.Fatalf("column order not preserved: %s", out)
	}
}

func TestTypeFidelity(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{Statement: "test",
		TimestampDefinition: timeseries.FieldDefinition{Name: "time"}}

	t.Run("json integers stay integers", func(t *testing.T) {
		body := `[{"time":"2024-01-01T00:00:00Z","count":9007199254740993,"ratio":1.5}]`
		ts, err := UnmarshalTimeseries([]byte(body), trq)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := MarshalTimeseries(ts, &timeseries.RequestOptions{}, 200)
		if !strings.Contains(string(out), "9007199254740993") {
			t.Fatalf("int64 lost precision: %s", out)
		}
		if !strings.Contains(string(out), "1.5") {
			t.Fatalf("float lost: %s", out)
		}
	})

	t.Run("csv integers stay integers", func(t *testing.T) {
		body := "time,count\n2024-01-01T00:00:00Z,42\n"
		ts, err := UnmarshalTimeseries([]byte(body), trq)
		if err != nil {
			t.Fatal(err)
		}
		ds := ts.(*dataset.DataSet)
		if v := ds.Results[0].SeriesList[0].Points[0].Values[0]; v != int64(42) {
			t.Fatalf("csv integer = %v (%T), want int64(42)", v, v)
		}
	})

	t.Run("null first row does not stringify column", func(t *testing.T) {
		body := `[{"time":"2024-01-01T00:00:00Z","v":null},` +
			`{"time":"2024-01-01T01:00:00Z","v":2.5}]`
		ts, err := UnmarshalTimeseries([]byte(body), trq)
		if err != nil {
			t.Fatal(err)
		}
		points := ts.(*dataset.DataSet).Results[0].SeriesList[0].Points
		if points[0].Values[0] != nil {
			t.Fatalf("null not preserved: %v", points[0].Values[0])
		}
		if v := points[1].Values[0]; v != 2.5 {
			t.Fatalf("value after null = %v (%T), want 2.5", v, v)
		}
	})

	t.Run("large csv float not exponentiated", func(t *testing.T) {
		body := "time,v\n2024-01-01T00:00:00Z,1234567.5\n"
		ts, err := UnmarshalTimeseries([]byte(body), trq)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := MarshalTimeseries(ts,
			&timeseries.RequestOptions{OutputFormat: iofmt.V3OutputCSV}, 200)
		if !strings.Contains(string(out), "1234567.5") {
			t.Fatalf("float formatting changed value: %s", out)
		}
	})
}

func TestUnmarshalRobustness(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{Statement: "test",
		TimestampDefinition: timeseries.FieldDefinition{Name: "time"}}

	t.Run("header-only csv is an empty result", func(t *testing.T) {
		ts, err := UnmarshalTimeseries([]byte("time,v\n"), trq)
		if err != nil {
			t.Fatalf("header-only csv errored: %v", err)
		}
		if n := len(ts.(*dataset.DataSet).Results[0].SeriesList); n != 0 {
			t.Fatalf("expected empty series list, got %d", n)
		}
	})

	t.Run("unparsable timestamp skips row", func(t *testing.T) {
		body := `[{"time":"not-a-time","v":1},{"time":"2024-01-01T00:00:00Z","v":2}]`
		ts, err := UnmarshalTimeseries([]byte(body), trq)
		if err != nil {
			t.Fatal(err)
		}
		points := ts.(*dataset.DataSet).Results[0].SeriesList[0].Points
		if len(points) != 1 || points[0].Epoch == 0 {
			t.Fatalf("bad-timestamp row not skipped: %+v", points)
		}
	})

	t.Run("malformed jsonl line errors", func(t *testing.T) {
		body := "{\"time\":\"2024-01-01T00:00:00Z\",\"v\":1}\n{not json}\n"
		if _, err := UnmarshalTimeseries([]byte(body), trq); err == nil {
			t.Fatal("malformed JSONL line was silently dropped")
		}
	})

	t.Run("naive v3 timestamps", func(t *testing.T) {
		for _, in := range []string{
			"2024-01-01T00:00:00", "2024-01-01T00:00:00.5", "2024-01-01 00:00:00",
		} {
			if _, err := parseV3Timestamp(in); err != nil {
				t.Fatalf("parseV3Timestamp(%s): %v", in, err)
			}
		}
		ep, err := parseV3Timestamp("2024-01-01T00:00:10")
		if err != nil || int64(ep) != 1704067210*int64(time.Second) {
			t.Fatalf("naive timestamp misparsed: %d, %v", ep, err)
		}
	})

	t.Run("epoch magnitudes", func(t *testing.T) {
		for _, tc := range []struct {
			in   string
			want int64
		}{
			{"1704067200", 1704067200 * int64(time.Second)},
			{"1704067200000", 1704067200 * int64(time.Second)},
			{"1704067200000000000", 1704067200 * int64(time.Second)},
		} {
			ep, err := parseV3Timestamp(tc.in)
			if err != nil || int64(ep) != tc.want {
				t.Fatalf("parseV3Timestamp(%s) = %d, %v; want %d", tc.in, ep, err, tc.want)
			}
		}
	})
}
