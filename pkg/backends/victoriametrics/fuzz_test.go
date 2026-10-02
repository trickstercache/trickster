/*
 * Copyright 2026 The Trickster Authors
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

package victoriametrics

import (
	"bytes"
	"net/url"
	"strconv"
	"testing"

	"github.com/VictoriaMetrics/metricsql"
)

// FuzzAbsoluteTime checks that every accepted timestamp round-trips through the format the
// adjusted range is written back in, so a rewritten request names the same instant.
func FuzzAbsoluteTime(f *testing.F) {
	for _, s := range []string{"1700000000", "1700000000.5", "2023-11-14T22:13:20Z", "-1h", "1e9", "0001"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		ms, err := absoluteTime(v)
		if err != nil {
			return
		}
		again, err := absoluteTime(formatMillis(ms))
		if err != nil || again != ms {
			t.Fatalf("%q -> %d -> %q -> %d, %v", v, ms, formatMillis(ms), again, err)
		}
	})
}

// FuzzAdjustRange checks VictoriaMetrics' range adjustment: a long range starts on a step boundary
// at or before its start and keeps its number of points; a short one is left as sent.
func FuzzAdjustRange(f *testing.F) {
	f.Add(int64(1700000017), int64(3600), int64(60))
	f.Add(int64(1700000000), int64(600), int64(15))
	f.Add(int64(1700000001), int64(86400), int64(7))
	c := &Client{}
	f.Fuzz(func(t *testing.T, start, span, step int64) {
		if start < 1e8 || start >= 1e10-1e7 || span < 0 || span > 1e7 || step <= 0 || step > 1e5 {
			return
		}
		v := url.Values{
			"start": {strconv.FormatInt(start, 10)}, "end": {strconv.FormatInt(start+span, 10)},
			"step": {strconv.FormatInt(step, 10)},
		}
		changed, ok := c.adjustRange(v)
		if !ok {
			t.Fatalf("a valid range was refused: %v", v)
		}
		s, _ := absoluteTime(v.Get("start"))
		e, _ := absoluteTime(v.Get("end"))
		stepMs := step * 1000
		points := span*1000/stepMs + 1
		if points < minPointsForAlignment {
			if changed || s != start*1000 {
				t.Fatalf("a short range was adjusted: %v", v)
			}
			return
		}
		if s%stepMs != 0 || s > start*1000 || start*1000-s >= stepMs || (e-s)/stepMs+1 != points || (e-s)%stepMs != 0 {
			t.Fatalf("start %d span %d step %d -> %d..%d", start, span, step, s, e)
		}
	})
}

// FuzzClassify checks that classification never panics, relays what doesn't parse, and never
// delta caches an expression holding a function that depends on the whole range.
func FuzzClassify(f *testing.F) {
	for _, s := range []string{
		"rate(m[5m])", "range_avg(m)", "now()", "sum(", "WITH (x = m) x",
		`topk_avg(1, m) by (a)`, "m @ start()", "sum(m) limit 2",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, q string) {
		a := classify(q)
		expr, err := metricsql.Parse(q)
		if err != nil {
			if a.mode != modeProxy || a.reason != reasonParse {
				t.Fatalf("%q: unparsable but %+v", q, a)
			}
			return
		}
		if a.mode != modeDelta {
			return
		}
		metricsql.VisitAll(expr, func(e metricsql.Expr) {
			switch e := e.(type) {
			case *metricsql.FuncExpr:
				if _, bad := rangeTransforms[e.Name]; bad {
					t.Fatalf("%q delta caches %s", q, e.Name)
				}
				if _, bad := volatileFuncs[e.Name]; bad {
					t.Fatalf("%q delta caches %s", q, e.Name)
				}
			case *metricsql.AggrFuncExpr:
				if _, bad := rangeAggrs[e.Name]; bad || e.Limit > 0 {
					t.Fatalf("%q delta caches %s", q, e.Name)
				}
			}
		})
	})
}

// FuzzIsPartialWriter checks that however a response is split into writes, isPartial is written
// once, only into a success envelope.
func FuzzIsPartialWriter(f *testing.F) {
	f.Add([]byte(`{"status":"success","data":{"result":[]}}`), uint8(3))
	f.Add([]byte(`{"status":"error","error":"x"}`), uint8(1))
	f.Fuzz(func(t *testing.T, body []byte, chunk uint8) {
		size := int(chunk%16) + 1
		var buf bytes.Buffer
		pw := newIsPartialWriter(&buf)
		for i := 0; i < len(body); i += size {
			if _, err := pw.Write(body[i:min(i+size, len(body))]); err != nil {
				t.Fatal(err)
			}
		}
		if err := pw.flush(); err != nil {
			t.Fatal(err)
		}
		want := body
		if bytes.HasPrefix(body, successPrefix) {
			want = append(append(append([]byte{}, successPrefix...), isPartialFalse...), body[len(successPrefix):]...)
		}
		if !bytes.Equal(buf.Bytes(), want) {
			t.Fatalf("got %q want %q", buf.Bytes(), want)
		}
	})
}
