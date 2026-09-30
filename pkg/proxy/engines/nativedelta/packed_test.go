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

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

const packedTestHeader = "row description"

func packedDelta(series, points int) *Delta {
	// rows as the listeners model them: a series per host, each point a row's bytes and a sequence
	ds := &dataset.DataSet{
		ExtentList: timeseries.ExtentList{{Start: time.Unix(0, 0), End: time.Unix(int64(points-1)*60, 0)}},
		Results:    dataset.Results{{StatementID: 3, Name: "result", Error: "none"}},
	}
	for h := range series {
		s := &dataset.Series{Header: dataset.SeriesHeader{
			Name: "rows", Tags: dataset.Tags{"host": fmt.Sprintf("host-%d", h)},
		}}
		for i := range points {
			values := []any{fmt.Appendf(nil, "2026-09-10 08:%02d:00+00|host-%d|%d", i%60, h, i), int64(i*series + h)}
			if i%7 == 0 {
				values[0] = nil
			}
			if i%5 == 0 {
				values[1] = i
			}
			size := dataset.PointSize(values)
			s.Points = append(s.Points, dataset.Point{Epoch: epoch.Epoch(int64(i) * int64(time.Minute)), Size: size, Values: values})
			s.PointSize += int64(size)
		}
		ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, s)
	}
	return &Delta{Header: []byte(packedTestHeader), DS: ds}
}

func TestPackedRows(t *testing.T) {
	src := packedDelta(3, 30)
	data, err := deltaCodec{}.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	if layout := data[len(deltaCodecMagic)+1+4+len(packedTestHeader)]; layout != layoutPacked {
		t.Fatalf("layout %d, want packed", layout)
	}
	got, err := deltaCodec{}.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Header) != packedTestHeader || fmt.Sprint(got.DS.ExtentList) != fmt.Sprint(src.DS.ExtentList) {
		t.Fatalf("header %q, extents %v", got.Header, got.DS.ExtentList)
	}
	want, have := src.DS.Results[0], got.DS.Results[0]
	if have.StatementID != 3 || have.Name != "result" || have.Error != "none" || len(have.SeriesList) != 3 {
		t.Fatalf("result = %+v", have)
	}
	for i, s := range have.SeriesList {
		ws := want.SeriesList[i]
		if s.Header.Tags["host"] != ws.Header.Tags["host"] || len(s.Points) != len(ws.Points) {
			t.Fatalf("series %d = %+v", i, s.Header)
		}
		for j, p := range s.Points {
			wp := ws.Points[j]
			wantSeq, _ := dataset.IntValue(wp.Values[1])
			// decoded integers come back as the pointers AddInt appends, which box without allocating
			seq, ok := p.Values[1].(*int64)
			body, _ := dataset.BytesValue(p.Values[0])
			wantBody, _ := dataset.BytesValue(wp.Values[0])
			// decoded bytes come back as the arena pointers AddBytes appends, so they size as those do
			if p.Epoch != wp.Epoch || !bytes.Equal(body, wantBody) ||
				(wp.Values[0] == nil) != (p.Values[0] == nil) || !ok || *seq != wantSeq {
				t.Fatalf("point %d/%d = %+v, want %+v", i, j, p, wp)
			}
		}
	}

	t.Run("values that don't pack", func(t *testing.T) {
		for name, mutate := range map[string]func(*dataset.DataSet){
			"a float":        func(ds *dataset.DataSet) { ds.Results[0].SeriesList[0].Points[1].Values[1] = 1.5 },
			"uneven widths":  func(ds *dataset.DataSet) { ds.Results[0].SeriesList[1].Points[2].Values = []any{nil} },
			"a nil series":   func(ds *dataset.DataSet) { ds.Results[0].SeriesList[2] = nil },
			"a nil result":   func(ds *dataset.DataSet) { ds.Results = append(ds.Results, nil) },
			"a string value": func(ds *dataset.DataSet) { ds.Results[0].SeriesList[0].Points[0].Values[0] = "text" },
		} {
			d := packedDelta(3, 5)
			mutate(d.DS)
			if _, ok := packedRowsSize(d.DS); ok {
				t.Errorf("%s packed", name)
			}
		}
		// they keep the DataSet layout, which the codec still reads
		d := packedDelta(1, 3)
		d.DS.Results[0].SeriesList[0].Points[0].Values[0] = "text"
		data, err := deltaCodec{}.Marshal(d)
		if err != nil || data[len(deltaCodecMagic)+1+4+len(packedTestHeader)] != layoutDataSet {
			t.Fatalf("fallback layout: %v", err)
		}
		if got, err := (deltaCodec{}).Unmarshal(data); err != nil || got.Rows() != 3 {
			t.Fatalf("fallback round trip: %v", err)
		}
	})

	t.Run("corrupt rows", func(t *testing.T) {
		data, err := deltaCodec{}.Marshal(packedDelta(2, 4))
		if err != nil {
			t.Fatal(err)
		}
		// every truncation, and a value of an unknown kind, fails without a panic
		for n := len(deltaCodecMagic) + 1 + 4; n < len(data); n++ {
			if _, err := (deltaCodec{}).Unmarshal(data[:n]); err == nil {
				t.Fatalf("rows cut to %d of %d bytes decoded", n, len(data))
			}
		}
		huge := []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01}
		for _, garbage := range [][]byte{
			{9},
			{0, 1, 0, 1, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01},
			// counts no body could hold never size an allocation
			huge, append([]byte{0}, huge...),
		} {
			if _, err := readPackedRows(garbage); err == nil {
				t.Fatalf("%x decoded", garbage)
			}
		}
		if _, err := (deltaCodec{}).Unmarshal(append(bytes.Clone(data), 0)); err == nil {
			t.Fatal("trailing bytes decoded")
		}
	})
}

func BenchmarkDeltaCodec(b *testing.B) {
	for name, points := range map[string]int{"panel": 288, "large": 33334} {
		d := packedDelta(3, points)
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
