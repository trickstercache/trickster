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

// IndexInSlice returns the index of a string element in a given slice
func IndexInSlice(arr []string, val string) int {
	for i, v := range arr {
		if v == val {
			return i
		}
	}
	return -1
}

// CloneBoolMap returns an exact copy of a map consisting string key and bool value
func CloneBoolMap(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Equal returns true if the slices contain identical values in the identical order
func Equal(s1, s2 []string) bool {
	if s1 == nil && s2 == nil {
		return true
	}
	if s1 == nil || s2 == nil || len(s1) != len(s2) {
		return false
	}
	for i, v := range s1 {
		if v != s2[i] {
			return false
		}
	}
	return true
}

// Unique returns a uniqueified version of the list
func Unique(in []string) []string {
	l := len(in)
	if l == 0 {
		return in
	}
	m := make(map[string]interface{})
	out := make([]string, 0, l)
	for _, v := range in {
		if _, ok := m[v]; ok {
			continue
		}
		out = append(out, v)
		m[v] = nil
	}
	return out
}

// ErrKeyNotInMap represents an error for key not found in map
var ErrKeyNotInMap = errors.New("key not found in map")

// StringMap represents a map[string]string
type StringMap map[string]string

// Lookup represents a map[string]interface{} with assumed nil values
type Lookup map[string]interface{}

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
