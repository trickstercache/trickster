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
	"sync/atomic"
	"time"

	"github.com/tinylib/msgp/msgp"
)

//go:generate go tool msgp

// StandardLibTime is a time.Time wrapper that implements msgp.Encodable and msgp.Decodable
type StandardLibTime struct {
	time.Time
}

// AtomicTime is a msgpack compatible time.Time wrapper, backed by an atomic.Int64
type AtomicTime atomic.Int64

func (at *AtomicTime) StoreTime(ts time.Time) {
	(*atomic.Int64)(at).Store(ts.UnixNano())
}

func (at *AtomicTime) LoadTime() time.Time {
	return time.Unix(0, (*atomic.Int64)(at).Load())
}

func NewAtomicTime(in time.Time) *AtomicTime {
	at := AtomicTime{}
	at.StoreTime(in)
	return &at
}

func (at *AtomicTime) ToTime() StandardLibTime {
	return StandardLibTime{at.LoadTime()}
}

func (at *AtomicTime) FromTime(in StandardLibTime) {
	(*atomic.Int64)(at).Store(in.UnixNano())
}

func (at *AtomicTime) EncodeMsg(en *msgp.Writer) (err error) {
	return at.ToTime().EncodeMsg(en)
}

func (at *AtomicTime) MarshalMsg(b []byte) (o []byte, err error) {
	return at.ToTime().MarshalMsg(b)
}

func (at *AtomicTime) DecodeMsg(dc *msgp.Reader) (err error) {
	t := &StandardLibTime{}
	if err = t.DecodeMsg(dc); err != nil {
		return err
	}
	at.FromTime(*t)
	return
}

func (at *AtomicTime) UnmarshalMsg(bts []byte) (o []byte, err error) {
	t := &StandardLibTime{}
	o, err = t.UnmarshalMsg(bts)
	if err != nil {
		return o, err
	}
	at.FromTime(*t)
	return
}

func (at *AtomicTime) Msgsize() (s int) {
	return at.ToTime().Msgsize()
}
