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

package stream

// an Interner shares up to maxInterned strings, and Short only those no longer than maxInternedLen
const (
	maxInterned    = 4096
	maxInternedLen = 64
)

// Interner converts bytes to strings, sharing one string for bytes it has seen, so the names and
// short values a response repeats across its series are allocated once. The zero value is ready.
type Interner struct {
	m map[string]string
}

// String returns b as a string, shared with earlier equal bytes, and shared with later ones too when
// share is set and the Interner isn't full.
func (in *Interner) String(b []byte, share bool) string {
	if s, ok := in.m[string(b)]; ok {
		return s
	}
	s := string(b)
	if share && len(in.m) < maxInterned {
		if in.m == nil {
			in.m = make(map[string]string)
		}
		in.m[s] = s
	}
	return s
}

// Short returns b as a string, shared when it is short enough that it is likely repeated.
func (in *Interner) Short(b []byte) string {
	return in.String(b, len(b) <= maxInternedLen)
}
