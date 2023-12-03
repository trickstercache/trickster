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

// Package timeconv provides time conversion capabilities to Trickster
package timeconv

import (
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/util/strings"
)

const (
	UnitMonth         = DurationUnit("mo")
	UnitMillisecond   = DurationUnit("ms")
	UnitMicrosecond   = DurationUnit("us")
	UnitMicrosecondB5 = DurationUnit("µs")
	UnitMicrosecondBC = DurationUnit("μs")
	UnitNanosecond    = DurationUnit("ns")
	UnitYear          = DurationUnit("y")
	UnitWeek          = DurationUnit("w")
	UnitDay           = DurationUnit("d")
	UnitHour          = DurationUnit("h")
	UnitMinute        = DurationUnit("m")
	UnitSecond        = DurationUnit("s")
	UnitMicro         = DurationUnit("u")
	UnitMicroB5       = DurationUnit("µ")
	UnitMicroBC       = DurationUnit("μ")
	UnitNil           = DurationUnit("nil")
)

const (
	Year        = Hour * 8760
	Month       = Hour * 730
	Week        = Day * 7
	Day         = Hour * 24
	Hour        = Minute * 60
	Minute      = Second * 60
	Second      = time.Second
	Millisecond = time.Millisecond
	Microsecond = time.Microsecond
	Nanosecond  = time.Nanosecond
)

// Slice of units supported by the package.
// PLEASE NOTE that when parsing durations, these units will be checked in this order--for example,
// if minute "m" is before month "mo" or millisecond "ms", the parser will fail to recognize months and milliseconds
// in duration literals.
var Units []DurationUnit = []DurationUnit{
	UnitMonth,
	UnitMillisecond,
	UnitMicrosecond,
	UnitMicrosecondB5,
	UnitMicrosecondBC,
	UnitNanosecond,
	UnitYear,
	UnitWeek,
	UnitDay,
	UnitHour,
	UnitMinute,
	UnitSecond,
	UnitMicro,
	UnitMicroB5,
	UnitMicroBC,
}

var Durations map[DurationUnit]time.Duration = map[DurationUnit]time.Duration{
	UnitYear:          Year,
	UnitMonth:         Month,
	UnitWeek:          Week,
	UnitDay:           Day,
	UnitHour:          Hour,
	UnitMinute:        Minute,
	UnitSecond:        Second,
	UnitMillisecond:   Millisecond,
	UnitMicrosecond:   Microsecond,
	UnitMicrosecondB5: Microsecond,
	UnitMicrosecondBC: Microsecond,
	UnitMicro:         Microsecond,
	UnitMicroB5:       Microsecond,
	UnitMicroBC:       Microsecond,
	UnitNanosecond:    Nanosecond,
}

type DurationUnit string

func isUnit(s string, u DurationUnit) bool {
	return s == string(u)
}

// Determine if rune is a digit, allowing for a sign if true, based on ASCII values
func isDigit(s rune, allowSign bool) bool {
	return (allowSign && s == 45) || (s >= 48 && s <= 57)
}

// Determine if a unit is at the current string position.
// Returns the unit, true, and the amount to increment by if a unit is present.
// Otherwise, returns UnitNil, false, and 1.
func isUnitAtPos(s string, i int) (u DurationUnit, is bool, inc int) {
	for _, unit := range Units {
		if isUnit(strings.Substring(s, i, len(unit)), unit) {
			return unit, true, len(unit)
		}
	}
	return UnitNil, false, 1
}

func isIntAtPos(s string, i int) (v int64, is bool, inc int) {
	var j int
	for j = i; j < len(s); j++ {
		c := rune(s[j])
		if !isDigit(c, i == j) {
			break
		}
	}
	if i == j {
		return 0, false, 1
	}
	token := s[i:j]
	v, _ = strconv.ParseInt(token, 10, 64)
	return v, true, j - i
}

// ParseDuration returns a duration from a string. Slightly improved over the builtin,
// since it supports units larger than hour.
// Parse a literal duration.
// Durations are formatted as [signed int][unit]..., with each int-unit pair representing a number of those units of duration.
func ParseDuration(s string) (time.Duration, error) {
	if len(s) <= 1 {
		return 0, ErrInvalidDurationFormat(0, "value of at least length 2", s)
	}
	var d time.Duration = 0
	var currentMult int64 = 0
	for i := 0; i < len(s); {
		if currentMult == 0 {
			v, is, inc := isIntAtPos(s, i)
			if !is {
				return 0, ErrInvalidDurationFormat(i, "valid integer value", s)
			}
			currentMult = v
			i += inc
		} else {
			u, is, inc := isUnitAtPos(s, i)
			if !is {
				return 0, ErrInvalidDurationFormat(i, "valid duration unit", s)
			}
			d += time.Duration(currentMult) * Durations[u]
			currentMult = 0
			i += inc
		}
	}
	// If we don't have a duration at this point, catch-all with ErrUnableToParse
	if d == 0 {
		return d, ErrInvalidDurationFormat(0, "valid duration string", s)
	}
	// Multiplier should be set to zero at this point; if there isn't, it means there was a trailing
	// multiplier without a unit.
	if currentMult != 0 {
		return 0, ErrInvalidDurationFormat(len(s), "valid duration unit", s)
	}
	return d, nil
}
