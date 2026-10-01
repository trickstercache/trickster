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

package nativedelta

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

const rowsTestHeader = "row description"

// rowsDelta returns rows as the listeners model them: a series per host, each point a row's bytes and
// a sequence number
func rowsDelta(series, points int) *Delta {
	ds := &dataset.DataSet{
		ExtentList: timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(int64(points-1)*60, 0)}},
		Results:    dataset.Results{{StatementID: 3, Name: "result", Error: "none"}},
	}
	for h := range series {
		pts := make(dataset.Points, points)
		for i := range points {
			values := []any{fmt.Appendf(nil, "2026-09-10 08:%02d:00+00|host-%d|%d", i%60, h, i), int64(i*series + h)}
			if i%7 == 0 {
				values[0] = nil
			}
			pts[i] = dataset.Point{Epoch: epoch.Epoch(int64(i) * int64(time.Minute)), Values: values}
		}
		ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, dataset.NewSeries(dataset.SeriesHeader{
			Name: "rows", Tags: dataset.Tags{"host": fmt.Sprintf("host-%d", h)},
		}, pts))
	}
	return &Delta{Header: []byte(rowsTestHeader), DS: ds}
}

func TestDeltaCodecRoundTrip(t *testing.T) {
	for name, d := range map[string]*Delta{
		"rows": rowsDelta(3, 30),
		// values of any kind hold as rows do
		"typed": {Header: []byte("h"), DS: &dataset.DataSet{Results: dataset.Results{{SeriesList: dataset.SeriesList{
			dataset.NewSeries(dataset.SeriesHeader{Name: "t"}, dataset.Points{
				{Epoch: 1, Values: []any{1.5, "text", true}},
				{Epoch: 2, Values: []any{nil, "more", false}},
			}),
		}}}}},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := deltaCodec{}.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			got, err := deltaCodec{}.Unmarshal(data)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Header, d.Header) || fmt.Sprint(got.DS.ExtentList) != fmt.Sprint(d.DS.ExtentList) {
				t.Fatalf("header %q, extents %v", got.Header, got.DS.ExtentList)
			}
			want, have := d.DS.Results[0], got.DS.Results[0]
			if have.StatementID != want.StatementID || have.Name != want.Name || have.Error != want.Error ||
				len(have.SeriesList) != len(want.SeriesList) {
				t.Fatalf("result = %+v", have)
			}
			for i, s := range have.SeriesList {
				ws := want.SeriesList[i]
				if s.Header.Tags["host"] != ws.Header.Tags["host"] || !dspoints.Of(s).Equal(dspoints.Of(ws)) {
					t.Fatalf("series %d = %v, want %v", i, dspoints.Of(s), dspoints.Of(ws))
				}
			}
			if got.Rows() != d.Rows() || (deltaCodec{}).Size(got) <= 0 {
				t.Fatalf("rows %d, size %d", got.Rows(), (deltaCodec{}).Size(got))
			}
		})
	}
}

func TestDeltaCodecCorruption(t *testing.T) {
	data, err := deltaCodec{}.Marshal(rowsDelta(2, 4))
	if err != nil {
		t.Fatal(err)
	}
	// every truncation fails without a panic
	for n := range len(data) {
		if _, err := (deltaCodec{}).Unmarshal(data[:n]); err == nil {
			t.Fatalf("rows cut to %d of %d bytes decoded", n, len(data))
		}
	}
	if _, err := (deltaCodec{}).Unmarshal(append(bytes.Clone(data), 0)); err == nil {
		t.Fatal("trailing bytes decoded")
	}
	old := bytes.Clone(data)
	old[len(deltaCodecMagic)] = 1
	if _, err := (deltaCodec{}).Unmarshal(old); err == nil {
		t.Fatal("an entry of another version decoded")
	}
	if _, err := (deltaCodec{}).Marshal(nil); err == nil {
		t.Fatal("a nil delta encoded")
	}
}

func BenchmarkDeltaCodec(b *testing.B) {
	for name, points := range map[string]int{"panel": 288, "large": 33334} {
		d := rowsDelta(3, points)
		data, err := deltaCodec{}.Marshal(d)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(name+"/marshal", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := (deltaCodec{}).Marshal(d); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/unmarshal", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if _, err := (deltaCodec{}).Unmarshal(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
