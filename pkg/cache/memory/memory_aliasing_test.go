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

package memory

import (
	"bytes"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
)

func TestMemoryCacheStoreDefensivelyCopies(t *testing.T) {
	cfg := newCacheConfig()
	mc := New(t.Name(), &cfg)
	t.Cleanup(func() { _ = mc.Close() })

	buf := []byte("original")
	want := append([]byte(nil), buf...)

	if err := mc.Store("k1", buf, time.Minute); err != nil {
		t.Fatalf("Store: %v", err)
	}

	copy(buf, "MODIFIED")

	got, s, err := mc.Retrieve("k1")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if s != status.LookupStatusHit {
		t.Fatalf("status = %v, want hit", s)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("cached bytes mutated by caller: got %q, want %q", got, want)
	}
}
