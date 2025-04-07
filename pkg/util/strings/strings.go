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
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

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

// StringMap represents a map[string]string
type StringMap map[string]string

// Lookup represents a map[string]any with assumed nil values
type Lookup map[string]any

func (m StringMap) String() string {
	delimiter := ""
	sb := &strings.Builder{}
	sb.WriteString("{")
	for k, v := range m {
		sb.WriteString(fmt.Sprintf(`%s"%s":"%s"`, delimiter, k, v))
		delimiter = ", "
	}
	sb.WriteString("}")
	return sb.String()
}

// GetInt returns an integer value from the map, if convertible
// If not, an error is returned with a value of 0
func (m StringMap) GetInt(key string) (int, error) {
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
