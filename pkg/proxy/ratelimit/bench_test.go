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

package ratelimit

import (
	"math"
	"runtime"
	"testing"
	"time"
)

func BenchmarkTakeHit(b *testing.B) {
	l := newTest(math.MaxUint32, time.Minute, 10)
	now := int64(time.Minute)
	l.Take(1, true, now, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		l.Take(1, true, now, 1)
	}
}

func BenchmarkTakeHit16(b *testing.B) {
	l := newTest(math.MaxUint32, time.Minute, 10)
	now := int64(time.Minute)
	l.Take(1, true, now, 1)
	// p*GOMAXPROCS is 16 when the process count divides 16, the contended budget.
	p := 16 / runtime.GOMAXPROCS(0)
	if p < 1 {
		p = 1
	}
	b.SetParallelism(p)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			l.Take(1, true, now, 1)
		}
	})
}
