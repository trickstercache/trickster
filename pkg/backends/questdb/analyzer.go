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

package questdb

import (
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"
)

var questDBVolatileFunctions = map[string]struct{}{
	"sysdate": {}, "systimestamp": {}, "systimestamp_ns": {},
	"rnd_bin": {}, "rnd_boolean": {}, "rnd_byte": {}, "rnd_char": {},
	"rnd_date": {}, "rnd_decimal": {}, "rnd_double": {},
	"rnd_double_array": {}, "rnd_float": {}, "rnd_int": {}, "rnd_ipv4": {},
	"rnd_long": {}, "rnd_long256": {}, "rnd_short": {}, "rnd_str": {},
	"rnd_symbol": {}, "rnd_symbol_weighted": {}, "rnd_symbol_zipf": {},
	"rnd_timestamp": {}, "rnd_timestamp_ns": {}, "rnd_uuid4": {}, "rnd_varchar": {},
}

var analyzer = cockroach.NewAnalyzer(cockroach.Options{
	BucketMatchers: []cockroach.BucketMatcher{timestampFloorMatcher},
	ExprBucketMatchers: []cockroach.ExprBucketMatcher{
		cockroach.EpochFloorMatcher,
	},
	ClauseRewriters: []cockroach.ClauseRewriter{sampleByRewriter{}},
	NakedIntIsInt4:  true,
	IsVolatileFunction: func(name string) bool {
		_, ok := questDBVolatileFunctions[strings.ToLower(name)]
		return ok
	},
	// QuestDB's PostgreSQL wire protocol renders TIMESTAMP with six digits
	// of fractional precision. Inclusive upper bounds must round at that
	// precision when an extent is rendered.
	BoundPrecision: time.Microsecond,
})

var _ sqlanalyzer.DialectAnalyzer = analyzer

func timestampFloorMatcher(name string, args []tree.Expr) (cockroach.BucketMatch, bool) {
	if name != "timestamp_floor" || len(args) != 2 {
		return cockroach.BucketMatch{}, false
	}
	literal, ok := args[0].(*tree.StrVal)
	if !ok {
		return cockroach.BucketMatch{}, false
	}
	step, ok := parseSampleInterval(literal.RawString())
	if !ok {
		return cockroach.BucketMatch{}, false
	}
	column, ok := cockroach.ColumnName(args[1])
	if !ok {
		return cockroach.BucketMatch{}, false
	}
	return cockroach.BucketMatch{
		TimeColumn:   column,
		Step:         step,
		OutputColumn: "timestamp_floor",
	}, true
}
