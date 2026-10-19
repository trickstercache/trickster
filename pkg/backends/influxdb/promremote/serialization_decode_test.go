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
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"testing/iotest"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/promremote/prompb"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/klauspost/compress/snappy"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func unmarshalGenerated(data []byte, trq *timeseries.TimeRangeQuery) (*dataset.DataSet, error) {
	// the generated protobuf types decode the response as the decoder once did, for the wire decoder to match
	decoded, err := decodeSnappyBlock(data, responseDecodeLimit(trq.ParsedQuery))
	if err != nil {
		return nil, err
	}
	response := &prompb.ReadResponse{}
	if err := proto.Unmarshal(decoded, response); err != nil {
		return nil, err
	}
	if len(response.Results) != 1 || response.Results[0] == nil {
		return nil, errUnexpectedResults
	}
	result := &dataset.Result{}
	for _, ws := range response.Results[0].Timeseries {
		tags := make(dataset.Tags, len(ws.Labels))
		for _, l := range ws.Labels {
			if _, ok := tags[l.Name]; ok {
				return nil, errDuplicateLabel
			}
			tags[l.Name] = l.Value
		}
		h := seriesHeader(tags, trq.Statement)
		h.CalculateSize()
		points := make(dataset.Points, len(ws.Samples))
		for i, s := range ws.Samples {
			points[i] = dataset.Point{Epoch: epoch.Epoch(s.Timestamp * nanosPerMillisecond), Values: []any{s.Value}}
		}
		slices.SortStableFunc(points, func(a, b dataset.Point) int { return cmpEpoch(a.Epoch, b.Epoch) })
		result.SeriesList = append(result.SeriesList, dataset.NewSeries(h, points))
	}
	return &dataset.DataSet{
		Results: dataset.Results{result}, TimeRangeQuery: trq,
		ExtentList: timeseries.ExtentList{trq.Extent},
	}, nil
}

func cmpEpoch(a, b epoch.Epoch) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func readResponse(n, labels, samples int) *prompb.ReadResponse {
	qr := &prompb.QueryResult{}
	for i := range n {
		ts := &prompb.TimeSeries{Labels: []*prompb.Label{{Name: "__name__", Value: "requests_total"}}}
		for j := range labels {
			ts.Labels = append(ts.Labels, &prompb.Label{
				Name:  fmt.Sprintf("label_%02d", j),
				Value: fmt.Sprintf("value-%d-%d", i, j),
			})
		}
		for j := range samples {
			t := int64(1_700_000_000_000 + 15_000*j)
			// some samples arrive out of time order
			if j%7 == 3 {
				t -= 45_000
			}
			ts.Samples = append(ts.Samples, &prompb.Sample{Timestamp: t, Value: float64(i*samples + j)})
		}
		qr.Timeseries = append(qr.Timeseries, ts)
	}
	return &prompb.ReadResponse{Results: []*prompb.QueryResult{qr}}
}

func TestUnmarshalMatchesGeneratedTypes(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{
		Statement: "query", Step: time.Millisecond,
		Extent: timeseries.Extent{Start: time.UnixMilli(0), End: time.UnixMilli(1)},
	}
	responses := map[string]*prompb.ReadResponse{
		"realistic":        readResponse(20, 8, 50),
		"no samples":       readResponse(3, 2, 0),
		"no labels":        readResponse(2, 0, 5),
		"empty series":     {Results: []*prompb.QueryResult{{Timeseries: []*prompb.TimeSeries{{}, {}}}}},
		"one empty result": {Results: []*prompb.QueryResult{{}}},
		"no name": {Results: []*prompb.QueryResult{{Timeseries: []*prompb.TimeSeries{{
			Labels:  []*prompb.Label{{Name: "job", Value: ""}, {Name: "", Value: "x"}},
			Samples: []*prompb.Sample{{Timestamp: -5, Value: -1}, {}},
		}}}}},
	}
	for name, response := range responses {
		t.Run(name, func(t *testing.T) {
			body := encodeReadResponse(t, response)
			want, err := unmarshalGenerated(body, trq)
			if err != nil {
				t.Fatal(err)
			}
			ts, err := UnmarshalTimeseries(body, trq)
			if err != nil {
				t.Fatal(err)
			}
			got := ts.(*dataset.DataSet)
			if len(got.Results) != 1 || len(got.Results[0].SeriesList) != len(want.Results[0].SeriesList) {
				t.Fatalf("results = %v, want %v", got.Results, want.Results)
			}
			if !reflect.DeepEqual(got.ExtentList, want.ExtentList) || got.TimeRangeQuery != trq {
				t.Errorf("extents = %v, want %v", got.ExtentList, want.ExtentList)
			}
			for i, ws := range want.Results[0].SeriesList {
				gs := got.Results[0].SeriesList[i]
				if !reflect.DeepEqual(gs.Header, ws.Header) {
					t.Errorf("series %d header = %#v, want %#v", i, gs.Header, ws.Header)
				}
				if gp, wp := dspoints.Of(gs), dspoints.Of(ws); !reflect.DeepEqual(gp, wp) {
					t.Errorf("series %d points = %v, want %v", i, gp, wp)
				}
			}
		})
	}
}

