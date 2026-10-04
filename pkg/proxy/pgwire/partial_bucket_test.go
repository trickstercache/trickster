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

package pgwire

import (
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func partialUpstream(t *testing.T) *fakeUpstream {
	return newFakeUpstream(t, func(f *fakeUpstream) { f.partialBuckets = true })
}

func TestPartialBucketsMatchTheOrigin(t *testing.T) {
	// 08:02 to 11:03 at a five-minute step: each mode's answer is the origin's over the range it serves
	upstream := partialUpstream(t)
	served := map[timeseries.StepAlignment][2]string{
		timeseries.StepAlignmentTruncate:     {"08:00", "11:00"},
		timeseries.StepAlignmentDrop:         {"08:05", "11:00"},
		timeseries.StepAlignmentPartial:      {"08:02", "11:03"},
		timeseries.StepAlignmentPartialStart: {"08:02", "11:00"},
		timeseries.StepAlignmentPartialEnd:   {"08:00", "11:03"},
	}
	for mode, bounds := range served {
		for _, selectList := range []string{cacheTestSelect, cacheTestHosts} {
			tail := "1 ORDER BY 1"
			if selectList == cacheTestHosts {
				tail = "1, 2 ORDER BY 1 DESC"
			}
			t.Run(mode.String()+"/"+tail, func(t *testing.T) {
				config := cachedConfig(t, upstream)
				config.StepAlignment, config.PartialBucketTTL = mode, 20*time.Second
				_, address := startServer(t, config)
				conn := mustDial(t, address, testClientUser, testClientPass)
				want := directRows(t, upstream, rangeQuery(selectList, bounds[0], bounds[1], tail))
				client := rangeQuery(selectList, "08:02", "11:03", tail)
				upstream.forget()
				if got := rowsOf(t, conn, client); !equalRows(got, want) {
					t.Fatalf("first answer differs from the origin's:\n%v\n%v", got, want)
				}
				// only a partial bucket's statement holds one of the client's own bounds
				partials := 0
				for _, sql := range upstream.received() {
					if strings.Contains(sql, cacheTestDay+"08:02") || strings.Contains(sql, cacheTestDay+"11:03") {
						partials++
					}
				}
				if want := partialCount(mode); partials != want || len(upstream.received()) != partials+1 {
					t.Fatalf("%d partial bucket fetches, want %d: %q", partials, want, upstream.received())
				}
				// the repeat comes from the delta tier and the object tier, and no partial count was cached
				upstream.forget()
				if got := rowsOf(t, conn, client); !equalRows(got, want) || len(upstream.received()) != 0 {
					t.Fatalf("repeat = %v, fetched %q", got, upstream.received())
				}
				for key, ttl := range config.Cache.(*byteCache).ttls {
					partialKey := strings.Contains(key, cacheKeySeparator+cacheKeyProtocol+cacheKeySeparator+cacheEnginePartial)
					if partialKey != (ttl == 20*time.Second) {
						t.Errorf("%s stored for %s", key, ttl)
					}
				}
				whole := rangeQuery(selectList, "08:00", "11:05", tail)
				if got, full := rowsOf(t, conn, whole), directRows(t, upstream, whole); !equalRows(got, full) {
					t.Fatalf("a whole range after partial buckets:\n%v\n%v", got, full)
				}
			})
		}
	}
}

func partialCount(mode timeseries.StepAlignment) int {
	start, end := mode.Edges()
	n := 0
	for _, edge := range []timeseries.EdgePolicy{start, end} {
		if edge == timeseries.EdgePartial {
			n++
		}
	}
	return n
}

func TestRangeWithoutACompleteBucketIsTheOriginsAnswer(t *testing.T) {
	upstream := partialUpstream(t)
	config := cachedConfig(t, upstream)
	config.PartialBucketTTL = 20 * time.Second
	_, address := startServer(t, config)
	conn := mustDial(t, address, testClientUser, testClientPass)
	client := rangeQuery(cacheTestSelect, "08:01", "08:04", "1 ORDER BY 1")
	want := directRows(t, upstream, client)
	upstream.forget()
	if got := rowsOf(t, conn, client); !equalRows(got, want) || len(want) != 2 {
		t.Fatalf("= %v, want the origin's %v", got, want)
	}
	if got := upstream.received(); len(got) != 1 || got[0] != client {
		t.Fatalf("expected the client's own statement, got %q", got)
	}
	if got := rowsOf(t, conn, client); !equalRows(got, want) || len(upstream.received()) != 1 {
		t.Fatalf("the repeat was not answered from the object tier: %v", got)
	}
	ttls := config.Cache.(*byteCache).storedTTLs()
	if len(ttls) != 1 || ttls[0] != 20*time.Second {
		t.Fatalf("stored for %v", ttls)
	}
}
