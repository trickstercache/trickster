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

package sqlanalyzertest

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
)

func TestDropParity(t *testing.T) {
	now := time.Unix(100_000, 0)
	at := func(v int64) *sqlanalyzer.Bound { return &sqlanalyzer.Bound{Value: time.Unix(v, 0), Inclusive: true} }
	exclusive := func(v int64) *sqlanalyzer.Bound { return &sqlanalyzer.Bound{Value: time.Unix(v, 0)} }
	plan := func(rawLower, rawUpper, lower, upper *sqlanalyzer.Bound) *sqlanalyzer.QueryPlan {
		return &sqlanalyzer.QueryPlan{
			Step: time.Minute, RawLower: rawLower, RawUpper: rawUpper, LowerBound: lower, UpperBound: upper,
		}
	}
	tests := []struct {
		name     string
		plan     *sqlanalyzer.QueryPlan
		compared bool
		fails    bool
	}{
		{"no plan", nil, false, true},
		{"no raw bounds", plan(nil, nil, at(60), exclusive(600)), false, true},
		{"agreeing inward rounding", plan(at(30), exclusive(630), at(60), exclusive(600)), true, false},
		{"a rounded extent one bucket wider", plan(at(30), exclusive(630), at(0), exclusive(600)), true, true},
		{"no complete bucket on either side", plan(at(10), exclusive(50), at(60), exclusive(0)), true, false},
		{"complete buckets on one side only", plan(at(10), exclusive(50), at(0), exclusive(60)), true, true},
		{"open ended", plan(at(30), nil, at(60), nil), false, false},
		{"reaching past now", plan(at(30), exclusive(200_000), at(60), exclusive(199_980)), false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compared, err := DropParity(test.plan, now)
			if compared != test.compared || (err != nil) != test.fails {
				t.Errorf("got compared %t, err %v", compared, err)
			}
		})
	}
}
