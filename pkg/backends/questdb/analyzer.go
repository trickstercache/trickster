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

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser"
	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/pgfamily"
)

var questDBVolatileFunctions = map[string]struct{}{
	"sysdate": {}, "systimestamp": {}, "systimestamp_ns": {}, "now_ns": {},
	"today": {}, "tomorrow": {}, "yesterday": {}, "timestamp_shuffle": {},
}

var analyzer = pgfamily.NewAnalyzer(pgfamily.Options{
	BucketMatchers: []pgfamily.BucketMatcher{timestampFloorMatcher},
	ExprBucketMatchers: []pgfamily.ExprBucketMatcher{
		pgfamily.EpochFloorMatcher,
	},
	// SAMPLE BY steps are duration literals such as 1h, which the base grammar cannot scan
	Syntax:         pgparser.DialectSpec{Clauses: []string{"SAMPLE BY"}, DurationLiterals: true},
	ClauseMatchers: []pgfamily.ClauseMatcher{sampleByMatcher},
	NakedIntIsInt4: true,
	IsVolatileFunction: func(name string) bool {
		_, ok := questDBVolatileFunctions[name]
		return ok || strings.HasPrefix(name, "rnd_")
	},
	// QuestDB's PostgreSQL wire protocol renders TIMESTAMP with six digits
	// of fractional precision. Inclusive upper bounds must round at that
	// precision when an extent is rendered.
	BoundPrecision: time.Microsecond,
})

var _ sqlanalyzer.DialectAnalyzer = analyzer

func timestampFloorMatcher(name string, args []ast.Expr) (pgfamily.BucketMatch, bool) {
	if name != "timestamp_floor" || len(args) != 2 {
		return pgfamily.BucketMatch{}, false
	}
	literal, ok := args[0].(*ast.StrVal)
	if !ok {
		return pgfamily.BucketMatch{}, false
	}
	step, ok := parseSampleInterval(literal.RawString())
	if !ok {
		return pgfamily.BucketMatch{}, false
	}
	column, ok := pgfamily.ColumnName(args[1])
	if !ok {
		return pgfamily.BucketMatch{}, false
	}
	return pgfamily.BucketMatch{
		TimeColumn:   column,
		Step:         step,
		OutputColumn: "timestamp_floor",
	}, true
}
