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
	return fmt.Sprintf("|> range(start: %d, stop: %d)\n", stmt.ext.Start.UnixNano(), stmt.ext.End.UnixNano())
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
