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

package stream_test

import (
	"cmp"
	"encoding/json"
	"io"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream/streamtest"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

func wantSeries(name string, tags dataset.Tags, fields timeseries.SeriesFields,
	points ...dataset.Point,
) *dataset.Series {
	return &dataset.Series{
		Header: dataset.SeriesHeader{
			Name:            name,
			Tags:            tags,
			TimestampField:  fields.Timestamp,
			TagFieldsList:   fields.Tags,
			ValueFieldsList: fields.Values,
		},
		Points: points,
	}
}

func wantDataSet(series ...*dataset.Series) *dataset.DataSet {
	return &dataset.DataSet{
		ExtentList: timeseries.ExtentList{testTRQ.Extent},
		Results:    dataset.Results{{SeriesList: series}},
	}
}

func pt(ms int64, v any) dataset.Point {
	return dataset.Point{Epoch: epoch.Epoch(ms * 1e6), Values: []any{v}}
}

func TestTSVConformance(t *testing.T) {
	body := "time\thost\tvalue\r\n2000\ta\t3\n1000\ta\t1.5\n1000\tb\t2\n\n"
	want := wantDataSet(
		wantSeries("tsv", dataset.Tags{"host": "a"}, rowFields, pt(1000, 1.5), pt(2000, 3.0)),
		wantSeries("tsv", dataset.Tags{"host": "b"}, rowFields, pt(1000, 2.0)),
	)
	for _, s := range want.Results[0].SeriesList {
		s.Header.QueryStatement = "q"
	}
	// the blank line is a row with the wrong column count
	streamtest.Conformance(t, newTSVDecoder, streamtest.Case{
		TRQ: testTRQ, Body: []byte(body), WantErr: timeseries.ErrInvalidBody,
	})
	streamtest.Conformance(t, newTSVDecoder, streamtest.Case{
		TRQ: testTRQ, Body: []byte(strings.TrimSuffix(body, "\n\n")), Want: want,
		Shuffle: streamtest.ShuffleLines(1),
	})
}

func TestTSVConformanceErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		err  error
	}{
		{"empty", "", errBadHeader},
		{"header", "time\tvalue\n1\t1\n", errBadHeader},
		{"columns", "time\thost\tvalue\n1\ta\n", timeseries.ErrInvalidBody},
		{"epoch", "time\thost\tvalue\nx\ta\t1\n", timeseries.ErrInvalidTimeFormat},
		{"value", "time\thost\tvalue\n1\ta\tx\n", stream.ErrInvalidValue},
		{"duplicate", "time\thost\tvalue\n1\ta\t1\n1\ta\t2\n", dataset.ErrDuplicateEpoch},
		{"unordered duplicate", "time\thost\tvalue\n2\ta\t1\n1\ta\t1\n2\ta\t2\n", dataset.ErrDuplicateEpoch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			streamtest.Conformance(t, newTSVDecoder, streamtest.Case{
				TRQ: testTRQ, Body: []byte(test.body), WantErr: test.err,
			})
		})
	}
}

