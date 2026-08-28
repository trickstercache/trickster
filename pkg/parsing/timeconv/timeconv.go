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
	"crypto/rand"
	"math"
	"math/big"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/util/strings"

	"go.yaml.in/yaml/v3"
)

const (
	// SQLDateLayout is the go-formatted date representation of a SQL Basic Date
	SQLDateLayout = "2006-01-02"

	// SQLTimeLayout is the go-formatted date representation of a SQL Basic Date
	SQLTimeLayout = "15:04:05"

	// SQLDateTimeLayout is the go-formatted date representation of a SQL Basic DateTime
	SQLDateTimeLayout = SQLDateLayout + " " + SQLTimeLayout

	SQLDateTimeSubSec1Layout = SQLDateTimeLayout + ".0"
	SQLDateTimeSubSec2Layout = SQLDateTimeLayout + ".00"
	SQLDateTimeSubSec3Layout = SQLDateTimeLayout + ".000"
	SQLDateTimeSubSec4Layout = SQLDateTimeLayout + ".0000"
	SQLDateTimeSubSec5Layout = SQLDateTimeLayout + ".00000"
	SQLDateTimeSubSec6Layout = SQLDateTimeLayout + ".000000"
	SQLDateTimeSubSec7Layout = SQLDateTimeLayout + ".0000000"
	SQLDateTimeSubSec8Layout = SQLDateTimeLayout + ".00000000"
	SQLDateTimeSubSec9Layout = SQLDateTimeLayout + ".000000000"
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
var Units = []DurationUnit{
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

var Durations = map[DurationUnit]time.Duration{
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
	v, err := strconv.ParseInt(token, 10, 64)
	if err != nil {
		return 0, false, j - i
	}
	return v, true, j - i
}

func addDurationComponent(d time.Duration, multiplier int64, unit DurationUnit) (time.Duration, bool) {
	unitDuration := int64(Durations[unit])
	if unitDuration <= 0 {
		return 0, false
	}
	if multiplier > 0 && multiplier > math.MaxInt64/unitDuration {
		return 0, false
	}
	if multiplier < 0 && multiplier < math.MinInt64/unitDuration {
		return 0, false
	}

	component := multiplier * unitDuration
	total := int64(d)
	if component > 0 && total > math.MaxInt64-component {
		return 0, false
	}
	if component < 0 && total < math.MinInt64-component {
		return 0, false
	}
	return time.Duration(total + component), true
}

// ParseDuration returns a duration from a string. Slightly improved over the builtin,
// since it supports units larger than hour.
// Parse a literal duration.
// Durations are formatted as [signed int][unit]..., with each int-unit pair representing a number of those units of duration.
func ParseDuration(s string) (time.Duration, error) {
	// Preserve the complete standard-library grammar, including fractional
	// values, before trying Trickster's larger duration units.
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if len(s) <= 1 {
		return 0, ErrInvalidDurationFormat(0, "value of at least length 2", s)
	}
	var d time.Duration
	var currentMult int64
	var hasMult bool
	var hasUnits bool
	for i := 0; i < len(s); {
		if !hasMult {
			v, is, inc := isIntAtPos(s, i)
			if !is {
				return 0, ErrInvalidDurationFormat(i, "valid integer value", s)
			}
			currentMult = v
			hasMult = true
			i += inc
		} else {
			u, is, inc := isUnitAtPos(s, i)
			if !is {
				return 0, ErrInvalidDurationFormat(i, "valid duration unit", s)
			}
			var ok bool
			d, ok = addDurationComponent(d, currentMult, u)
			if !ok {
				return 0, ErrInvalidDurationFormat(i, "duration within int64 range", s)
			}
			currentMult = 0
			hasMult = false
			hasUnits = true
			i += inc
		}
	}
	if !hasUnits {
		return 0, ErrInvalidDurationFormat(0, "valid duration string", s)
	}
	if hasMult {
		return 0, ErrInvalidDurationFormat(len(s), "valid duration unit", s)
	}
	return d, nil
}

// SleepRandomMS sleeps a random amount of MS between min and max (inclusive)
func SleepRandomMS(min, max int) {
	delay := min
	max++
	if n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min))); err == nil {
		delay += int(n.Int64())
	}
	time.Sleep(time.Duration(delay) * time.Millisecond)
}

// Duration is a custom time.Duration type that supports custom YAML marshalling
type Duration time.Duration

// UnmarshalYAML unmarshals a string into a timeconv.Duration
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}

	parsed, err := ParseDuration(s)
	if err != nil {
		return err
	}

	*d = Duration(parsed)
	return nil
}

// MarshalYAML marshals a timeconv.Duration to string format
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}
