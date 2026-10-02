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

package epoch

import (
	"math"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// ParseDecimal parses a base-10 count of the unit (DateTimeUnixSecs, Milli, Micro
// or Nano) into an Epoch with integer math, rejecting precision finer than 1ns.
func ParseDecimal(raw []byte, unit timeseries.FieldDataType) (Epoch, error) {
	var exp int
	switch unit {
	case timeseries.DateTimeUnixSecs:
		exp = 9
	case timeseries.DateTimeUnixMilli:
		exp = 6
	case timeseries.DateTimeUnixMicro:
		exp = 3
	case timeseries.DateTimeUnixNano:
	default:
		return 0, timeseries.ErrInvalidTimeFormat
	}
	b := raw
	if n := len(b); n >= 2 && b[0] == '"' && b[n-1] == '"' {
		b = b[1 : n-1]
	}
	var neg bool
	if len(b) > 0 && (b[0] == '-' || b[0] == '+') {
		neg = b[0] == '-'
		b = b[1:]
	}
	intPart, b := leadingDigits(b)
	var frac []byte
	if len(b) > 0 && b[0] == '.' {
		frac, b = leadingDigits(b[1:])
	}
	if len(intPart)+len(frac) == 0 {
		return 0, timeseries.ErrInvalidTimeFormat
	}
	if len(b) > 0 && (b[0] == 'e' || b[0] == 'E') {
		e, ok := parseExponent(b[1:])
		if !ok {
			return 0, timeseries.ErrInvalidTimeFormat
		}
		exp += e
	} else if len(b) > 0 {
		return 0, timeseries.ErrInvalidTimeFormat
	}
	// trailing fractional zeros carry no value and would only risk overflow
	for len(frac) > 0 && frac[len(frac)-1] == '0' {
		frac = frac[:len(frac)-1]
	}
	exp -= len(frac)
	var v int64
	for _, digits := range [2][]byte{intPart, frac} {
		for _, c := range digits {
			d := int64(c - '0')
			if v > (math.MaxInt64-d)/10 {
				return 0, timeseries.ErrInvalidTimeFormat
			}
			v = v*10 + d
		}
	}
	if v == 0 {
		return 0, nil
	}
	// a nonzero int64 has at most 19 digits, so larger shifts cannot be exact
	if exp > 19 || exp < -19 {
		return 0, timeseries.ErrInvalidTimeFormat
	}
	for ; exp > 0; exp-- {
		if v > math.MaxInt64/10 {
			return 0, timeseries.ErrInvalidTimeFormat
		}
		v *= 10
	}
	for ; exp < 0; exp++ {
		if v%10 != 0 {
			return 0, timeseries.ErrInvalidTimeFormat
		}
		v /= 10
	}
	if neg {
		v = -v
	}
	return Epoch(v), nil
}

func leadingDigits(b []byte) (digits, rest []byte) {
	i := 0
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	return b[:i], b[i:]
}

func parseExponent(raw []byte) (int, bool) {
	b := raw
	var neg bool
	if len(b) > 0 && (b[0] == '-' || b[0] == '+') {
		neg = b[0] == '-'
		b = b[1:]
	}
	digits, rest := leadingDigits(b)
	if len(digits) == 0 || len(rest) > 0 {
		return 0, false
	}
	var e int
	for _, c := range digits {
		// any exponent beyond this is rejected by the caller's range check
		if e < 1000 {
			e = e*10 + int(c-'0')
		}
	}
	if neg {
		e = -e
	}
	return e, true
}
