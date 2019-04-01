/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package proxy

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/Comcast/trickster/internal/util/regexp/matching"
)

var reDuration = regexp.MustCompile(`(?P<value>[0-9]+)(?P<unit>(ms|s|m|h|d|w|y|ns|us|µs|μs))`)

func parseDurationError(input string) (time.Duration, error) {
	return time.Duration(0), fmt.Errorf("unable to parse duration %s", input)
}

// ParseDuration returns a duration from a string. Slightly improved over the builtin, since it supports units larger than hour.
func ParseDuration(input string) (time.Duration, error) {

	m := matching.GetNamedMatches(reDuration, input, []string{"value", "unit"})

	var u, v string
	var vi int64
	var ok bool
	var err error

	if v, ok = m["value"]; !ok {
		return parseDurationError(input)
	}

	if u, ok = m["unit"]; !ok {
		return parseDurationError(input)
	}

	vi, err = strconv.ParseInt(v, 10, 64)
	if err != nil {
		return parseDurationError(input)
	}

	return ParseDurationParts(vi, u)
}

// ParseDurationParts returns a time.Duration from a value and unit
func ParseDurationParts(value int64, units string) (time.Duration, error) {
	if _, ok := unitMap[units]; !ok {
		return parseDurationError(fmt.Sprintf("%d%s", value, units))
	}
	return time.Duration(value * unitMap[units]), nil
}

var unitMap = map[string]int64{
	"ns": int64(time.Nanosecond),
	"us": int64(time.Microsecond),
	"µs": int64(time.Microsecond), // U+00B5 = micro symbol
	"μs": int64(time.Microsecond), // U+03BC = Greek letter mu
	"ms": int64(time.Millisecond),
	"s":  int64(time.Second),
	"m":  int64(time.Minute),
	"h":  int64(time.Hour),
	"d":  int64(24 * time.Hour),
	"w":  int64(24 * 7 * time.Hour),
	"y":  int64(24 * 365 * time.Hour),
}
