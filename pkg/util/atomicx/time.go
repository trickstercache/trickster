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
	"fmt"
	"sync/atomic"
	"time"

	"github.com/tinylib/msgp/msgp"
)

const (
	TimeExtensionType = 101
)

func init() {
	msgp.RegisterExtension(TimeExtensionType, func() msgp.Extension { return new(Time) })
}

func NewTime(in time.Time) *Time {
	t := Time{}
	t.Pointer.Store(&in)
	return &t
}

// Time is a wrapper for safely accessing a timestamp that implements the msgp.Extension interface
type Time struct {
	atomic.Pointer[time.Time]
}

func (t *Time) ExtensionType() int8 {
	return TimeExtensionType
}

func (t *Time) Len() int {
	return 15
}

func (t *Time) Store(in time.Time) {
	t.Pointer.Store(&in)
}

func (t *Time) Load() time.Time {
	var ts *time.Time
	if ts = t.Pointer.Load(); ts == nil {
		fmt.Println("nil time")
		ts = &time.Time{}
	}
	fmt.Println("time", *ts)
	return *ts
}

func (t *Time) MarshalBinaryTo(b []byte) error {
	var ts *time.Time
	if ts = t.Pointer.Load(); ts == nil {
		ts = &time.Time{}
	}
	var err error
	buf, err := ts.MarshalBinary()
	copy(b, buf)
	return err
}

func (t *Time) UnmarshalBinary(b []byte) error {
	var ts time.Time
	err := ts.UnmarshalBinary(b)
	if err != nil {
		return err
	}
	t.Pointer.Store(&ts)
	return nil
}
