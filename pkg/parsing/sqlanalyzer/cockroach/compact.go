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

package cockroach

import (
	"time"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"
)

// ParseCompactDuration reads positive integer/unit pairs without allocating.
// Units are case-sensitive and dialect-supplied; signs, fractions, whitespace,
// zero components and overflow are rejected. Only fixed-length units belong in
// the table, which must remain immutable while the parser is in use.
func ParseCompactDuration(s string, units map[string]time.Duration) (time.Duration, bool) {
	var total time.Duration
	for at := 0; at < len(s); {
		start := at
		var n int64
		for at < len(s) && s[at] >= '0' && s[at] <= '9' {
			digit := int64(s[at] - '0')
			if n > ((1<<63-1)-digit)/10 {
				return 0, false
			}
			n = n*10 + digit
			at++
		}
		if start == at || n == 0 {
			return 0, false
		}
		start = at
		for at < len(s) && (s[at] < '0' || s[at] > '9') {
			at++
		}
		unit, ok := units[s[start:at]]
		if !ok || unit <= 0 || n > int64((1<<63-1-total)/unit) {
			return 0, false
		}
		total += time.Duration(n) * unit
	}
	return total, total > 0
}

// CompactDateBinMatcher recognizes date_bin('5m', column [, origin]) using a
// dialect's fixed-length compact units. INTERVAL syntax remains a separate matcher.
func CompactDateBinMatcher(units map[string]time.Duration) BucketMatcher {
	return func(name string, args []tree.Expr) (BucketMatch, bool) {
		if name != "date_bin" || len(args) < 2 || len(args) > 3 {
			return BucketMatch{}, false
		}
		literal, ok := args[0].(*tree.StrVal)
		if !ok {
			return BucketMatch{}, false
		}
		step, ok := ParseCompactDuration(literal.RawString(), units)
		if !ok {
			return BucketMatch{}, false
		}
		return dateBinMatch(step, args)
	}
}
