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

// Package bytes provides byte-slice helpers shared across Trickster.
package bytes

import (
	"errors"
	"fmt"
	"io"
)

// ErrBodyTooLarge is returned by ReadBoundedBody when a whole-body read
// exceeds its limit. Callers wanting to distinguish an oversized payload
// from a transport failure can test for it with errors.Is.
var ErrBodyTooLarge = errors.New("body exceeds the maximum allowed size")

// ErrNegativeLimit is returned by ReadBoundedBody when max is negative.
var ErrNegativeLimit = errors.New("read limit cannot be negative")

// ReadBoundedBody reads from r under a size bound, in one of two modes.
//
// With truncate false, it reads the whole body and returns
// ErrBodyTooLarge if it exceeds max. Use this when the bytes are a
// document that is only meaningful whole -- a member list, a service
// catalog -- where a truncated read would parse into a plausible but wrong
// result. Failing is what lets the caller keep its last-good state instead
// of applying a fragment.
//
// With truncate true, it reads exactly max bytes and does not care whether
// more remain. Use this for a fixed-width field in a framed wire protocol,
// where the length is known ahead of the read and the remaining bytes
// belong to whatever comes next. A short read is still an error
// (io.ErrUnexpectedEOF), and the partially-filled buffer is returned
// alongside it.
func ReadBoundedBody(r io.Reader, max int, truncate bool) ([]byte, error) {
	if max < 0 {
		return nil, ErrNegativeLimit
	}
	if truncate {
		b := make([]byte, max)
		_, err := io.ReadFull(r, b)
		return b, err
	}
	// read one byte past the limit, so that hitting it is distinguishable
	// from a body that merely ends exactly at it
	b, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, fmt.Errorf("%w (limit %d bytes)", ErrBodyTooLarge, max)
	}
	return b, nil
}
