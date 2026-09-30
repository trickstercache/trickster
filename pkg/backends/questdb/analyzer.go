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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"
)

var analyzer = cockroach.NewAnalyzer(cockroach.Options{
	BucketMatchers: []cockroach.BucketMatcher{timestampFloorMatcher},
	ExprBucketMatchers: []cockroach.ExprBucketMatcher{
		cockroach.EpochFloorMatcher,
	},
	ClauseRewriters: []cockroach.ClauseRewriter{sampleByRewriter{}},
	NakedIntIsInt4:  true,
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
