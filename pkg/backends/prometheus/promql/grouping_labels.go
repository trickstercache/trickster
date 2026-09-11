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
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/prometheus/common/model"
)

// parseLabels accepts PromQL's optional trailing comma and quoted UTF-8 label names.
func parseLabels(input string) ([]string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, true
	}

	parts := make([]string, 0, strings.Count(input, ",")+1)
	start := 0
	var quote byte
	var escaped bool
	for i := range input {
		c := input[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' && quote != '`' {
				escaped = true
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quote = c
		case ',':
			part := strings.TrimSpace(input[start:i])
			if part == "" {
				return nil, false
			}
			parts = append(parts, part)
			start = i + 1
		}
	}
	if quote != 0 {
		return nil, false
	}
	if part := strings.TrimSpace(input[start:]); part != "" {
		parts = append(parts, part)
	} else if len(parts) == 0 {
		return nil, false
	}

	seen := make(map[string]struct{}, len(parts))
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		label, ok := parseGroupingLabel(part)
		if !ok {
			return nil, false
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	slices.Sort(labels)
	return labels, true
}

func parseGroupingLabel(input string) (string, bool) {
	if input == "" {
		return "", false
	}
	if input[0] == '"' || input[0] == '\'' || input[0] == '`' {
		label, ok := unquotePromQLString(input)
		return label, ok && model.UTF8Validation.IsValidLabelName(label)
	}
	for i := range input {
		c := input[i]
		if i == 0 {
			if !isPromQLGroupingIdentifierStart(c) {
				return "", false
			}
			continue
		}
		if !isPromQLGroupingIdentifierPart(c) {
			return "", false
		}
	}
	return input, model.UTF8Validation.IsValidLabelName(input)
}

func unquotePromQLString(input string) (string, bool) {
	if len(input) < 2 || input[0] != input[len(input)-1] {
		return "", false
	}
	quote := input[0]
	input = input[1 : len(input)-1]
	if quote == '`' {
		if strings.ContainsRune(input, '`') {
			return "", false
		}
		return input, true
	}
	if (quote != '"' && quote != '\'') || strings.ContainsRune(input, '\n') {
		return "", false
	}

	var output strings.Builder
	output.Grow(len(input))
	for input != "" {
		value, multibyte, tail, err := strconv.UnquoteChar(input, quote)
		if err != nil {
			return "", false
		}
		switch {
		case value >= 0 && value < utf8.RuneSelf:
			output.WriteByte(byte(value))
		case !multibyte && value >= 0 && value <= 255:
			output.WriteByte(byte(value))
		case multibyte:
			_, _ = output.WriteRune(value)
		default:
			return "", false
		}
		input = tail
	}
	return output.String(), true
}

func isPromQLGroupingIdentifierStart(c byte) bool {
	return c == '_' || c == ':' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isPromQLGroupingIdentifierPart(c byte) bool {
	return isPromQLGroupingIdentifierStart(c) || c >= '0' && c <= '9'
}
