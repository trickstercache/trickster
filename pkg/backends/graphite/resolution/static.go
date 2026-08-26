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

package resolution

import (
	"fmt"
	"regexp"
)

// StaticRule is one static_retentions entry
type StaticRule struct {
	Pattern   *regexp.Regexp
	Ladder    *Ladder
	Source    string // the retentions text, for logging
	PatternSt string
}

// Static is the storage-schemas.conf-shaped static layer: ordered rules, first
// match wins (re.search semantics); a match is Configured until probe-confirmed.
type Static struct {
	rules []StaticRule
}

// NewStatic compiles rules from (pattern, retentions) pairs
func NewStatic(rules [][2]string) (*Static, error) {
	s := &Static{}
	for i, r := range rules {
		re, err := regexp.Compile(r[0])
		if err != nil {
			return nil, fmt.Errorf("static_retentions[%d]: pattern: %w", i, err)
		}
		l, err := ParseRetentions(r[1])
		if err != nil {
			return nil, fmt.Errorf("static_retentions[%d]: %w", i, err)
		}
		s.rules = append(s.rules, StaticRule{Pattern: re, Ladder: l, Source: r[1], PatternSt: r[0]})
	}
	return s, nil
}

// Match returns the ladder of the first rule matching a leaf path
func (s *Static) Match(leaf string) (*Ladder, bool) {
	if s == nil {
		return nil, false
	}
	for i := range s.rules {
		if s.rules[i].Pattern.MatchString(leaf) {
			return s.rules[i].Ladder, true
		}
	}
	return nil, false
}

// Len is the number of rules
func (s *Static) Len() int {
	if s == nil {
		return 0
	}
	return len(s.rules)
}
