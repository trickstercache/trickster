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

package flux

import (
	"fmt"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	// ConstStatements are any statements that Trickster doesn't care about modifying.
	// This is most statements.
	Const = StatementKind(iota)
	// RangeStatements are the range(...) contained in the query.
	Range
)

type StatementKind int

type Statement interface {
	// Kind() returns the StatementKind of the statement.
	Kind() StatementKind
	// String() returns a string representation of the statement.
	String() string
}

type ConstStatement struct {
	stmt string
}

func (stmt *ConstStatement) Kind() StatementKind { return Const }
func (stmt *ConstStatement) String() string      { return stmt.stmt }

type RangeStatement struct {
	ext timeseries.Extent
}

func (stmt *RangeStatement) Kind() StatementKind { return Range }
func (stmt *RangeStatement) String() string {
	start := stmt.ext.Start.Format(time.RFC3339)
	stop := stmt.ext.End.Format(time.RFC3339)
	return fmt.Sprintf("|> range(start: %s, stop: %s)\n", start, stop)
}

type Query struct {
	stmts  []Statement
	Extent timeseries.Extent
	Step   time.Duration
}

func (q *Query) SetExtent(ext timeseries.Extent) {
	q.Extent = ext
	for _, stmt := range q.stmts {
		if rs, ok := stmt.(*RangeStatement); ok {
			rs.ext = ext
			break
		}
	}
}

func (q *Query) String() string {
	var out string
	for _, stmt := range q.stmts {
		out += stmt.String()
	}
	return out
}
