/*
 * Copyright 2026 The Trickster Authors
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

package wire

import (
	"errors"
	"math"
	"slices"
)

// MaxKVValueLen bounds the value of an odd-typed Key-Value-Pair
const MaxKVValueLen = 1<<16 - 1

var (
	// ErrTypeOverflow is returned when a delta-encoded type would exceed 2^64-1
	ErrTypeOverflow = errors.New("moq: key-value type overflows")
	// ErrTypeOrder is returned when encoding Key-Value-Pairs out of ascending type order
	ErrTypeOrder = errors.New("moq: key-value types must ascend")
)

// KV is one Key-Value-Pair: an even Type carries Uint, an odd Type carries Bytes
type KV struct {
	Type  uint64
	Uint  uint64
	Bytes []byte
}

// IsBytesType reports whether values of type t are length-prefixed bytes rather than a varint
func IsBytesType(t uint64) bool {
	return t&1 == 1
}

// AppendKVs appends pairs in ascending type order, each type a delta from the one before it
func AppendKVs(b []byte, kvs []KV) ([]byte, error) {
	var prev uint64
	for i := range kvs {
		t := kvs[i].Type
		if t < prev {
			return b, ErrTypeOrder
		}
		b = AppendVarint(b, t-prev)
		prev = t
		if IsBytesType(t) {
			if len(kvs[i].Bytes) > MaxKVValueLen {
				return b, ErrTooLong
			}
			b = AppendBytes(b, kvs[i].Bytes)
			continue
		}
		b = AppendVarint(b, kvs[i].Uint)
	}
	return b, nil
}

// SortKVs orders pairs by type for AppendKVs, keeping repeated types in their given order
func SortKVs(kvs []KV) {
	slices.SortStableFunc(kvs, func(a, b KV) int {
		switch {
		case a.Type < b.Type:
			return -1
		case a.Type > b.Type:
			return 1
		}
		return 0
	})
}

// KVIter walks a block of Key-Value-Pairs that fills its input
type KVIter struct {
	r    Reader
	prev uint64
}

// NewKVIter returns an iterator over the pairs in b
func NewKVIter(b []byte) KVIter {
	return KVIter{r: NewReader(b)}
}

// Next returns the next pair, or false at the end of the block or on a malformed pair
func (it *KVIter) Next() (KV, bool) {
	if it.r.Remaining() == 0 {
		return KV{}, false
	}
	delta := it.r.Varint()
	if it.r.err != nil {
		return KV{}, false
	}
	if delta > math.MaxUint64-it.prev {
		it.r.Fail(ErrTypeOverflow)
		return KV{}, false
	}
	kv := KV{Type: it.prev + delta}
	it.prev = kv.Type
	if IsBytesType(kv.Type) {
		kv.Bytes = it.r.LenBytes(MaxKVValueLen)
	} else {
		kv.Uint = it.r.Varint()
	}
	if it.r.err != nil {
		return KV{}, false
	}
	return kv, true
}

// Err returns the first framing error the iterator met
func (it *KVIter) Err() error {
	return it.r.Err()
}

// ValidateKVs checks that b is a well-framed block of Key-Value-Pairs
func ValidateKVs(b []byte) error {
	it := NewKVIter(b)
	for {
		if _, ok := it.Next(); !ok {
			return it.Err()
		}
	}
}

// FindKV returns the first pair of type t in the block b
func FindKV(b []byte, t uint64) (KV, bool) {
	it := NewKVIter(b)
	for {
		kv, ok := it.Next()
		if !ok || kv.Type > t {
			return KV{}, false
		}
		if kv.Type == t {
			return kv, true
		}
	}
}
