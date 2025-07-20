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

// Package strings provides extended functionality for string types
package strings

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
)

func EscapeQuotes(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `"`, `\"`), `\\"`, `\"`)
}

// Get s[i:i+length].
// Returns an empty string if i+length > len(s)
func Substring(s string, i int, length int) string {
	if i+length > len(s) {
		return ""
	}
	return s[i : i+length]
}

// Unique returns a uniqueified version of the list
func Unique(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

// ErrKeyNotInMap represents an error for key not found in map
var ErrKeyNotInMap = errors.New("key not found in map")

// Map represents a map[string]string
type Map map[string]string

// Lookup represents a map[string]any with assumed nil values
type Lookup map[string]any

func (m Map) String() string {
	b, _ := json.Marshal(m)
	return string(b)
}

// GetInt returns an integer value from the map, if convertible
// If not, an error is returned with a value of 0
func (m Map) GetInt(key string) (int, error) {
	value, ok := m[key]
	if !ok {
		return 0, ErrKeyNotInMap
	}
	i, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	return i, nil
}

// IsApparentSQLDateFormat is a rudimentary check that the input appears to be
// in SQL Format by matching a '####-##-## ##:##:##' format. No actual dates
// or times are parsed, so strings like '3928-31-28 88:73:95' would match.
func IsApparentSQLDateFormat(s string) bool {
	if len(s) != 19 {
		return false
	}
	for i := range 19 {
		c := s[i]
		switch i {
		case 4, 7:
			if c != '-' {
				return false
			}
		case 10:
			if c != ' ' {
				return false
			}
		case 13, 16:
			if c != ':' {
				return false
			}
		default:
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// Pare returns a new version of s that excludes values present in exclude
func Pare(s, exclude []string) []string {
	out := make([]string, len(s))
	var k int
outer:
	for _, sa := range s {
		for _, sb := range exclude {
			if sa == sb {
				continue outer
			}
		}
		out[k] = sa
		k++
	}
	return out[:k]
}