func TestMatrixConformance(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"job":"a"},"values":[[1,"1"],[2.5,"2.5"]]},
		{"metric":{"job":"b"},"values":[[2,"NaN"],[1,"+Inf"]],"extra":[{}]}]},"warnings":[]}
	`
	fields := timeseries.SeriesFields{Values: timeseries.FieldDefinitions{
		{Name: "value", DataType: timeseries.Float64, Role: timeseries.RoleValue}}}
	want := wantDataSet(
		wantSeries("matrix", dataset.Tags{"job": "a"}, fields, pt(1000, 1.0), pt(2500, 2.5)),
		wantSeries("matrix", dataset.Tags{"job": "b"}, fields, pt(1000, math.Inf(1)), pt(2000, math.NaN())),
	)
	want.Status = "success"
	streamtest.Conformance(t, newMatrixDecoder, streamtest.Case{TRQ: testTRQ, Body: []byte(body), Want: want})
}

func TestMatrixConformanceErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		err  error
	}{
		{"empty", "", streamtest.ErrAny},
		{"truncated", `{"data":{"result":[{"metric":{}`, streamtest.ErrAny},
		{"null result", `{"data":{"result":null}}`, stream.ErrNull},
		{"not an object", `[]`, stream.ErrUnexpectedToken},
		{"trailing", `{} {}`, stream.ErrTrailingData},
		{"trailing garbage", `{} x`, streamtest.ErrAny},
		{"values first", `{"data":{"result":[{"values":[],"metric":{}}]}}`, errMetricFirst},
		{"bad pair", `{"data":{"result":[{"metric":{},"values":[[1]]}]}}`, timeseries.ErrInvalidBody},
		{"bad time", `{"data":{"result":[{"metric":{},"values":[["x","1"]]}]}}`, timeseries.ErrInvalidTimeFormat},
		{"bad value", `{"data":{"result":[{"metric":{},"values":[[1,"x"]]}]}}`, stream.ErrInvalidValue},
		{"duplicate", `{"data":{"result":[{"metric":{},"values":[[1,"1"],[1,"2"]]}]}}`, dataset.ErrDuplicateEpoch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			streamtest.Conformance(t, newMatrixDecoder, streamtest.Case{
				TRQ: testTRQ, Body: []byte(test.body), WantErr: test.err,
			})
		})
	}
}

func shuffleRows(body []byte, rng *weaktest.Rand) []byte {
	var doc map[string]json.RawMessage
	var rows []json.RawMessage
	if json.Unmarshal(body, &doc) != nil || json.Unmarshal(doc["rows"], &rows) != nil {
		return body
	}
	rng.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
	doc["rows"], _ = json.Marshal(rows)
	out, _ := json.Marshal(doc)
	return out
}

func legacyRows(r io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	// decodes the whole document before building its DataSet, like pre-stream unmarshalers
	var doc struct {
		Rows [][]any `json:"rows"`
	}
	dec := json.NewDecoder(r)
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	byHost := map[string]*dataset.Series{}
	for _, row := range doc.Rows {
		host := row[1].(string)
		s, ok := byHost[host]
		if !ok {
			s = wantSeries("rows", dataset.Tags{"host": host}, rowFields)
			byHost[host] = s
		}
		ms, _ := row[0].(json.Number).Int64()
		var v any
		if n, ok := row[2].(json.Number); ok {
			v, _ = n.Float64()
		} else if str, ok := row[2].(string); ok {
			v, _ = json.Number(str).Float64()
		}
		s.Points = append(s.Points, pt(ms, v))
	}
	sl := make(dataset.SeriesList, 0, len(byHost))
	for _, host := range slices.Sorted(maps.Keys(byHost)) {
		s := byHost[host]
		slices.SortStableFunc(s.Points, func(a, b dataset.Point) int { return cmp.Compare(a.Epoch, b.Epoch) })
		sl = append(sl, s)
	}
	ds := wantDataSet(sl...)
	ds.TimeRangeQuery = trq
	return ds, nil
}

func TestRowsConformance(t *testing.T) {
	// "\u0061" spells the same tag value as "a", so both rows belong to one series
	body := `{"rows":[[2000,"b",1],[1000,"a",null],[1000,"b","2.5"],[3000,"c\"d",-4e-1],[4000,"\u0061",5]],
		"total":5,"extra":{"x":[1,2,{"y":null}]}}`
	want := wantDataSet(
		wantSeries("rows", dataset.Tags{"host": "a"}, rowFields, pt(1000, nil), pt(4000, 5.0)),
		wantSeries("rows", dataset.Tags{"host": "b"}, rowFields, pt(1000, 2.5), pt(2000, 1.0)),
		wantSeries("rows", dataset.Tags{"host": `c"d`}, rowFields, pt(3000, -0.4)),
	)
	streamtest.Conformance(t, newRowsDecoder, streamtest.Case{
		TRQ: testTRQ, Body: []byte(body), Want: want, Shuffle: shuffleRows, Legacy: legacyRows,
	})
	streamtest.Conformance(t, newRowsDecoder, streamtest.Case{
		TRQ: testTRQ, Body: []byte(`{"rows":[[1,"a",1]],"total":2}`), WantErr: timeseries.ErrInvalidBody,
	})
}

func TestUnmarshalerAdapters(t *testing.T) {
	u := stream.ReaderUnmarshaler(newTSVDecoder)
	_, err := u(nil, testTRQ)
	require.ErrorIs(t, err, timeseries.ErrInvalidBody)

	ts, err := stream.BytesUnmarshaler(newTSVDecoder)([]byte("time\thost\tvalue\n1\ta\t1\n"), testTRQ)
	require.NoError(t, err)
	require.Equal(t, int64(1), ts.ValueCount())

	errNew := io.ErrClosedPipe
	failing := func(*timeseries.TimeRangeQuery) (stream.Decoder, error) { return nil, errNew }
	_, err = stream.BytesUnmarshaler(failing)(nil, testTRQ)
	require.ErrorIs(t, err, errNew)

	nilResult := func(*timeseries.TimeRangeQuery) (stream.Decoder, error) {
		return stream.NewLines(func([]byte) error { return nil },
			func() (timeseries.Timeseries, error) { return nil, nil }), nil
	}
	_, err = stream.BytesUnmarshaler(nilResult)([]byte("x"), testTRQ)
	require.ErrorIs(t, err, timeseries.ErrInvalidBody)
}

func BenchmarkRowsDecoders(b *testing.B) {
	var sb strings.Builder
	sb.WriteString(`{"rows":[`)
	const rows = 5000
	for i := range rows {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`[`)
		sb.WriteString(strings.Repeat("1", 1+i%3))
		sb.WriteString(`000,"host-`)
		sb.WriteByte(byte('a' + i%8))
		sb.WriteString(`",1.5]`)
	}
	sb.WriteString(`],"total":5000}`)
	body := []byte(sb.String())
	b.Run("legacy", func(b *testing.B) {
		streamtest.Bench(b, legacyRows, testTRQ, body)
	})
	b.Run("stream", func(b *testing.B) {
		streamtest.Bench(b, stream.ReaderUnmarshaler(newRowsDecoder), testTRQ, body)
	})
}
