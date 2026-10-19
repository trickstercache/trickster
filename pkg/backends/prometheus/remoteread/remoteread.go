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

// Package remoteread reads Prometheus remote-read responses straight from their protobuf wire format, so a decoder
// can count what a response holds, and refuse it, before allocating what it keeps.
package remoteread

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/klauspost/compress/snappy"
	"google.golang.org/protobuf/encoding/protowire"
)

// Prometheus remote-read field numbers of ReadResponse, QueryResult, TimeSeries, Label and Sample
const (
	ReadResponseResults   = 1
	QueryResultTimeseries = 1
	TimeSeriesLabels      = 1
	TimeSeriesSamples     = 2
	TimeSeriesHistograms  = 4
	LabelName             = 1
	LabelValue            = 2
	SampleValue           = 1
	SampleTimestamp       = 2
)

// DecodeExpansion is how many times a response's byte limit the series a decoder keeps from it may take in memory, above
// what short labels and few samples a series expand to.
const DecodeExpansion = 4

const (
	// sampleValueTag and sampleTimestampTag start a Sample's value, a fixed64, and its time, a varint, which the value's
	// tag and eight bytes, sampleFixedBytes, precede as Prometheus writes them
	sampleValueTag     = SampleValue<<3 | byte(protowire.Fixed64Type)
	sampleTimestampTag = SampleTimestamp<<3 | byte(protowire.VarintType)
	sampleFixedBytes   = 9
)

// Field is one field of a protobuf message: its varint or fixed-size value in U, or its bytes in B when length-delimited.
type Field struct {
	Num protowire.Number
	Typ protowire.Type
	U   uint64
	B   []byte
}

// Reader reads protobuf messages, failing a malformed one with an error that wraps Kind.
type Reader struct {
	Kind error
}

// Field reads the field b starts with, returning the bytes after it; any length is checked against b's.
func (r Reader) Field(b []byte) (Field, []byte, error) {
	num, typ, n := protowire.ConsumeTag(b)
	if n < 0 {
		return Field{}, nil, r.ParseError(n)
	}
	b = b[n:]
	f := Field{Num: num, Typ: typ}
	switch typ {
	case protowire.VarintType:
		f.U, n = protowire.ConsumeVarint(b)
	case protowire.Fixed64Type:
		f.U, n = protowire.ConsumeFixed64(b)
	case protowire.BytesType:
		f.B, n = protowire.ConsumeBytes(b)
	default:
		n = protowire.ConsumeFieldValue(num, typ, b)
	}
	if n < 0 {
		return Field{}, nil, r.ParseError(n)
	}
	return f, b[n:], nil
}

// ParseError returns the error for a protowire consume result n, which is negative.
func (r Reader) ParseError(n int) error {
	return fmt.Errorf("%w: %w", r.Kind, protowire.ParseError(n))
}

// Expect refuses a field without the wire type its field number is declared with.
func (r Reader) Expect(f Field, typ protowire.Type) error {
	if f.Typ == typ {
		return nil
	}
	return fmt.Errorf("%w: field %d has wire type %d, not %d", r.Kind, f.Num, f.Typ, typ)
}

// Repeated returns how many values a repeated scalar field holds: one when it isn't packed, and is of wire type typ,
// or as many varints, or fixed64 values when typ is that, as its packed bytes hold.
func (r Reader) Repeated(f Field, typ protowire.Type) (int, error) {
	if f.Typ == typ {
		return 1, nil
	}
	if err := r.Expect(f, protowire.BytesType); err != nil {
		return 0, err
	}
	if typ == protowire.Fixed64Type {
		if len(f.B)%8 != 0 {
			return 0, fmt.Errorf("%w: field %d packs %d bytes, not whole 8-byte values", r.Kind, f.Num, len(f.B))
		}
		return len(f.B) / 8, nil
	}
	var count int
	for b := f.B; len(b) > 0; count++ {
		_, n := protowire.ConsumeVarint(b)
		if n < 0 {
			return 0, r.ParseError(n)
		}
		b = b[n:]
	}
	return count, nil
}

