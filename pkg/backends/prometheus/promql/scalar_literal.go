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
	if strings.HasPrefix(unsignedLower, "0x") ||
		strings.Contains(lower, "infinity") {
		return 0, false
	}
	normalized, ok := normalizePromQLNumericUnderscores(literal)
	if !ok {
		return 0, false
	}
	if value, err := strconv.ParseFloat(normalized, 64); err == nil {
		return value, true
	}
	normalizedUnsigned := normalized
	if normalizedUnsigned[0] == '+' || normalizedUnsigned[0] == '-' {
		normalizedUnsigned = normalizedUnsigned[1:]
	}
	duration, err := model.ParseDuration(normalizedUnsigned)
	if err != nil {
		return 0, false
	}
	seconds := float64(time.Duration(duration)) / float64(time.Second)
	return sign * seconds, true
}

func normalizePromQLNumericUnderscores(literal string) (string, bool) {
	if !strings.Contains(literal, "_") {
		return literal, true
	}
	for i, char := range []byte(literal) {
		if char != '_' {
			continue
		}
		if i == 0 || i+1 == len(literal) ||
			literal[i-1] < '0' || literal[i-1] > '9' ||
			literal[i+1] < '0' || literal[i+1] > '9' {
			return "", false
		}
	}
	return strings.ReplaceAll(literal, "_", ""), true
}
