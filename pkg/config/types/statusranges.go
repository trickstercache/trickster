/*
 * Copyright 2026 The Trickster Authors
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
package types

import (
	"errors"
	"fmt"

	"go.yaml.in/yaml/v3"
)

const (
	minStatusCode = 100
	maxStatusCode = 599
)

// ErrInvalidStatusRange is returned for a status code range outside 100-599 or with its
// start above its end.
var ErrInvalidStatusRange = errors.New("invalid status code range")

// StatusRange is an inclusive range of HTTP status codes. In YAML it is either a bare code:
//
//	status_codes: [200, 204]
//
// or a mapping, and a list may mix the two:
//
//	status_codes: [{start: 200, end: 299}, 304]
type StatusRange struct {
	Start int `yaml:"start"`
	End   int `yaml:"end"`
}

// StatusRanges is a list of status code ranges. Ranges may overlap or touch; the list means
// their union.
type StatusRanges []StatusRange

// StatusCodes returns a StatusRanges of single codes.
func StatusCodes(codes ...int) StatusRanges {
	out := make(StatusRanges, len(codes))
	for i, c := range codes {
		out[i] = StatusRange{Start: c, End: c}
	}
	return out
}

// UnmarshalYAML accepts either a bare status code or a {start, end} mapping.
func (r *StatusRange) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var code int
		if err := value.Decode(&code); err != nil {
			return err
		}
		r.Start, r.End = code, code
		return nil
	}
	type loadStatusRange StatusRange
	var lr loadStatusRange
	if err := value.Decode(&lr); err != nil {
		return err
	}
	*r = StatusRange(lr)
	return nil
}

// MarshalYAML renders a single-code range as a bare code, so a list of codes reads back out
// the way it was written.
func (r StatusRange) MarshalYAML() (any, error) {
	if r.Start == r.End {
		return r.Start, nil
	}
	type dumpStatusRange StatusRange
	return dumpStatusRange(r), nil
}

// Validate checks that every range lies within 100-599 and starts no higher than it ends.
func (l StatusRanges) Validate() error {
	for _, r := range l {
		if r.Start < minStatusCode || r.End > maxStatusCode || r.Start > r.End {
			return fmt.Errorf("%w: %d-%d (codes are %d-%d, start first)",
				ErrInvalidStatusRange, r.Start, r.End, minStatusCode, maxStatusCode)
		}
	}
	return nil
}

// StatusTable answers whether a status code is in a set with one array index.
type StatusTable [maxStatusCode + 1]bool

// Compile returns the lookup table of the ranges' union. Anything outside 100-599 is ignored.
func (l StatusRanges) Compile() *StatusTable {
	t := &StatusTable{}
	for _, r := range l {
		for code := max(r.Start, minStatusCode); code <= min(r.End, maxStatusCode); code++ {
			t[code] = true
		}
	}
	return t
}

// Contains reports whether code is in the set; a code outside 0-599 never is.
func (t *StatusTable) Contains(code int) bool {
	return t != nil && code >= 0 && code <= maxStatusCode && t[code]
}
