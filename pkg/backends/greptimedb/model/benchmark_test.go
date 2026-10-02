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

package model

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func BenchmarkHTTPDecode(b *testing.B) {
	const count = 10000
	for _, series := range []int{10, 1000, count} {
		b.Run(fmt.Sprintf("series=%d", series), func(b *testing.B) {
			var rows strings.Builder
			rows.WriteByte('[')
			for i := range count {
				if i != 0 {
					rows.WriteByte(',')
				}
				fmt.Fprintf(&rows, `[9007199254740993,%d,"host-%d"]`, int64(i)*int64(time.Second), i%series)
			}
			rows.WriteByte(']')
			body := []byte(envelope(rows.String(), count))
			trq := query()
			trq.Extent.End = time.Unix(count, 0)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				ts, err := UnmarshalTimeseriesReader(bytes.NewReader(body), trq)
				if err != nil || ts.ValueCount() != count {
					b.Fatalf("decode failed: %v", err)
				}
			}
		})
	}
}
