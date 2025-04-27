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

package atomicx

import (
	"encoding/binary"
	"sync/atomic"
	"time"

	"github.com/tinylib/msgp/msgp"
)

var (
	ZeroTime = time.Unix(0, time.Time{}.UnixNano())
)

//go:generate go tool msgp

func NewTime(in time.Time) Time {
	t := Time{}
	t.v = in.UnixNano()
	return t
}

//msgp:ignore Time

func init() {
	msgp.RegisterExtension(TimeExtensionType, func() msgp.Extension { return new(Time) })
}

// Time is a wrapper for safely accessing a timestamp that implements the msgp.Extension interface
type Time struct {
	v int64
}

const (
	TimeExtensionType = 101
)

var (
	encoder = binary.LittleEndian
)

func (t *Time) ExtensionType() int8 {
	return TimeExtensionType
}

func (t *Time) Len() int {
	return 8
}

func (t *Time) MarshalBinaryTo(b []byte) error {
	encoder.PutUint64(b, uint64(atomic.LoadInt64(&t.v)))
	return nil
}
func (t *Time) UnmarshalBinary(b []byte) error {
	atomic.StoreInt64(&t.v, int64(encoder.Uint64(b)))
	return nil
}

func (t *Time) Load() time.Time {
	return time.Unix(0, atomic.LoadInt64(&t.v))
}

func (t *Time) IsZero() bool {
	return t.Equal(ZeroTime)
}

func (t *Time) Equal(t2 time.Time) bool {
	return t.Load().UnixNano() == t2.UnixNano()
}

func (t *Time) Store(t2 time.Time) {
	atomic.StoreInt64(&t.v, t2.UnixNano())
}
