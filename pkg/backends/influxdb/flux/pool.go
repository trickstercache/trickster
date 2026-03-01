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

package flux

import (
	"bytes"
	"sync"
)

// maxMarshalBufSize is the maximum buffer capacity to allow back into the pool.
// Buffers that grew beyond this are discarded to prevent pool bloat.
const maxMarshalBufSize = 64 << 10 // 64 KB

var marshalBufPool = sync.Pool{
	New: func() any { return &bytes.Buffer{} },
}

func getMarshalBuf() *bytes.Buffer {
	buf := marshalBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

func putMarshalBuf(buf *bytes.Buffer) {
	if buf == nil {
		return
	}
	if buf.Cap() > maxMarshalBufSize {
		return
	}
	marshalBufPool.Put(buf)
}
