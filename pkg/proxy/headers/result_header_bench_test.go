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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func benchResultParts() ResultHeaderParts {
	t0 := time.UnixMilli(1700000000000)
	return ResultHeaderParts{
		Engine: "DeltaProxyCache", Status: "phit", FastForwardStatus: "hit",
		Fetched: timeseries.ExtentList{{Start: t0, End: t0.Add(time.Hour)}},
		PartialBuckets: []PartialBucketResult{{
			Extent: timeseries.Extent{Start: t0.Add(time.Hour), End: t0.Add(time.Hour + time.Minute)},
			Edge:   timeseries.BucketEdgeEnd, Status: "hit",
		}},
	}
}

func BenchmarkResultHeaderString(b *testing.B) {
	p := benchResultParts()
	plain := ResultHeaderParts{Engine: p.Engine, Status: p.Status, Fetched: p.Fetched}
	b.Run("fetched", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = plain.String()
		}
	})
	b.Run("partial-bucket", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = p.String()
		}
	})
}

func BenchmarkParseResultHeader(b *testing.B) {
	v := benchResultParts().String()
	b.ReportAllocs()
	for b.Loop() {
		_ = ParseResultHeader(v)
	}
}

func BenchmarkMergeResultHeaderVals(b *testing.B) {
	v := benchResultParts().String()
	for _, members := range []int{2, 8} {
		b.Run(string(rune('0'+members))+"-members", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var s string
				for range members {
					s = MergeResultHeaderVals(s, v)
				}
			}
		})
	}
}

func BenchmarkResultHeaderMerger(b *testing.B) {
	v := benchResultParts().String()
	b.ReportAllocs()
	for b.Loop() {
		var m ResultHeaderMerger
		for range 8 {
			m.Add(v)
		}
		_ = m.String()
	}
}
