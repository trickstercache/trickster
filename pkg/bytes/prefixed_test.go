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

package bytes

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestLengthPrefixed(t *testing.T) {
	// parts split differently never write the same bytes
	parts := [][]string{{"ab", "c"}, {"a", "bc"}, {"abc"}, {"abc", ""}, {"", "abc"}, {strings.Repeat("x", 200)}}
	seen := make(map[string][]string, len(parts))
	for _, p := range parts {
		var b, buf []byte
		h := sha256.New()
		for _, s := range p {
			b = AppendLengthPrefixed(b, s)
			buf = WriteLengthPrefixed(h, buf, s)
		}
		if prev, ok := seen[string(b)]; ok {
			t.Fatalf("%q and %q write the same bytes", prev, p)
		}
		seen[string(b)] = p
		if want := sha256.Sum256(b); string(h.Sum(nil)) != string(want[:]) {
			t.Fatalf("%q: WriteLengthPrefixed hashes other bytes than AppendLengthPrefixed appends", p)
		}
	}
	if got := AppendLengthPrefixed([]byte("x"), []byte("yz")); string(got) != "x\x02yz" {
		t.Fatalf("got %q", got)
	}
	// a length past 127 takes two bytes
	if got := AppendLengthPrefixed(nil, strings.Repeat("x", 200)); string(got[:2]) != "\xc8\x01" || len(got) != 202 {
		t.Fatalf("got %q", got[:2])
	}
}

func TestWriteLengthPrefixedReusesItsBuffer(t *testing.T) {
	h := sha256.New()
	buf := WriteLengthPrefixed(h, nil, strings.Repeat("x", 64))
	parts := []string{"a", strings.Repeat("y", 32), "z"}
	if allocs := testing.AllocsPerRun(100, func() {
		for _, s := range parts {
			buf = WriteLengthPrefixed(h, buf, s)
		}
	}); allocs != 0 {
		t.Fatalf("got %v allocations", allocs)
	}
}
