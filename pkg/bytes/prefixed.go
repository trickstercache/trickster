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
	"encoding/binary"
	"hash"
)

// AppendLengthPrefixed appends v after its length as a uvarint, so a sequence of values appended this way can't
// be read as another sequence of values.
func AppendLengthPrefixed[T ~string | ~[]byte](dst []byte, v T) []byte {
	return append(binary.AppendUvarint(dst, uint64(len(v))), v...)
}

// WriteLengthPrefixed writes s to h as AppendLengthPrefixed appends it, for a key hashed from several parts. It writes
// through buf, returned for the next part to reuse, so hashing many parts allocates once.
func WriteLengthPrefixed(h hash.Hash, buf []byte, s string) []byte {
	buf = AppendLengthPrefixed(buf[:0], s)
	h.Write(buf)
	return buf
}
