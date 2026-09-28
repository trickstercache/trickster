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

package cockroach

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
)

func TestParseCompactDuration(t *testing.T) {
	units := map[string]time.Duration{"ns": time.Nanosecond, "ms": time.Millisecond, "s": time.Second, "m": time.Minute, "h": time.Hour, "T": time.Millisecond}
	for input, want := range map[string]time.Duration{
		"1ns": time.Nanosecond, "15s": 15 * time.Second, "1h30m": 90 * time.Minute,
		"5T": 5 * time.Millisecond, "9223372036854775807ns": 1<<63 - 1,
	} {
		t.Run(input, func(t *testing.T) {
			if got, ok := ParseCompactDuration(input, units); !ok || got != want {
				t.Fatalf("got %s/%t, want %s", got, ok, want)
			}
		})
	}
	for _, input := range []string{"", "s", "1", "0s", "+1s", "-1s", "1.5s", "1 s", " 1s", "1s ", "1M", "1month", "1y", "1h0m", "9223372036854775808ns", "9223372036854775807s", "9223372036854775807ns1ns"} {
		t.Run(input, func(t *testing.T) {
			if _, ok := ParseCompactDuration(input, units); ok {
				t.Fatal("accepted invalid or overflowing width")
			}
		})
	}
	if _, ok := ParseCompactDuration("1s", map[string]time.Duration{"s": -1}); ok {
		t.Fatal("accepted a negative unit")
	}
	if allocs := testing.AllocsPerRun(100, func() { _, _ = ParseCompactDuration("1h30m", units) }); allocs != 0 {
		t.Fatalf("allocated %v", allocs)
	}
}

func TestCompactDateBinMatcher(t *testing.T) {
	a := NewAnalyzer(Options{BucketMatchers: []BucketMatcher{CompactDateBinMatcher(map[string]time.Duration{"m": time.Minute})}, RoundUnalignedTimeBounds: true})
	for _, bucket := range []string{"date_bin('5m', ts)", "date_bin('5m', ts, TIMESTAMP '1969-12-31T23:58:00Z')"} {
		sql := "SELECT " + bucket + " AS time, count(*) FROM t WHERE ts >= '2026-01-01T00:00:00Z' AND ts < '2026-01-02T00:00:00Z' GROUP BY 1"
		analysis := a.Analyze(sql, time.Now())
		if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan.Step != 5*time.Minute {
			t.Fatalf("%s: %+v", bucket, analysis)
		}
	}
	for _, bucket := range []string{"date_bin('0m', ts)", "date_bin('1M', ts)", "date_bin(width, ts)", "date_bin('5m')", "date_bin('5m', ts, now())", "date_bin('5m', ts + 1)", "date_bin(INTERVAL '5 minutes', ts)"} {
		analysis := a.Analyze("SELECT "+bucket+" AS time, count(*) FROM t WHERE ts >= '2026-01-01T00:00:00Z' AND ts < '2026-01-02T00:00:00Z' GROUP BY 1", time.Now())
		if analysis.Mode == sqlanalyzer.CacheModeDelta {
			t.Fatalf("accepted %s", bucket)
		}
	}
}

func TestIntervalDurationOverflow(t *testing.T) {
	for _, input := range []string{"9223372036854775807 seconds", "9223372036854775807 nanoseconds 1 nanosecond", "9223372036854775808 nanoseconds"} {
		if _, ok := ParseIntervalDuration(input); ok {
			t.Fatalf("accepted overflowing interval %s", input)
		}
	}
	if got, ok := ParseIntervalDuration("9223372036854775807 nanoseconds"); !ok || got != 1<<63-1 {
		t.Fatalf("rejected largest fixed interval: %s/%t", got, ok)
	}
}

func FuzzParseCompactDuration(f *testing.F) {
	for _, seed := range []string{"1ns", "5m", "1h30m", "500ms", "+1m", "1.5s", "0s", "1month", "9223372036854775807ns", "9223372036854775807ns1ns"} {
		f.Add(seed)
	}
	// The common fixed-length unit subset has an independent standard-library
	// oracle. This parser deliberately rejects some spellings that Go accepts.
	units := map[string]time.Duration{
		"ns": time.Nanosecond, "us": time.Microsecond, "ms": time.Millisecond,
		"s": time.Second, "m": time.Minute, "h": time.Hour,
	}
	f.Fuzz(func(t *testing.T, input string) {
		got, ok := ParseCompactDuration(input, units)
		if !ok {
			return
		}
		want, err := time.ParseDuration(input)
		if err != nil || got <= 0 || got != want {
			t.Fatalf("accepted %q as %v; standard parser: %v, %v", input, got, want, err)
		}
	})
}