// Label reads a Label message's name and value, which share b's memory.
func (r Reader) Label(b []byte) (name, value []byte, err error) {
	for len(b) > 0 {
		var f Field
		if f, b, err = r.Field(b); err != nil {
			return nil, nil, err
		}
		if f.Num != LabelName && f.Num != LabelValue {
			continue
		}
		if err = r.Expect(f, protowire.BytesType); err != nil {
			return nil, nil, err
		}
		if f.Num == LabelName {
			name = f.B
		} else {
			value = f.B
		}
	}
	return name, value, nil
}

// Sample reads a Sample message's time, in milliseconds, and value.
func (r Reader) Sample(b []byte) (t int64, v float64, err error) {
	// a sample written as Prometheus writes one, its value and then its time, is read directly
	if len(b) > sampleFixedBytes && b[0] == sampleValueTag && b[sampleFixedBytes] == sampleTimestampTag {
		if u, n := protowire.ConsumeVarint(b[sampleFixedBytes+1:]); n == len(b)-sampleFixedBytes-1 {
			return int64(u), math.Float64frombits(binary.LittleEndian.Uint64(b[1:sampleFixedBytes])), nil //nolint:gosec // wire protocol reinterpret
		}
	}
	for len(b) > 0 {
		var f Field
		if f, b, err = r.Field(b); err != nil {
			return 0, 0, err
		}
		switch f.Num {
		case SampleValue:
			if err = r.Expect(f, protowire.Fixed64Type); err != nil {
				return 0, 0, err
			}
			v = math.Float64frombits(f.U)
		case SampleTimestamp:
			if err = r.Expect(f, protowire.VarintType); err != nil {
				return 0, 0, err
			}
			t = int64(f.U) //nolint:gosec // wire protocol reinterpret
		}
	}
	return t, v, nil
}

// Series calls fn with each TimeSeries message of a ReadResponse, in order, and the index of the QueryResult holding
// it, returning how many QueryResults the response holds; it stops at the first error fn returns.
func (r Reader) Series(b []byte, fn func(result int, series []byte) error) (int, error) {
	var results int
	for len(b) > 0 {
		f, rest, err := r.Field(b)
		if err != nil {
			return results, err
		}
		b = rest
		if f.Num != ReadResponseResults {
			continue
		}
		if err := r.Expect(f, protowire.BytesType); err != nil {
			return results, err
		}
		for qr := f.B; len(qr) > 0; {
			ts, next, err := r.Field(qr)
			if err != nil {
				return results, err
			}
			qr = next
			if ts.Num != QueryResultTimeseries {
				continue
			}
			if err := r.Expect(ts, protowire.BytesType); err != nil {
				return results, err
			}
			if err := fn(results, ts.B); err != nil {
				return results, err
			}
		}
		results++
	}
	return results, nil
}

// DecodeAllowance returns the most memory the series a decoder keeps from a response of at most limit bytes may take.
func DecodeAllowance(limit int64) int64 {
	if limit > math.MaxInt64/DecodeExpansion {
		return math.MaxInt64
	}
	return DecodeExpansion * limit
}

// DecodeSnappy decodes a snappy block, first refusing one whose decoded length exceeds limit with an error that
// wraps tooLarge, so an oversized block is never allocated; a limit of 0 or less is no limit.
func DecodeSnappy(compressed []byte, limit int64, tooLarge error) ([]byte, error) {
	size, err := snappy.DecodedLen(compressed)
	if err != nil {
		return nil, err
	}
	if limit > 0 && int64(size) > limit {
		return nil, fmt.Errorf("%w: a response of %d bytes decoded exceeds %d", tooLarge, size, limit)
	}
	return snappy.Decode(nil, compressed)
}
