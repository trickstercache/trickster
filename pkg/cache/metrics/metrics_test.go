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

package metrics

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

var testCacheKey, testCacheName, testCacheProvider string

func init() {
	testCacheKey = "test-key"
	testCacheName = "test-cache"
	testCacheProvider = "test"
}

func sampleCount(t *testing.T, o prometheus.Observer) uint64 {
	t.Helper()
	var m dto.Metric
	if err := o.(prometheus.Metric).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount()
}

func assertObserved(t *testing.T, operation, opStatus string, wantBytes float64, fn func()) {
	t.Helper()
	count := metrics.CacheObjectOperations.WithLabelValues(testCacheName, testCacheProvider, operation, opStatus)
	duration := metrics.CacheObjectOperationDuration.WithLabelValues(testCacheName, testCacheProvider, operation, opStatus)
	bytes := metrics.CacheByteOperations.WithLabelValues(testCacheName, testCacheProvider, operation, opStatus)
	countBefore, samplesBefore, bytesBefore := testutil.ToFloat64(count), sampleCount(t, duration), testutil.ToFloat64(bytes)
	fn()
	if got := testutil.ToFloat64(count) - countBefore; got != 1 {
		t.Errorf("expected 1 %s/%s operation, got %v", operation, opStatus, got)
	}
	if got := sampleCount(t, duration) - samplesBefore; got != 1 {
		t.Errorf("expected 1 %s/%s duration sample, got %v", operation, opStatus, got)
	}
	if got := testutil.ToFloat64(bytes) - bytesBefore; got != wantBytes {
		t.Errorf("expected %v %s/%s bytes, got %v", wantBytes, operation, opStatus, got)
	}
}

func TestObserveCacheMiss(t *testing.T) {
	assertObserved(t, KeyGet, status.StatusKeyMiss, 0, func() {
		ObserveCacheMiss(testCacheName, testCacheProvider, time.Millisecond)
	})
}

func TestObserveCacheDel(t *testing.T) {
	assertObserved(t, KeyDel, KeyNone, 5, func() {
		ObserveCacheDel(testCacheName, testCacheProvider, 5, time.Millisecond)
	})
}

func TestObserveCacheDelBytes(t *testing.T) {
	count := metrics.CacheObjectOperations.WithLabelValues(testCacheName, testCacheProvider, KeyDel, KeyNone)
	duration := metrics.CacheObjectOperationDuration.WithLabelValues(testCacheName, testCacheProvider, KeyDel, KeyNone)
	bytes := metrics.CacheByteOperations.WithLabelValues(testCacheName, testCacheProvider, KeyDel, KeyNone)
	countBefore, samplesBefore, bytesBefore := testutil.ToFloat64(count), sampleCount(t, duration), testutil.ToFloat64(bytes)
	ObserveCacheDelBytes(testCacheName, testCacheProvider, 0)
	ObserveCacheDelBytes(testCacheName, testCacheProvider, 5)
	if got := testutil.ToFloat64(bytes) - bytesBefore; got != 5 {
		t.Errorf("expected 5 del bytes, got %v", got)
	}
	if testutil.ToFloat64(count) != countBefore || sampleCount(t, duration) != samplesBefore {
		t.Error("expected no operation count or duration sample for a bytes-only observation")
	}
}

func TestCacheError(t *testing.T) {
	_, err := CacheError(testCacheKey, testCacheName, testCacheProvider, "%s")
	if err.Error() != testCacheKey {
		t.Errorf("expected %s got %s", testCacheKey, err.Error())
	}
}

func TestObserveCacheOperation(t *testing.T) {
	assertObserved(t, KeySet, "ok", 0, func() {
		ObserveCacheOperation(testCacheName, testCacheProvider, KeySet, "ok", 0, time.Millisecond)
	})
	assertObserved(t, KeySet, "ok", 1, func() {
		ObserveCacheOperation(testCacheName, testCacheProvider, KeySet, "ok", 1, time.Millisecond)
	})
}

func TestObserveCacheEvent(t *testing.T) {
	ObserveCacheEvent(testCacheName, testCacheProvider, "test", "test")
}

func TestObserveCacheSizeChange(t *testing.T) {
	ObserveCacheSizeChange(testCacheName, testCacheProvider, 0, 0)
}