func appendMessage(dst []byte, num protowire.Number, msg []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(dst, num, protowire.BytesType), msg)
}

func TestUnmarshalTimeseriesMalformed(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{}
	series := func(fields ...[]byte) []byte {
		ts := slices.Concat(fields...)
		return appendMessage(nil, 1, appendMessage(nil, 1, ts))
	}
	label := func(name, value string) []byte {
		return appendMessage(nil, 1, appendMessage(appendMessage(nil, 1, []byte(name)), 2, []byte(value)))
	}
	tests := map[string]struct {
		message []byte
		want    error
	}{
		"truncated":           {message: []byte{0x0a, 0x05, 0x0a}, want: errInvalidResponse},
		"results as varint":   {message: protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), 1), want: errInvalidResponse},
		"labels as varint":    {message: series(protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), 1)), want: errInvalidResponse},
		"samples as varint":   {message: series(protowire.AppendVarint(protowire.AppendTag(nil, 2, protowire.VarintType), 1)), want: errInvalidResponse},
		"sample time fixed64": {message: series(appendMessage(nil, 2, protowire.AppendFixed64(protowire.AppendTag(nil, 2, protowire.Fixed64Type), 1))), want: errInvalidResponse},
		"label not utf-8":     {message: series(label("job", "\xff")), want: errInvalidLabel},
		"duplicate label":     {message: series(label("job", "a"), label("job", "b")), want: errDuplicateLabel},
		"second result":       {message: append(series(), series()...), want: errUnexpectedResults},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := UnmarshalTimeseries(snappy.Encode(nil, test.message), trq)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestUnmarshalTimeseriesChargesSeries(t *testing.T) {
	// series whose wire bytes fit the decode limit, but whose decoded memory would take more than it allows, fail
	// before they are kept
	const limit = 128
	trq := &timeseries.TimeRangeQuery{ParsedQuery: &parsedRequest{decodeLimit: limit}}
	empty := &prompb.QueryResult{}
	for range 30 {
		empty.Timeseries = append(empty.Timeseries, &prompb.TimeSeries{})
	}
	many := encodeReadResponse(t, &prompb.ReadResponse{Results: []*prompb.QueryResult{empty}})
	if n, _ := snappy.DecodedLen(many); n > limit {
		t.Fatalf("a response of %d bytes is over its decode limit", n)
	}
	if _, err := UnmarshalTimeseries(many, trq); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("error = %v, want %v", err, errBodyTooLarge)
	}
	// the same kind of series fits the allowance a larger limit gives
	one := encodeReadResponse(t, readResponse(1, 1, 1))
	if _, err := UnmarshalTimeseries(one, &timeseries.TimeRangeQuery{
		ParsedQuery: &parsedRequest{decodeLimit: 4 * limit},
	}); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkUnmarshalTimeseries(b *testing.B) {
	trq := &timeseries.TimeRangeQuery{Statement: "query"}
	encoded, err := proto.Marshal(readResponse(100, 10, 120))
	if err != nil {
		b.Fatal(err)
	}
	body := snappy.Encode(nil, encoded)
	b.Run("wire", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := UnmarshalTimeseries(body, trq); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("generated", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := unmarshalGenerated(body, trq); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestReadSnappyBlockErrors(t *testing.T) {
	failed := errors.New("read failed")
	if _, err := readSnappyBlock(iotest.ErrReader(failed), 0); !errors.Is(err, failed) {
		t.Fatalf("error = %v, want %v", err, failed)
	}
	// no limit is the default capture limit
	if b, err := decodeSnappyBlock(snappy.Encode(nil, []byte("abc")), 0); err != nil || string(b) != "abc" {
		t.Fatalf("decoded %q, %v", b, err)
	}
}
