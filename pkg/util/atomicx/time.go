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

var (
	ZeroTime = time.Unix(0, time.Time{}.UnixNano())
)

//go:generate go tool msgp

// StandardLibInt64 is a wrapper for int64 that implements msgp.Encodable and msgp.Decodable
type StandardLibInt64 int64

func NewTime(in time.Time) *Time {
	t := &Time{v: atomic.Int64{}}
	t.Store(in)
	return t
}

//msgp:ignore Time

// Time is a wrapper for atomic.Int64 that implements msgp.Encodable and msgp.Decodable
type Time struct {
	v atomic.Int64
}

func (d *Time) Load() time.Time {
	return time.Unix(0, d.v.Load())
}

func (d *Time) Store(d2 time.Time) {
	d.v.Store(d2.UnixNano())
}

func (d *Time) EncodeMsg(en *msgp.Writer) (err error) {
	ts := StandardLibInt64(d.v.Load())
	return (&ts).EncodeMsg(en)
}

func (d *Time) DecodeMsg(dc *msgp.Reader) (err error) {
	var ts StandardLibInt64
	if err := ts.DecodeMsg(dc); err != nil {
		return err
	}
	d.Store(time.Unix(0, int64(ts)))
	return
}

func (d *Time) MarshalMsg(b []byte) (o []byte, err error) {
	return StandardLibInt64(d.v.Load()).MarshalMsg(b)
}

func (d *Time) UnmarshalMsg(b []byte) (o []byte, err error) {
	var ts StandardLibInt64
	o, err = ts.UnmarshalMsg(b)
	if err != nil {
		return o, err
	}
	d.v.Store(int64(ts))
	return b, nil
}

func (d *Time) Msgsize() int {
	return msgp.Int64Size
}
