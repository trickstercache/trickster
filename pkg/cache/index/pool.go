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

package index

import "sync"

// maxObjectMsgBufSize is the maximum serialized-object buffer capacity to allow
// back into the pool. Buffers that grew beyond this are discarded to prevent bloat.
const maxObjectMsgBufSize = 1 << 20 // 1 MB

// objectPool pools *Object structs for operations where the object is fully
// consumed within a single call (Store serialization, Retrieve deserialization).
// Objects stored in the index (via updateIndex) are NOT pooled — they live in
// the sync.Map for the duration of the cache entry.
var objectPool = sync.Pool{New: func() any { return &Object{} }}

func getObject() *Object {
	return objectPool.Get().(*Object)
}

// putObject zeros reference-holding fields before returning the Object to the pool.
func putObject(o *Object) {
	o.Key = ""
	o.Value = nil
	o.ReferenceValue = nil
	objectPool.Put(o)
}

// objectMsgBufPool pools []byte slices used as output buffers for msgp serialization.
// Stored as *[]byte to preserve the slice header (capacity) across pool round-trips.
var objectMsgBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 512)
		return &b
	},
}

func getObjectMsgBuf() []byte {
	bp := objectMsgBufPool.Get().(*[]byte)
	return (*bp)[:0]
}

// putObjectMsgBuf returns b to the pool. b must not be used after this call.
func putObjectMsgBuf(b []byte) {
	if cap(b) > maxObjectMsgBufSize {
		return
	}
	objectMsgBufPool.Put(&b)
}
