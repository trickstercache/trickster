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

package promql

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/common/model"
)

// parsePromQLScalarLiteral parses the numeric and duration literal forms that
// PromQL accepts as scalar aggregation parameters. It deliberately excludes
// scalar expressions, which cannot be evaluated once per global TSM step.
func parsePromQLScalarLiteral(input string) (float64, bool) {
	literal := strings.TrimSpace(input)
	if literal == "" {
		return 0, false
	}
	lower := strings.ToLower(literal)
	switch lower {
	case "nan", "+nan", "-nan":
		return math.NaN(), true
	case "inf", "+inf":
		return math.Inf(1), true
	case "-inf":
		return math.Inf(-1), true
	}

	unsigned := literal
	sign := 1.0
	if unsigned[0] == '+' || unsigned[0] == '-' {
		if unsigned[0] == '-' {
			sign = -1
		}
		unsigned = unsigned[1:]
		if unsigned == "" {
			return 0, false
		}
	}
	unsignedLower := strings.ToLower(unsigned)
	if strings.HasPrefix(unsignedLower, "0b") ||
		strings.HasPrefix(unsignedLower, "0o") ||
		strings.Contains(lower, "infinity") {
		return 0, false
	}
	// Prometheus first parses integer tokens with base 0, which makes a
	// leading zero octal and permits hexadecimal integers. Its lexer does not
	// accept binary/octal prefixes or hexadecimal floating-point syntax.
	if value, err := strconv.ParseInt(literal, 0, 64); err == nil {
		return float64(value), true
	}
	if strings.HasPrefix(unsignedLower, "0x") {
		return 0, false
	}
	if value, err := strconv.ParseFloat(literal, 64); err == nil {
		return value, true
	}
	duration, err := model.ParseDuration(unsigned)
	if err != nil {
		return 0, false
	}
	seconds := float64(time.Duration(duration)) / float64(time.Second)
	return sign * seconds, true
}
