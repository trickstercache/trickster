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

package engines

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// varyFieldNames returns the request fields a response nominates for
// selection. Names are canonicalized, deduplicated and sorted, so neither the
// order they were listed in nor their spelling changes the key they produce.
// Every Vary field line contributes, per RFC 9110 5.3.
//
// The second return is false for Vary: *, which nominates something the cache
// cannot compare; RFC 9111 4.1 never allows such a response to be reused.
func varyFieldNames(h http.Header) ([]string, bool) {
	lines := h.Values(headers.NameVary)
	if len(lines) == 0 {
		return nil, true
	}
	var out []string
	for _, line := range lines {
		for name := range strings.SplitSeq(line, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if name == "*" {
				return nil, false
			}
			if cn := http.CanonicalHeaderKey(name); !slices.Contains(out, cn) {
				out = append(out, cn)
			}
		}
	}
	slices.Sort(out)
	return out, true
}

// varySecondaryKey derives the secondary cache key RFC 9111 4.1 calls for: one
// that changes whenever any nominated field changes. A field the request
// omitted is distinct from one it sent empty, so presence is part of the
// digest, and every value is length-prefixed so no combination of values can
// masquerade as another.
//
// The generation comes from the variant index. Invalidation drops the index,
// and the next store mints a new generation, so variants written before it
// can never be reached again even though they outlive their index.
func varySecondaryKey(primary, generation string, names []string, h http.Header) string {
	sum := sha256.New()
	var buf []byte
	sum.Write([]byte(generation))
	writeStr := func(s string) {
		buf = binary.AppendUvarint(buf[:0], uint64(len(s)))
		buf = append(buf, s...)
		sum.Write(buf)
	}
	for _, n := range names {
		writeStr(n)
		vv, ok := h[n]
		if !ok {
			sum.Write([]byte{0})
			continue
		}
		sum.Write([]byte{1})
		buf = binary.AppendUvarint(buf[:0], uint64(len(vv)))
		sum.Write(buf)
		for _, v := range vv {
			writeStr(v)
		}
	}
	return primary + ".v" + hex.EncodeToString(sum.Sum(nil))
}

// newVaryGeneration mints the token that scopes a URI's variant keys. It only
// has to be unguessable enough that a new generation cannot collide with the
// one an invalidation just retired.
func newVaryGeneration() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// a clock reading still moves the generation forward, which is what
		// invalidation needs; it is only less unique under a failing PRNG
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}
