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
	"errors"
	"slices"
	"testing"
	"time"

	trickstercache "github.com/trickstercache/trickster/v2/pkg/cache"
	cacheproviders "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const keepTestKey = "keep"

// referenceCache is a memory cache, holding entries by reference
type referenceCache struct {
	*testCache
	refs map[string]any
}

func newReferenceCache() *referenceCache {
	c := &referenceCache{testCache: newTestCache(), refs: make(map[string]any)}
	c.provider = cacheproviders.Memory
	return c
}

func (c *referenceCache) StoreReference(key string, data trickstercache.ReferenceObject, _ time.Duration) error {
	if c.storeErr != nil {
		return c.storeErr
	}
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.refs[key] = data
	return nil
}

func (c *referenceCache) Remove(keys ...string) error {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	for _, key := range keys {
		delete(c.refs, key)
	}
	return nil
}

func (c *referenceCache) RetrieveReference(key string) (any, status.LookupStatus, error) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	v, ok := c.refs[key]
	if !ok {
		return nil, status.LookupStatusKeyMiss, trickstercache.ErrKNF
	}
	return v, status.LookupStatusHit, nil
}

// keepTestEngine returns an engine over c, of codec and limit, adding the failures it observes to failures
func keepTestEngine(c trickstercache.Cache, codec Codec[*payload], limit int64, failures *[]string) *Engine[*payload] {
	return New(Config{
		Protocol: "test", BackendName: "test-backend", CacheTTL: time.Minute, MaxObjectSize: limit,
		CacheClient:         func() trickstercache.Cache { return c },
		ObserveCacheFailure: func(reason string) { *failures = append(*failures, reason) },
	}, codec)
}

func keepTestEntry() *Entry[*payload] {
	return &Entry[*payload]{
		Payload: &payload{Statements: []string{"range(60,120)", "range(180,240)"}},
		Extents: timeseries.ExtentList{{Start: time.Unix(60, 0), End: time.Unix(240, 0)}},
	}
}

func TestKeepReportsWhyAnEntryIsNotStored(t *testing.T) {
	for name, newCache := range map[string]func() (trickstercache.Cache, *testCache){
		"bytes":  func() (trickstercache.Cache, *testCache) { c := newTestCache(); return c, c },
		"memory": func() (trickstercache.Cache, *testCache) { c := newReferenceCache(); return c, c.testCache },
	} {
		t.Run(name, func(t *testing.T) {
			var failures []string
			c, _ := newCache()
			unlimited := keepTestEngine(c, testCodec{}, 0, &failures)
			if err := unlimited.Keep(keepTestKey, keepTestEntry()); err != nil {
				t.Fatalf("an entry without a limit wasn't kept: %v", err)
			}
			kept, ok := unlimited.Retrieve(keepTestKey)
			if !ok || kept.Size() <= 0 {
				t.Fatalf("the kept entry = %+v, %v", kept, ok)
			}
			size := int64(kept.Size())
			for limit, want := range map[int64]error{size: nil, size - 1: ErrObjectTooLarge} {
				failures = nil
				c, _ := newCache()
				engine := keepTestEngine(c, testCodec{}, limit, &failures)
				if err := engine.Keep(keepTestKey, keepTestEntry()); !errors.Is(err, want) {
					t.Errorf("keeping an entry of %d bytes under a limit of %d = %v, want %v", size, limit, err, want)
				}
				if _, ok := engine.Retrieve(keepTestKey); ok != (want == nil) {
					t.Errorf("under a limit of %d, the entry is stored: %v", limit, ok)
				}
				var wantFailures []string
				if want != nil {
					wantFailures = []string{failureTooLarge}
				}
				if !slices.Equal(failures, wantFailures) {
					t.Errorf("under a limit of %d, observed %v", limit, failures)
				}
			}
			// an entry already stored past a reader's limit is refused and removed as it's read
			failures = nil
			if _, ok := keepTestEngine(c, testCodec{}, size-1, &failures).Retrieve(keepTestKey); ok {
				t.Error("an oversized entry was read")
			}
			if _, ok := unlimited.Retrieve(keepTestKey); ok || !slices.Equal(failures, []string{failureOversized}) {
				t.Errorf("an oversized entry read is removed: %v, observed %v", !ok, failures)
			}
			failures = nil
			c, tc := newCache()
			tc.storeErr = errors.New("store failed")
			if err := keepTestEngine(c, testCodec{}, 0, &failures).Keep(keepTestKey, keepTestEntry()); !errors.Is(err, tc.storeErr) {
				t.Errorf("a failed store = %v", err)
			}
			if !slices.Equal(failures, []string{failureStore}) {
				t.Errorf("a failed store observed %v", failures)
			}
		})
	}
	t.Run("encoding", func(t *testing.T) {
		var failures []string
		boom := errors.New("boom")
		engine := keepTestEngine(newTestCache(), testCodec{marshalErr: boom}, 0, &failures)
		if err := engine.Keep(keepTestKey, keepTestEntry()); !errors.Is(err, boom) {
			t.Errorf("an entry that failed to encode = %v", err)
		}
		if err := engine.Keep(keepTestKey, nil); !errors.Is(err, errNilEntry) {
			t.Errorf("a nil entry = %v", err)
		}
		if !slices.Equal(failures, []string{failureEncode, failureEncode}) {
			t.Errorf("encoding failures observed %v", failures)
		}
	})
	t.Run("no cache", func(t *testing.T) {
		engine := New(Config{Protocol: "test", CacheTTL: time.Minute}, testCodec{})
		if err := engine.Keep(keepTestKey, keepTestEntry()); !errors.Is(err, errNoCacheClient) {
			t.Errorf("an entry with no cache = %v", err)
		}
	})
}
