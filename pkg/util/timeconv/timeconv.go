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
	"fmt"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
)

// ParseDuration returns a duration from a string. Slightly improved over the builtin,
// since it supports units larger than hour.
func ParseDuration(input string) (time.Duration, error) {
	for i := range input {
		if input[i] > 47 && input[i] < 58 {
			continue
		}
		if input[i] == 46 {
			break
		}
		if i > 0 {
			if _, ok := UnitMap[input[i:]]; !ok {
				return errors.ParseDuration(input)
			}
			v, err := strconv.ParseInt(input[0:i], 10, 64)
			if err != nil {
				return errors.ParseDuration(input)
			}
			return ParseDurationParts(v, input[i:])
		}
	}
	return errors.ParseDuration(input)
}

// ParseDurationParts returns a time.Duration from a value and unit
func ParseDurationParts(value int64, units string) (time.Duration, error) {
	if _, ok := UnitMap[units]; !ok {
		return errors.ParseDuration(fmt.Sprintf("%d%s", value, units))
	}
	return time.Duration(value) * UnitMap[units], nil
}

// UnitMap provides a map of common time unit abbreviations to their respective time.Durations
var UnitMap = map[string]time.Duration{
	"ns": time.Nanosecond,
	"u":  time.Microsecond,
	"µ":  time.Microsecond, // U+00B5 = micro symbol
	"μ":  time.Microsecond, // U+03BC = Greek letter mu
	"us": time.Microsecond,
	"µs": time.Microsecond, // U+00B5 = micro symbol
	"μs": time.Microsecond, // U+03BC = Greek letter mu
	"ms": time.Millisecond,
	"s":  time.Second,
	"m":  time.Minute,
	"h":  time.Hour,
	"d":  24 * time.Hour,
	"w":  24 * 7 * time.Hour,
	"y":  24 * 365 * time.Hour,
}
