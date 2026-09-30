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

package dataset

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/tinylib/msgp/msgp"
)

func TestMarshalUnmarshalPoints(t *testing.T) {
	v := Points{}
	bts, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	left, err := v.UnmarshalMsg(bts)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) > 0 {
		t.Errorf("%d bytes left over after UnmarshalMsg(): %q", len(left), left)
	}

	left, err = msgp.Skip(bts)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) > 0 {
		t.Errorf("%d bytes left over after Skip(): %q", len(left), left)
	}
}

func BenchmarkMarshalMsgPoints(b *testing.B) {
	v := Points{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v.MarshalMsg(nil)
	}
}

func BenchmarkAppendMsgPoints(b *testing.B) {
	v := Points{}
	bts := make([]byte, 0, v.Msgsize())
	bts, _ = v.MarshalMsg(bts[0:0])
	b.SetBytes(int64(len(bts)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bts, _ = v.MarshalMsg(bts[0:0])
	}
}

func BenchmarkUnmarshalPoints(b *testing.B) {
	v := Points{}
	bts, _ := v.MarshalMsg(nil)
	b.ReportAllocs()
	b.SetBytes(int64(len(bts)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := v.UnmarshalMsg(bts)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestEncodeDecodePoints(t *testing.T) {
	v := Points{}
	var buf bytes.Buffer
	msgp.Encode(&buf, &v)

	m := v.Msgsize()
	if buf.Len() > m {
		t.Log("WARNING: TestEncodeDecodePoints Msgsize() is inaccurate")
	}

	vn := Points{}
	err := msgp.Decode(&buf, &vn)
	if err != nil {
		t.Error(err)
	}

	buf.Reset()
	msgp.Encode(&buf, &v)
	err = msgp.NewReader(&buf).Skip()
	if err != nil {
		t.Error(err)
	}
}

func BenchmarkEncodePoints(b *testing.B) {
	v := Points{}
	var buf bytes.Buffer
	msgp.Encode(&buf, &v)
	b.SetBytes(int64(buf.Len()))
	en := msgp.NewWriter(msgp.Nowhere)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v.EncodeMsg(en)
	}
	en.Flush()
}

func BenchmarkDecodePoints(b *testing.B) {
	v := Points{}
	var buf bytes.Buffer
	msgp.Encode(&buf, &v)
	b.SetBytes(int64(buf.Len()))
	rd := msgp.NewEndlessReader(buf.Bytes(), b)
	dc := msgp.NewReader(rd)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := v.DecodeMsg(dc)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestPointsUnmarshalMatchesPointByPoint(t *testing.T) {
	rng := weaktest.NewRand(10, 3)
	for trial := range 50 {
		pts := make(Points, rng.IntN(3000))
		for i := range pts {
			pts[i] = Point{Epoch: epoch.Epoch(i), Size: i % 7}
			switch w := rng.IntN(5); w {
			case 0:
			case 1:
				pts[i].Values = []any{}
			default:
				for j := range w - 1 {
					pts[i].Values = append(pts[i].Values, []any{float64(j) / 3, "s", int64(-j), nil, true}[rng.IntN(5)])
				}
			}
		}
		b, err := pts.MarshalMsg(nil)
		if err != nil {
			t.Fatal(err)
		}
		var got Points
		if rest, err := got.UnmarshalMsg(b); err != nil || len(rest) != 0 {
			t.Fatalf("trial %d: %v, %d bytes left", trial, err, len(rest))
		}
		// the generated decode of each point is the reference
		want := make(Points, len(pts))
		_, rest, err := msgp.ReadArrayHeaderBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		for i := range want {
			if rest, err = want[i].UnmarshalMsg(rest); err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("trial %d: decoded points differ from the generated decode", trial)
		}
	}
}

func TestPointsUnmarshalKeepsValuesApart(t *testing.T) {
	pts := Points{{Epoch: 1, Values: []any{1.0}}, {Epoch: 2, Values: []any{2.0}}}
	b, _ := pts.MarshalMsg(nil)
	var got Points
	if _, err := got.UnmarshalMsg(b); err != nil {
		t.Fatal(err)
	}
	// the values share a chunk, capped, so an append can't reach the next point's
	got[0].Values = append(got[0].Values, "x")
	if got[1].Values[0] != 2.0 || cap(got[1].Values) != 1 {
		t.Fatalf("the next point's values changed: %v", got[1].Values)
	}
}

func TestPointsUnmarshalRejectsAnOversizedValuesHeader(t *testing.T) {
	b := msgp.AppendArrayHeader(nil, 1)
	b = msgp.AppendMapHeader(b, 1)
	b = msgp.AppendString(b, "values")
	b = msgp.AppendArrayHeader(b, 1<<31)
	var got Points
	if _, err := got.UnmarshalMsg(b); err == nil {
		t.Fatal("a values header longer than the bytes decoded")
	}
}
