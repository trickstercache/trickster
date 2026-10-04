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
)

// Token Alias Types say which Token fields follow and how the receiver treats the alias
const (
	TokenDelete   uint64 = 0x0
	TokenRegister uint64 = 0x1
	TokenUseAlias uint64 = 0x2
	TokenUseValue uint64 = 0x3
)

// ErrToken is returned when an authorization Token cannot be decoded
var ErrToken = errors.New("moq: malformed authorization token")

// Token is an authorization token, possibly registered under or referring to a session-specific alias
type Token struct {
	AliasType uint64
	Alias     uint64
	Type      uint64
	Value     []byte
}

// AppendToken appends the fields t's alias type calls for; the value runs to the end of the enclosing field
func AppendToken(b []byte, t Token) ([]byte, error) {
	b = AppendVarint(b, t.AliasType)
	switch t.AliasType {
	case TokenDelete, TokenUseAlias:
		return AppendVarint(b, t.Alias), nil
	case TokenRegister:
		b = AppendVarint(AppendVarint(b, t.Alias), t.Type)
	case TokenUseValue:
		b = AppendVarint(b, t.Type)
	default:
		return b, ErrToken
	}
	return append(b, t.Value...), nil
}

// ParseToken decodes a Token that fills b
func ParseToken(b []byte) (Token, error) {
	r := NewReader(b)
	t := Token{AliasType: r.Varint()}
	switch t.AliasType {
	case TokenDelete, TokenUseAlias:
		t.Alias = r.Varint()
	case TokenRegister:
		t.Alias = r.Varint()
		t.Type = r.Varint()
		t.Value = r.Rest()
	case TokenUseValue:
		t.Type = r.Varint()
		t.Value = r.Rest()
	default:
		return Token{}, ErrToken
	}
	if r.Done() != nil {
		return Token{}, ErrToken
	}
	return t, nil
}

// Range is an inclusive span of integer values; Open leaves its end unbounded
type Range struct {
	Start uint64
	End   uint64
	Open  bool
}

// Contains reports whether v falls within the range
func (rg Range) Contains(v uint64) bool {
	return v >= rg.Start && (rg.Open || v <= rg.End)
}

var (
	// ErrRangeOrder is returned when ranges are not ascending, or an open range is not the last
	ErrRangeOrder = errors.New("moq: ranges must ascend and only the last may be open")
	// ErrRangeOverflow is returned when a delta-encoded range bound exceeds 2^64-1
	ErrRangeOverflow = errors.New("moq: range bound overflows")
)

// AppendRanges appends ranges with each Start a delta from the prior End (from 0 for the first) and each End a
// delta from its own Start; only the last range may be open
func AppendRanges(b []byte, rs []Range) ([]byte, error) {
	var prev uint64
	for i, rg := range rs {
		if rg.Start < prev || (!rg.Open && rg.End < rg.Start) || (rg.Open && i != len(rs)-1) {
			return b, ErrRangeOrder
		}
		b = AppendVarint(b, rg.Start-prev)
		if rg.Open {
			break
		}
		b = AppendVarint(b, rg.End-rg.Start)
		prev = rg.End
	}
	return b, nil
}

// ParseRanges decodes the ranges that fill b
func ParseRanges(b []byte) ([]Range, error) {
	r := NewReader(b)
	var out []Range
	var prev uint64
	for r.Remaining() > 0 {
		d := r.Varint()
		if r.err != nil {
			return nil, r.err
		}
		if d > math.MaxUint64-prev {
			return nil, ErrRangeOverflow
		}
		rg := Range{Start: prev + d}
		if r.Remaining() == 0 {
			rg.Open = true
			out = append(out, rg)
			break
		}
		e := r.Varint()
		if r.err != nil {
			return nil, r.err
		}
		if e > math.MaxUint64-rg.Start {
			return nil, ErrRangeOverflow
		}
		rg.End = rg.Start + e
		prev = rg.End
		out = append(out, rg)
	}
	return out, nil
}
