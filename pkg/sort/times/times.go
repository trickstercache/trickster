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

// Package times provides sorting capabilities to a slice of type time
package times

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Times represents a slice of time.Time
type Times []time.Time

// FromMap returns a times.Times from a map of time.Time
func FromMap(m map[time.Time]bool) Times {

	l := make(Times, 0, len(m))
	for t := range m {
		l = append(l, t)
	}
	sort.Sort(l)
	return l
}

// Len returns the length of a slice of time.Times
func (t Times) Len() int {
	return len(t)
}

// Less returns true if i comes before j
func (t Times) Less(i, j int) bool {
	return t[i].Before(t[j])
}

// Swap modifies a slice of time.Times by swapping the values in indexes i and j
func (t Times) Swap(i, j int) {
	t[i], t[j] = t[j], t[i]
}

func (t Times) String() string {
	l := make([]string, 0, len(t))
	for _, v := range t {
		l = append(l, strconv.FormatInt(v.Unix(), 10))
	}
	return "[ " + strings.Join(l, ", ") + " ]"
}
