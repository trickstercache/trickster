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

package promremote

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/promremote/prompb"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/proto"
)

func encodeReadResponse(t *testing.T, response *prompb.ReadResponse) []byte {
	t.Helper()
	b, err := proto.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return snappy.Encode(nil, b)
}

func decodeReadResponse(t *testing.T, body []byte) *prompb.ReadResponse {
	t.Helper()
	b, err := snappy.Decode(nil, body)
	if err != nil {
		t.Fatal(err)
	}
	response := &prompb.ReadResponse{}
	if err := proto.Unmarshal(b, response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestTimeseriesRoundTrip(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{
		Statement: "query",
		Step:      time.Millisecond,
		Extent: timeseries.Extent{
			Start: time.UnixMilli(100),
			End:   time.UnixMilli(200),
		},
	}
	wire := &prompb.ReadResponse{Results: []*prompb.QueryResult{{
		Timeseries: []*prompb.TimeSeries{{
			Labels: []*prompb.Label{
				{Name: "job", Value: "api"},
				{Name: "__name__", Value: "requests_total"},
			},
			Samples: []*prompb.Sample{
				{Timestamp: 200, Value: 2},
				{Timestamp: 100, Value: 1},
			},
		}},
	}}}

	ts, err := UnmarshalTimeseries(encodeReadResponse(t, wire), trq)
	if err != nil {
		t.Fatal(err)
	}
	ds := ts.(*dataset.DataSet)
	if len(ds.Results) != 1 || len(ds.Results[0].SeriesList) != 1 {
		t.Fatalf("dataset shape = %#v", ds.Results)
	}
	series := ds.Results[0].SeriesList[0]
	if series.Header.Name != "requests_total" || series.Header.Tags["job"] != "api" {
		t.Fatalf("series header = %#v", series.Header)
	}
	if len(series.Header.TagFieldsList) != 2 ||
		series.Header.TagFieldsList[0].Name != "__name__" ||
		series.Header.TagFieldsList[1].Name != "job" {
		t.Fatalf("tag fields = %#v", series.Header.TagFieldsList)
	}
	if series.Points[0].Epoch != epoch.Epoch(100*int64(time.Millisecond)) ||
		series.Points[1].Epoch != epoch.Epoch(200*int64(time.Millisecond)) {
		t.Fatalf("points were not sorted: %#v", series.Points)
	}

	body, err := MarshalTimeseries(ds, nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip := decodeReadResponse(t, body)
	got := roundTrip.Results[0].Timeseries[0]
	if got.Labels[0].Name != "__name__" || got.Labels[1].Name != "job" {
		t.Fatalf("labels were not sorted: %#v", got.Labels)
	}
	if got.Samples[0].Timestamp != 100 || got.Samples[1].Timestamp != 200 {
		t.Fatalf("samples = %#v", got.Samples)
	}
}

func TestMarshalTimeseriesWriterHeaders(t *testing.T) {
	ds := &dataset.DataSet{Results: dataset.Results{{}}}
	w := httptest.NewRecorder()
	if err := MarshalTimeseriesWriter(ds, nil, 200, w); err != nil {
		t.Fatal(err)
	}
	if w.Header().Get(headers.NameContentType) != ContentType ||
		w.Header().Get(headers.NameContentEncoding) != ContentEncoding {
		t.Fatalf("headers = %#v", w.Header())
	}
	response := decodeReadResponse(t, w.Body.Bytes())
	if len(response.Results) != 1 || response.Results[0] == nil {
		t.Fatalf("response = %#v", response)
	}
}

func TestUnmarshalTimeseriesErrors(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{}
	tests := map[string][]byte{
		"bad compression": []byte("not-snappy"),
		"no results":      encodeReadResponse(t, &prompb.ReadResponse{}),
		"two results": encodeReadResponse(t, &prompb.ReadResponse{Results: []*prompb.QueryResult{
			{}, {},
		}}),
		"duplicate label": encodeReadResponse(t, &prompb.ReadResponse{Results: []*prompb.QueryResult{{
			Timeseries: []*prompb.TimeSeries{{Labels: []*prompb.Label{
				{Name: "job", Value: "a"}, {Name: "job", Value: "b"},
			}}},
		}}}),
		"timestamp overflow": encodeReadResponse(t, &prompb.ReadResponse{Results: []*prompb.QueryResult{{
			Timeseries: []*prompb.TimeSeries{{Samples: []*prompb.Sample{{
				Timestamp: math.MaxInt64,
			}}}},
		}}}),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := UnmarshalTimeseries(body, trq); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := UnmarshalTimeseriesReader(nil, trq); err == nil {
		t.Fatal("nil reader must fail")
	}
}

func TestUnmarshalTimeseriesDecodeLimit(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{ParsedQuery: &parsedRequest{decodeLimit: 16}}

	t.Run("decoded size", func(t *testing.T) {
		body := binary.AppendUvarint(nil, 17)
		if _, err := UnmarshalTimeseries(body, trq); !errors.Is(err, errBodyTooLarge) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("compressed size", func(t *testing.T) {
		body := bytes.Repeat([]byte{0}, snappy.MaxEncodedLen(16)+1)
		if _, err := UnmarshalTimeseries(body, trq); !errors.Is(err, errBodyTooLarge) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestMarshalTimeseriesErrors(t *testing.T) {
	if _, err := MarshalTimeseries(nil, nil, 200); err == nil {
		t.Fatal("nil dataset must fail")
	}
	ds := &dataset.DataSet{Results: dataset.Results{{SeriesList: dataset.SeriesList{{
		Header: dataset.SeriesHeader{Tags: dataset.Tags{"job": "api"}},
		Points: dataset.Points{{Epoch: 1, Values: []any{float64(1)}}},
	}}}}}
	if _, err := MarshalTimeseries(ds, nil, 200); err == nil {
		t.Fatal("sub-millisecond timestamp must fail")
	}
	ds.Results[0].SeriesList[0].Points[0] = dataset.Point{
		Epoch:  epoch.Epoch(time.Millisecond),
		Values: []any{"1"},
	}
	if _, err := MarshalTimeseries(ds, nil, 200); err == nil {
		t.Fatal("non-float sample must fail")
	}
}

func TestEmptyResultRoundTrip(t *testing.T) {
	wire := &prompb.ReadResponse{Results: []*prompb.QueryResult{{}}}
	ts, err := UnmarshalTimeseries(encodeReadResponse(t, wire), &timeseries.TimeRangeQuery{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := MarshalTimeseries(ts, nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeReadResponse(t, body)
	if len(got.Results) != 1 || len(got.Results[0].Timeseries) != 0 {
		t.Fatalf("response = %#v", got)
	}
}

func TestMarshalTimeseriesToBuffer(t *testing.T) {
	ds := &dataset.DataSet{Results: dataset.Results{{}}}
	var buf bytes.Buffer
	if err := MarshalTimeseriesWriter(ds, nil, 200, &buf); err != nil {
		t.Fatal(err)
	}
	decodeReadResponse(t, buf.Bytes())
}
