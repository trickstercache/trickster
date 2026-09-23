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

// Package cockroach adapts the CockroachDB SQL parser to Trickster's
// dialect-independent sqlanalyzer contract. CockroachDB's grammar is a close
// superset of the PostgreSQL-flavored SQL dialects spoken by engines that have
// no native Go parser — InfluxDB 3 (Apache DataFusion) and Apache Druid among
// them — so one adapter serves multiple backend providers. Dialect-specific
// time-bucketing functions are supplied per backend via Options.BucketMatchers;
// everything else (statement gating, range analysis, canonicalization, and
// extent rendering) is shared.
package cockroach

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/parser"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/parser/statements"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree/treebin"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree/treecmp"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/types"
)

var (
	// ErrInvalidSQL indicates the statement could not be parsed.
	ErrInvalidSQL = errors.New("invalid SQL")
	// ErrUnsupportedStatement indicates a statement that cannot be cached safely.
	ErrUnsupportedStatement = errors.New("unsupported SQL statement")
	// ErrNotTimeRangeQuery indicates a SELECT without a supported time range.
	ErrNotTimeRangeQuery = errors.New("not a time-range query")
	// ErrUnsupportedBucket indicates an unknown or ambiguous bucket expression.
	ErrUnsupportedBucket = errors.New("unsupported time bucket")
	// ErrUnsafePredicate indicates range semantics that could cache partial buckets.
	ErrUnsafePredicate = errors.New("unsafe time predicate")
	// ErrAmbiguousTimeAxis indicates more than one complete time range in a query.
	ErrAmbiguousTimeAxis = errors.New("ambiguous time axis")
	// ErrInvalidGroupByClause indicates a GROUP BY layout that DPC cannot preserve.
	ErrInvalidGroupByClause = errors.New("unsupported GROUP BY clause")
	// ErrUnsupportedLimit indicates a LIMIT/OFFSET clause, which delta caching
	// cannot preserve.
	ErrUnsupportedLimit = errors.New("unsupported LIMIT clause")
	// ErrNoLowerBound indicates a time-range query without a lower time bound.
	ErrNoLowerBound = errors.New("no lower time bound")
	// ErrUnsupportedOrdering indicates an ORDER BY the delta tier cannot
	// reproduce when rebuilding a response from merged cache parts.
	ErrUnsupportedOrdering = errors.New("unsupported ORDER BY clause")
)

// BucketMatch describes a recognized time-bucketing expression.
type BucketMatch struct {
	// TimeColumn is the source time column the bucket function operates on.
	TimeColumn string
	// Step is the bucket cadence.
	Step time.Duration
	// Phase is the bucket alignment offset from the Unix epoch.
	Phase time.Duration
	// OutputUnit is the unit of the bucket's output values; zero means timestamps.
	OutputUnit timeseries.FieldDataType
	// ColumnUnit, when set, says the time column holds epoch counts of this
	// unit; bounds written in any other form fail closed.
	ColumnUnit timeseries.FieldDataType
	// OutputColumn names the result column of an unaliased bucket, for engines
	// that do not name it after the expression text.
	OutputColumn string
}

// BucketMatcher inspects a lowercase function name and its arguments and
// reports the bucket cadence and time column when the call is a dialect
// time-bucketing function.
type BucketMatcher func(name string, args []tree.Expr) (BucketMatch, bool)

// ExprBucketMatcher recognizes a bucket written as an arbitrary select-list
// expression, such as epoch arithmetic, rather than as one function call.
type ExprBucketMatcher func(expr tree.Expr) (BucketMatch, bool)

// LiftedClause describes a dialect clause a ClauseRewriter took out of a statement.
type LiftedClause struct {
	// Payload is defined by the rewriter and handed to ClauseBucketMatchers.
	Payload any
	// Bucket declares the time bucket when the clause itself defines it and no
	// select-list function does. Its TimeColumn must appear in the select list.
	Bucket *BucketMatch
	// ImplicitGrouping marks a clause that groups by every plain select-list
	// column without a GROUP BY, as a sampling clause does.
	ImplicitGrouping bool
}

// ClauseRewriter adapts a dialect clause that the CockroachDB grammar cannot
// parse. Lift rewrites the statement into SQL the parser accepts and returns
// what it took out, or a nil clause when the statement has none. Restore
// reverses Lift on SQL rendered from the rewritten statement, which may carry
// time-bound placeholders. Implementations must ignore quoted and commented
// text; package sqlscan finds token boundaries for that purpose.
type ClauseRewriter interface {
	Lift(statement string) (rewritten string, lifted *LiftedClause, err error)
	Restore(rendered string, lifted *LiftedClause) (string, error)
}

// ClauseBucketMatcher is a BucketMatcher that also sees the statement's lifted clauses.
type ClauseBucketMatcher func(name string, args []tree.Expr, clauses []*LiftedClause) (BucketMatch, bool)

// Options configures an Analyzer for a specific backend dialect.
type Options struct {
	// BucketMatchers describe the dialect's time-bucketing functions.
	BucketMatchers []BucketMatcher
	// ExprBucketMatchers run on select-list items no BucketMatcher recognized.
	ExprBucketMatchers []ExprBucketMatcher
	// ClauseRewriters run in order before parsing and are reversed, last first,
	// on the canonical SQL and on every rendered extent query.
	ClauseRewriters []ClauseRewriter
	// ClauseBucketMatchers describe bucketing functions whose cadence or
	// alignment comes from a lifted clause rather than from their arguments.
	ClauseBucketMatchers []ClauseBucketMatcher
	// RenderNumericBoundsAsRFC3339 renders time bounds that were written as
	// bare epoch integers back as quoted RFC3339 literals. Engines with strict
	// type coercion, such as Apache DataFusion (InfluxDB 3), reject
	// Timestamp-to-Int64 comparisons but coerce string literals to timestamps.
	// Bounds written as string or TIMESTAMP literals are unaffected.
	RenderNumericBoundsAsRFC3339 bool
	// RoundUnalignedTimeBounds accepts raw-time-column predicates that are not
	// aligned to the bucket cadence by rounding the lower bound up and the
	// exclusive upper bound down to the cadence, per the sqlanalyzer contract's
	// unaligned-bound provision. Partial edge buckets are dropped rather than
	// cached. Dashboard clients such as Grafana emit live, unaligned ranges;
	// without this option those queries fail closed to the object cache.
	RoundUnalignedTimeBounds bool
	// NakedIntIsInt4 parses the bare INT and INTEGER type names as 4-byte
	// integers, as PostgreSQL defines them, instead of the parser's 8-byte default.
	NakedIntIsInt4 bool
	// BoundPrecision is the finest time resolution the engine stores. An inclusive
	// upper bound renders one such tick below the boundary; zero means 1ns.
	BoundPrecision time.Duration
	// RejectZonelessBounds fails closed on time bounds written without a zone,
	// for sessions where the engine would not read them as UTC.
	RejectZonelessBounds bool
	// PostRender re-spells what the parser's formatter gets wrong for the engine, in the
	// canonical SQL and the extent template. The SQL may carry time-bound placeholders.
	PostRender func(rendered string) (string, error)
}

// Analyzer converts CockroachDB-parsed SQL into Trickster's dialect-independent
// cache plan. It contains no mutable per-query state and is safe for
// concurrent use.
type Analyzer struct {
	opts Options
}

var _ sqlanalyzer.DialectAnalyzer = (*Analyzer)(nil)

// NewAnalyzer returns an Analyzer configured with the provided Options.
func NewAnalyzer(opts Options) *Analyzer {
	return &Analyzer{opts: opts}
}

const day = 24 * time.Hour

var intervalUnits = map[string]time.Duration{
	"nanosecond": time.Nanosecond, "nanoseconds": time.Nanosecond,
	"microsecond": time.Microsecond, "microseconds": time.Microsecond,
	"millisecond": time.Millisecond, "milliseconds": time.Millisecond,
	"second": time.Second, "seconds": time.Second,
	"minute": time.Minute, "minutes": time.Minute,
	"hour": time.Hour, "hours": time.Hour,
	"day": day, "days": day,
	"week": 7 * day, "weeks": 7 * day,
}

// truncUnits maps date_trunc-style unit strings to fixed durations. Units with
// variable length (month, quarter, year) are omitted; queries bucketed on them
// fail closed to the object cache.
var truncUnits = map[string]time.Duration{
	"second": time.Second,
	"minute": time.Minute,
	"hour":   time.Hour,
	"day":    day,
	"week":   7 * day,
}

// ParseIntervalDuration converts an SQL interval literal body such as
// "1 hour" or "5 minutes" into a fixed duration. Variable-length units are
// rejected.
func ParseIntervalDuration(s string) (time.Duration, bool) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(s)))
	if len(fields) == 0 || len(fields)%2 != 0 {
		return 0, false
	}
	var total time.Duration
	for i := 0; i < len(fields); i += 2 {
		n, err := strconv.ParseInt(fields[i], 10, 64)
		if err != nil || n <= 0 {
			return 0, false
		}
		unit, ok := intervalUnits[fields[i+1]]
		if !ok || n > int64((1<<63-1-total)/unit) {
			return 0, false
		}
		total += time.Duration(n) * unit
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}

// ColumnName returns the bare column name of a simple column reference.
func ColumnName(expr tree.Expr) (string, bool) {
	name, ok := expr.(*tree.UnresolvedName)
	if !ok || name.Star || name.NumParts < 1 {
		return "", false
	}
	return name.Parts[0], true
}

// intervalDuration extracts a fixed duration from an INTERVAL expression.
func intervalDuration(expr tree.Expr) (time.Duration, bool) {
	cast, ok := expr.(*tree.CastExpr)
	if !ok {
		return 0, false
	}
	castType, ok := tree.GetStaticallyKnownType(cast.Type)
	if !ok || castType.Family() != types.IntervalFamily {
		return 0, false
	}
	literal, ok := cast.Expr.(*tree.StrVal)
	if !ok {
		return 0, false
	}
	return ParseIntervalDuration(literal.RawString())
}

// timestampLiteral extracts a concrete time from a TIMESTAMP '...' style cast
// or a bare SQL datetime string literal.
func timestampLiteral(expr tree.Expr) (time.Time, bool) {
	switch value := expr.(type) {
	case *tree.StrVal:
		parsed, _, ok := parseSQLTime(value.RawString())
		return parsed, ok
	case *tree.CastExpr:
		castType, ok := tree.GetStaticallyKnownType(value.Type)
		if !ok {
			return time.Time{}, false
		}
		switch castType.Family() {
		case types.TimestampFamily, types.TimestampTZFamily, types.DateFamily:
			if literal, ok := value.Expr.(*tree.StrVal); ok {
				parsed, _, ok := parseSQLTime(literal.RawString())
				return parsed, ok
			}
		}
	}
	return time.Time{}, false
}

// DateBinMatcher matches DataFusion's date_bin(INTERVAL, column[, origin]).
func DateBinMatcher(name string, args []tree.Expr) (BucketMatch, bool) {
	if name != "date_bin" || len(args) < 2 || len(args) > 3 {
		return BucketMatch{}, false
	}
	step, ok := intervalDuration(args[0])
	if !ok {
		return BucketMatch{}, false
	}
	return dateBinMatch(step, args)
}

func dateBinMatch(step time.Duration, args []tree.Expr) (BucketMatch, bool) {
	column, ok := ColumnName(args[1])
	if !ok {
		return BucketMatch{}, false
	}
	var phase time.Duration
	if len(args) == 3 {
		origin, ok := timestampLiteral(args[2])
		if !ok {
			return BucketMatch{}, false
		}
		phase = time.Duration(origin.UnixNano() % step.Nanoseconds())
		if phase < 0 {
			phase += step
		}
	}
	return BucketMatch{TimeColumn: column, Step: step, Phase: phase}, true
}

// DateTruncMatcher matches DataFusion's date_trunc('unit', column).
func DateTruncMatcher(name string, args []tree.Expr) (BucketMatch, bool) {
	if name != "date_trunc" || len(args) != 2 {
		return BucketMatch{}, false
	}
	literal, ok := args[0].(*tree.StrVal)
	if !ok {
		return BucketMatch{}, false
	}
	step, ok := truncUnits[strings.ToLower(literal.RawString())]
	if !ok {
		return BucketMatch{}, false
	}
	column, ok := ColumnName(args[1])
	if !ok {
		return BucketMatch{}, false
	}
	var phase time.Duration
	if step == 7*day {
		// date_trunc('week') anchors on Monday; the epoch was a Thursday.
		phase = 4 * day
	}
	return BucketMatch{TimeColumn: column, Step: step, Phase: phase}, true
}

// DataFusionBucketMatchers returns the bucket matchers for Apache
// DataFusion-based dialects such as InfluxDB 3 SQL.
func DataFusionBucketMatchers() []BucketMatcher {
	return []BucketMatcher{DateBinMatcher, DateTruncMatcher}
}

// Analyze implements sqlanalyzer.DialectAnalyzer.
func (a *Analyzer) Analyze(statement string, now time.Time) sqlanalyzer.Analysis {
	if strings.TrimSpace(statement) == "" {
		return sqlanalyzer.Analysis{Reason: sqlanalyzer.ReasonInvalidSQL, Err: ErrInvalidSQL}
	}
	original := statement
	statement, lifted, err := a.liftClauses(statement)
	if err != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedBucket, err)
	}
	parsed, err := a.parse(statement)
	if err != nil {
		mode := sqlanalyzer.CacheModeNone
		if leadingKeywordIsSelect(original) {
			mode = sqlanalyzer.CacheModeObject
		}
		return sqlanalyzer.Analysis{
			Mode: mode, Reason: sqlanalyzer.ReasonInvalidSQL,
			Err: fmt.Errorf("%w: %w", ErrInvalidSQL, err),
		}
	}
	selectStmt, ok := parsed.AST.(*tree.Select)
	if !ok {
		return sqlanalyzer.Analysis{
			Mode:   sqlanalyzer.CacheModeNone,
			Reason: sqlanalyzer.ReasonUnsupportedStatement, Err: ErrUnsupportedStatement,
		}
	}
	clause, ok := selectStmt.Select.(*tree.SelectClause)
	if !ok || len(selectStmt.Locking) > 0 {
		if _, compound := selectStmt.Select.(*tree.UnionClause); compound &&
			len(selectStmt.Locking) == 0 {
			return sqlanalyzer.ObjectAnalysis(
				sqlanalyzer.ReasonUnsupportedFormat, ErrUnsupportedStatement)
		}
		return sqlanalyzer.Analysis{
			Mode:   sqlanalyzer.CacheModeNone,
			Reason: sqlanalyzer.ReasonUnsupportedStatement, Err: ErrUnsupportedStatement,
		}
	}
	if selectStmt.Limit != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedLimit, ErrUnsupportedLimit)
	}
	if selectStmt.With != nil || clause.Distinct || len(clause.DistinctOn) > 0 ||
		clause.Having != nil || len(clause.Window) > 0 || containsSubquery(clause) {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedFormat, ErrUnsupportedStatement)
	}
	// Joins and multi-table selects can carry time predicates in ON clauses
	// this analyzer never inspects, and inline window frames span rows across
	// bucket boundaries; per-extent delta computation would return wrong
	// values for either, so both fail closed to the object cache.
	if !singleTableFrom(clause) || containsWindowFunction(clause.Exprs) {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedFormat, ErrUnsupportedStatement)
	}
	if containsVolatileFunction(clause.Exprs) {
		return sqlanalyzer.Analysis{
			Mode:   sqlanalyzer.CacheModeNone,
			Reason: sqlanalyzer.ReasonNondeterministic, Err: ErrUnsupportedStatement,
		}
	}

	bucket, bucketIndex, err := a.analyzeSelectList(clause.Exprs, lifted)
	if err != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedBucket, err)
	}
	groups, err := analyzeGroupBy(clause.GroupBy, clause.Exprs, bucket, bucketIndex)
	if err != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedGrouping, err)
	}
	ordering, err := analyzeOrderBy(selectStmt.OrderBy, clause.Exprs)
	if err != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedOrdering, err)
	}
	ranges, err := a.analyzeRanges(clause, bucket, now)
	if err != nil {
		reason := sqlanalyzer.ReasonNotTimeRange
		if errors.Is(err, ErrUnsafePredicate) {
			reason = sqlanalyzer.ReasonUnsafePredicate
		} else if errors.Is(err, ErrAmbiguousTimeAxis) {
			reason = sqlanalyzer.ReasonAmbiguousTimeAxis
		}
		return sqlanalyzer.ObjectAnalysis(reason, err)
	}

	canonical, renderer := buildQueryArtifacts(selectStmt, clause, ranges, bucket,
		a.opts.RenderNumericBoundsAsRFC3339)
	if canonical, err = a.finishRender(canonical, lifted); err == nil {
		renderer.template, err = a.finishRender(renderer.template, lifted)
	}
	if err != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedFormat, err)
	}
	plan := &sqlanalyzer.QueryPlan{
		CanonicalSQL: canonical,
		TimeColumn:   bucket.timeColumn,
		OutputColumn: bucket.outputColumn,
		Step:         bucket.step,
		Phase:        bucket.phase,
		OutputUnit:   bucket.outputUnit,
		InputUnit:    inputTypeForBound(ranges.lower.style),
		LowerBound: &sqlanalyzer.Bound{
			Value: ranges.lower.value, Inclusive: ranges.lower.inclusive,
		},
		GroupColumns:        groups,
		DropsPartialBuckets: ranges.dropsPartialBuckets,
		Ordering:            ordering,
		Renderer:            renderer,
	}
	if ranges.upper != nil {
		plan.UpperBound = &sqlanalyzer.Bound{
			Value: ranges.upper.value, Inclusive: ranges.upper.inclusive,
		}
	}
	return sqlanalyzer.Analysis{
		Mode: sqlanalyzer.CacheModeDelta, Reason: sqlanalyzer.ReasonDeltaCacheable, Plan: plan,
	}
}

func (a *Analyzer) parse(statement string) (statements.Statement[tree.Statement], error) {
	if a.opts.NakedIntIsInt4 {
		return parser.ParseOneWithInt(statement, types.Int4)
	}
	return parser.ParseOne(statement)
}

func (a *Analyzer) finishRender(rendered string, lifted []liftedClause) (string, error) {
	rendered, err := a.restoreClauses(rendered, lifted)
	if err != nil || a.opts.PostRender == nil {
		return rendered, err
	}
	if rendered, err = a.opts.PostRender(rendered); err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnsupportedStatement, err)
	}
	return rendered, nil
}

// liftedClause pairs a lifted clause with the rewriter that can restore it.
type liftedClause struct {
	rewriter ClauseRewriter
	clause   *LiftedClause
}

func (a *Analyzer) liftClauses(statement string) (string, []liftedClause, error) {
	var lifted []liftedClause
	for _, rewriter := range a.opts.ClauseRewriters {
		rewritten, clause, err := rewriter.Lift(statement)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %w", ErrUnsupportedBucket, err)
		}
		if clause != nil {
			statement = rewritten
			lifted = append(lifted, liftedClause{rewriter: rewriter, clause: clause})
		}
	}
	return statement, lifted, nil
}

func (a *Analyzer) restoreClauses(rendered string, lifted []liftedClause) (string, error) {
	var err error
	for _, l := range slices.Backward(lifted) {
		if rendered, err = l.rewriter.Restore(rendered, l.clause); err != nil {
			return "", fmt.Errorf("%w: %w", ErrUnsupportedStatement, err)
		}
	}
	return rendered, nil
}

// leadingKeywordIsSelect reports whether an unparsable statement still begins
// with a SELECT, keeping it eligible for the object proxy cache.
func leadingKeywordIsSelect(statement string) bool {
	fields := strings.Fields(strings.ToLower(statement))
	return len(fields) > 0 && fields[0] == "select"
}

func containsSubquery(clause *tree.SelectClause) bool {
	found := false
	visit := func(expr tree.Expr) {
		if expr == nil || found {
			return
		}
		walkExprTree(expr, func(node tree.Expr) bool {
			if _, ok := node.(*tree.Subquery); ok {
				found = true
			}
			return !found
		})
	}
	for _, item := range clause.Exprs {
		visit(item.Expr)
	}
	if clause.Where != nil {
		visit(clause.Where.Expr)
	}
	for _, expr := range clause.GroupBy {
		visit(expr)
	}
	for _, table := range clause.From.Tables {
		if aliased, ok := table.(*tree.AliasedTableExpr); ok {
			if _, sub := aliased.Expr.(*tree.Subquery); sub {
				found = true
			}
		}
	}
	return found
}

// singleTableFrom reports whether the select reads from exactly one plain
// table. Joins (including parenthesized ones) and comma-joined table lists
// fail the check; FROM-subqueries are separately rejected by containsSubquery.
func singleTableFrom(clause *tree.SelectClause) bool {
	if len(clause.From.Tables) != 1 {
		return false
	}
	aliased, ok := clause.From.Tables[0].(*tree.AliasedTableExpr)
	if !ok {
		return false
	}
	_, ok = aliased.Expr.(*tree.TableName)
	return ok
}

// containsWindowFunction reports whether any select expression carries an
// inline OVER (...) window; the explicit WINDOW clause is checked separately.
func containsWindowFunction(items tree.SelectExprs) bool {
	windowed := false
	for _, item := range items {
		if item.Expr == nil || windowed {
			continue
		}
		walkExprTree(item.Expr, func(node tree.Expr) bool {
			if function, ok := node.(*tree.FuncExpr); ok && function.WindowDef != nil {
				windowed = true
			}
			return !windowed
		})
	}
	return windowed
}

var volatileFunctions = map[string]struct{}{
	"now": {}, "current_timestamp": {}, "current_date": {}, "current_time": {},
	"random": {}, "gen_random_uuid": {}, "uuid_generate_v4": {},
}

// containsVolatileFunction reports whether the select list references a
// nondeterministic function. Volatile functions remain acceptable inside WHERE
// time bounds, where analysis resolves them to concrete times.
func containsVolatileFunction(items tree.SelectExprs) bool {
	volatile := false
	for _, item := range items {
		if item.Expr == nil || volatile {
			continue
		}
		walkExprTree(item.Expr, func(node tree.Expr) bool {
			if function, ok := node.(*tree.FuncExpr); ok {
				name := strings.ToLower(function.Func.String())
				if _, unsafe := volatileFunctions[name]; unsafe {
					volatile = true
				}
			}
			return !volatile
		})
	}
	return volatile
}

// walkExprTree walks an expression tree without copying, calling visit for
// each node until visit returns false.
type walkVisitor struct {
	visit func(tree.Expr) bool
}

func (v *walkVisitor) VisitPre(expr tree.Expr) (bool, tree.Expr) {
	return v.visit(expr), expr
}

func (v *walkVisitor) VisitPost(expr tree.Expr) tree.Expr { return expr }

func walkExprTree(expr tree.Expr, visit func(tree.Expr) bool) {
	_, _ = tree.WalkExpr(&walkVisitor{visit: visit}, expr)
}

type bucketSpec struct {
	timeColumn   string
	outputColumn string
	step         time.Duration
	phase        time.Duration
	outputUnit   timeseries.FieldDataType
	columnUnit   timeseries.FieldDataType
	// implicitGrouping is set when a lifted clause, not a GROUP BY, groups the rows.
	implicitGrouping bool
}

// matchBucket tries the plain matchers, then the ones that read lifted clauses.
func (a *Analyzer) matchBucket(name string, args []tree.Expr, clauses []*LiftedClause) (BucketMatch, bool) {
	for _, matcher := range a.opts.BucketMatchers {
		if match, ok := matcher(name, args); ok {
			return match, true
		}
	}
	for _, matcher := range a.opts.ClauseBucketMatchers {
		if match, ok := matcher(name, args, clauses); ok {
			return match, true
		}
	}
	return BucketMatch{}, false
}

func (a *Analyzer) matchItem(expr tree.Expr, clauses []*LiftedClause) (BucketMatch, bool) {
	if function, ok := expr.(*tree.FuncExpr); ok {
		name := strings.ToLower(function.Func.String())
		if match, ok := a.matchBucket(name, function.Exprs, clauses); ok {
			return match, true
		}
	}
	for _, matcher := range a.opts.ExprBucketMatchers {
		if match, ok := matcher(expr); ok {
			return match, true
		}
	}
	return BucketMatch{}, false
}

func outputUnit(unit timeseries.FieldDataType) timeseries.FieldDataType {
	if unit == 0 {
		return timeseries.DateTimeRFC3339Nano
	}
	return unit
}

// clauseBucket finds the select-list column a lifted clause buckets by.
func clauseBucket(items tree.SelectExprs, clauses []*LiftedClause) (bucketSpec, int, error) {
	var found *bucketSpec
	foundIndex := -1
	for _, clause := range clauses {
		if clause.Bucket == nil {
			continue
		}
		if found != nil {
			return bucketSpec{}, -1, ErrAmbiguousTimeAxis
		}
		for i, item := range items {
			name, ok := ColumnName(item.Expr)
			if !ok || name != clause.Bucket.TimeColumn {
				continue
			}
			if found != nil {
				return bucketSpec{}, -1, ErrAmbiguousTimeAxis
			}
			found = &bucketSpec{
				timeColumn: name, outputColumn: name, step: clause.Bucket.Step,
				phase: clause.Bucket.Phase, implicitGrouping: clause.ImplicitGrouping,
				outputUnit: outputUnit(clause.Bucket.OutputUnit), columnUnit: clause.Bucket.ColumnUnit,
			}
			if item.As != "" {
				found.outputColumn = string(item.As)
			}
			foundIndex = i
		}
	}
	if found == nil || found.step <= 0 {
		return bucketSpec{}, -1, ErrUnsupportedBucket
	}
	return *found, foundIndex, nil
}

func (a *Analyzer) analyzeSelectList(items tree.SelectExprs, lifted []liftedClause) (bucketSpec, int, error) {
	clauses := make([]*LiftedClause, len(lifted))
	for i := range lifted {
		clauses[i] = lifted[i].clause
	}
	var found *bucketSpec
	foundIndex := -1
	for i, item := range items {
		match, ok := a.matchItem(item.Expr, clauses)
		if !ok {
			continue
		}
		if found != nil {
			return bucketSpec{}, -1, ErrAmbiguousTimeAxis
		}
		bucket := bucketSpec{
			timeColumn: match.TimeColumn, step: match.Step, phase: match.Phase,
			outputUnit: outputUnit(match.OutputUnit), columnUnit: match.ColumnUnit,
		}
		switch {
		case item.As != "":
			bucket.outputColumn = string(item.As)
		case match.OutputColumn != "":
			bucket.outputColumn = match.OutputColumn
		default:
			bucket.outputColumn = tree.AsString(item.Expr)
		}
		found = &bucket
		foundIndex = i
	}
	if found == nil && len(clauses) > 0 {
		return clauseBucket(items, clauses)
	}
	if found == nil || found.step <= 0 {
		return bucketSpec{}, -1, ErrUnsupportedBucket
	}
	return *found, foundIndex, nil
}

// implicitGroups lists the plain select-list columns a sampling clause groups by.
func implicitGroups(clause tree.GroupBy, items tree.SelectExprs, bucketIndex int) ([]string, error) {
	if len(clause) != 0 {
		return nil, ErrInvalidGroupByClause
	}
	var groups []string
	for index, item := range items {
		// A star expands to columns this analyzer cannot name, so the series
		// identity the grouping implies would be unknown.
		switch expr := item.Expr.(type) {
		case tree.UnqualifiedStar, *tree.AllColumnsSelector:
			return nil, ErrInvalidGroupByClause
		case *tree.UnresolvedName:
			if expr.Star {
				return nil, ErrInvalidGroupByClause
			}
		}
		if index == bucketIndex {
			continue
		}
		if _, ok := ColumnName(item.Expr); !ok {
			continue
		}
		if name, ok := outputName(item); ok {
			groups = append(groups, name)
		}
	}
	return groups, nil
}

func analyzeGroupBy(
	clause tree.GroupBy,
	items tree.SelectExprs,
	bucket bucketSpec,
	bucketIndex int,
) ([]string, error) {
	if bucket.implicitGrouping {
		return implicitGroups(clause, items, bucketIndex)
	}
	if len(clause) == 0 {
		return nil, ErrInvalidGroupByClause
	}
	groups := make([]string, 0, len(clause)-1)
	seen := make(map[int]struct{}, len(clause))
	timestampGrouped := false
	for _, expr := range clause {
		index, ok := resolveOutputReference(expr, items)
		if !ok {
			return nil, ErrInvalidGroupByClause
		}
		if _, duplicate := seen[index]; duplicate {
			return nil, ErrInvalidGroupByClause
		}
		seen[index] = struct{}{}
		if index == bucketIndex {
			timestampGrouped = true
			continue
		}
		name, ok := outputName(items[index])
		if !ok {
			return nil, ErrInvalidGroupByClause
		}
		groups = append(groups, name)
	}
	if !timestampGrouped {
		return nil, ErrInvalidGroupByClause
	}
	// Every non-aggregated plain column in the select list must be grouped so
	// DPC's tag-based series identity holds.
	for index, item := range items {
		if index == bucketIndex {
			continue
		}
		if _, ok := ColumnName(item.Expr); !ok {
			continue
		}
		if _, grouped := seen[index]; !grouped {
			return nil, ErrInvalidGroupByClause
		}
	}
	return groups, nil
}

// analyzeOrderBy resolves an ORDER BY clause to result-column terms the delta
// tier can reproduce when rebuilding a response. Every term must reference a
// select-list output; anything else (expressions absent from the select list,
// index ordering) fails closed to the object cache, where responses are
// returned byte-verbatim.
func analyzeOrderBy(clause tree.OrderBy, items tree.SelectExprs) ([]sqlanalyzer.OrderTerm, error) {
	if len(clause) == 0 {
		return nil, nil
	}
	terms := make([]sqlanalyzer.OrderTerm, 0, len(clause))
	seen := make(map[int]struct{}, len(clause))
	for _, order := range clause {
		if order == nil || order.OrderType != tree.OrderByColumn {
			return nil, ErrUnsupportedOrdering
		}
		index, ok := resolveOutputReference(order.Expr, items)
		if !ok {
			return nil, ErrUnsupportedOrdering
		}
		if _, duplicate := seen[index]; duplicate {
			return nil, ErrUnsupportedOrdering
		}
		seen[index] = struct{}{}
		name, ok := outputName(items[index])
		if !ok {
			return nil, ErrUnsupportedOrdering
		}
		descending := order.Direction == tree.Descending
		terms = append(terms, sqlanalyzer.OrderTerm{
			Column:     name,
			Descending: descending,
			NullsFirst: nullsFirst(order.NullsOrder, descending),
		})
	}
	return terms, nil
}

// nullsFirst resolves a term's null placement, defaulting to the PostgreSQL
// and DataFusion convention: nulls sort last ascending and first descending.
func nullsFirst(order tree.NullsOrder, descending bool) bool {
	switch order {
	case tree.NullsFirst:
		return true
	case tree.NullsLast:
		return false
	}
	return descending
}

// resolveOutputReference resolves a GROUP BY item to a select-list index via
// ordinal, alias, or source column name.
func resolveOutputReference(expr tree.Expr, items tree.SelectExprs) (int, bool) {
	if number, ok := expr.(*tree.NumVal); ok {
		ordinal, err := number.AsInt64()
		if err != nil || ordinal <= 0 || ordinal > int64(len(items)) {
			return -1, false
		}
		return int(ordinal - 1), true
	}
	name, ok := ColumnName(expr)
	if !ok {
		return -1, false
	}
	for index, item := range items {
		if item.As != "" && strings.EqualFold(name, string(item.As)) {
			return index, true
		}
	}
	found := -1
	for index, item := range items {
		column, ok := ColumnName(item.Expr)
		if ok && strings.EqualFold(name, column) {
			if found >= 0 {
				return -1, false
			}
			found = index
		}
	}
	return found, found >= 0
}

func outputName(item tree.SelectExpr) (string, bool) {
	if item.As != "" {
		return string(item.As), true
	}
	return ColumnName(item.Expr)
}

type boundStyle uint8

const (
	boundUnixSeconds boundStyle = iota
	boundUnixMilli
	boundUnixMicro
	boundUnixNano
	boundSQLDateTime
	boundSQLDate
	boundRFC3339
	boundTimestampLiteral
)

type endpoint uint8

const (
	endpointLower endpoint = iota
	endpointUpper
)

type boundTarget struct {
	endpoint endpoint
	style    boundStyle
	field    string
	offset   time.Duration
	set      func(tree.Expr)
}

type analyzedBound struct {
	value     time.Time
	inclusive bool
	style     boundStyle
	target    *boundTarget
}

type predicateBound struct {
	field string
	lower *analyzedBound
	upper *analyzedBound
}

type rangeAnalysis struct {
	lower               analyzedBound
	upper               *analyzedBound
	targets             []*boundTarget
	addSynthetic        func(tree.Expr)
	timeColumn          string
	lowerStyle          boundStyle
	dropsPartialBuckets bool
}

func (a *Analyzer) analyzeRanges(
	clause *tree.SelectClause,
	bucket bucketSpec,
	now time.Time,
) (rangeAnalysis, error) {
	result := rangeAnalysis{timeColumn: bucket.timeColumn}
	if clause.Where == nil {
		return result, ErrNotTimeRangeQuery
	}
	conditions := sqlanalyzer.FlattenConjunction(clause.Where.Expr, unwrapParens, splitAnd, nil)
	var predicates []predicateBound
	for _, condition := range conditions {
		if containsUnsafeBoolean(condition) {
			return result, ErrUnsafePredicate
		}
		predicate, ok, err := analyzePredicate(condition, now)
		if err != nil {
			return result, err
		}
		if ok {
			predicates = append(predicates, predicate)
		}
	}

	isPrimary := func(field string) bool {
		return strings.EqualFold(field, bucket.timeColumn) ||
			strings.EqualFold(field, bucket.outputColumn)
	}
	for _, predicate := range predicates {
		if !isPrimary(predicate.field) {
			continue
		}
		if predicate.lower != nil {
			if !result.lower.value.IsZero() {
				return result, ErrAmbiguousTimeAxis
			}
			result.lower = *predicate.lower
		}
		if predicate.upper != nil {
			if result.upper != nil {
				return result, ErrAmbiguousTimeAxis
			}
			upper := *predicate.upper
			result.upper = &upper
		}
	}
	if result.lower.value.IsZero() {
		return result, fmt.Errorf("%w: time column %q did not match a lower range predicate",
			ErrNoLowerBound, bucket.timeColumn)
	}
	result.lowerStyle = result.lower.style
	result.targets = append(result.targets, result.lower.target)
	if result.upper != nil {
		result.targets = append(result.targets, result.upper.target)
	} else {
		where := clause.Where
		result.addSynthetic = func(expr tree.Expr) {
			where.Expr = &tree.AndExpr{Left: where.Expr, Right: expr}
		}
	}
	if !a.boundStyleAllowed(result.lower.style, bucket) ||
		result.upper != nil && !a.boundStyleAllowed(result.upper.style, bucket) {
		return result, ErrUnsafePredicate
	}
	if err := normalizePrimaryBounds(&result, bucket, a.opts.RoundUnalignedTimeBounds,
		a.opts.BoundPrecision); err != nil {
		return result, err
	}
	return result, nil
}

func (a *Analyzer) boundStyleAllowed(style boundStyle, bucket bucketSpec) bool {
	if bucket.columnUnit != 0 {
		// an epoch column compared with a timestamp, or in another unit, is a different range
		return inputTypeForBound(style) == bucket.columnUnit
	}
	if a.opts.RejectZonelessBounds {
		switch style {
		case boundSQLDateTime, boundSQLDate, boundTimestampLiteral:
			return false
		}
	}
	return true
}

// normalizePrimaryBounds converts SQL predicates into Trickster's inclusive
// bucket extent convention. Raw timestamp predicates must describe complete
// buckets; otherwise a partial aggregate could be cached as a complete bucket.
// When roundUnaligned is set, unaligned raw-column bounds are instead rounded
// inward to the cadence (lower up, upper down), dropping partial edge buckets
// per the contract's unaligned-bound provision. An inclusive upper is always
// floored, since its boundary bucket is partial. Predicates on
// the bucket output are discrete and can safely move by one cadence for
// strict comparisons.
func normalizePrimaryBounds(
	result *rangeAnalysis, bucket bucketSpec, roundUnaligned bool, precision time.Duration,
) error {
	rounded := false
	lowerOnOutput := result.lower.target != nil &&
		strings.EqualFold(result.lower.target.field, bucket.outputColumn) &&
		!strings.EqualFold(bucket.outputColumn, bucket.timeColumn)
	if lowerOnOutput {
		if result.lower.inclusive {
			result.lower.value = sqlanalyzer.CeilBucket(result.lower.value, bucket.step, bucket.phase)
		} else {
			result.lower.value = sqlanalyzer.FloorBucket(result.lower.value, bucket.step, bucket.phase)
			result.lower.target.offset = -bucket.step
		}
	} else {
		if !result.lower.inclusive {
			return ErrUnsafePredicate
		}
		if !sqlanalyzer.AlignedToBucket(result.lower.value, bucket.step, bucket.phase) {
			if !roundUnaligned {
				return ErrUnsafePredicate
			}
			result.lower.value = sqlanalyzer.CeilBucket(result.lower.value, bucket.step, bucket.phase)
			rounded = true
			result.dropsPartialBuckets = true
		}
	}

	if result.upper == nil {
		return nil
	}
	upperOnOutput := result.upper.target != nil &&
		strings.EqualFold(result.upper.target.field, bucket.outputColumn) &&
		!strings.EqualFold(bucket.outputColumn, bucket.timeColumn)
	if upperOnOutput {
		if result.upper.inclusive {
			result.upper.value = sqlanalyzer.FloorBucket(result.upper.value, bucket.step, bucket.phase)
		} else {
			result.upper.value = sqlanalyzer.CeilBucket(result.upper.value, bucket.step, bucket.phase)
			result.upper.target.offset = bucket.step
		}
		return nil
	}
	if result.upper.inclusive {
		if result.upper.target == nil {
			return ErrUnsafePredicate
		}
		tick, ok := inclusiveUpperTick(result.upper.target.style)
		tick = max(tick, precision)
		if !ok || tick > bucket.step {
			return ErrUnsafePredicate
		}
		switch {
		case sqlanalyzer.AlignedToBucket(result.upper.value.Add(tick), bucket.step, bucket.phase):
			// col <= X with X one tick below a boundary covers that bucket whole; it is
			// the form this renderer writes, so a rendered statement reads back unchanged
			result.upper.value = result.upper.value.Add(tick)
		case !sqlanalyzer.AlignedToBucket(result.upper.value, bucket.step, bucket.phase):
			if !roundUnaligned {
				return ErrUnsafePredicate
			}
			result.upper.value = sqlanalyzer.FloorBucket(result.upper.value, bucket.step, bucket.phase)
			result.dropsPartialBuckets = true
		default:
			result.dropsPartialBuckets = true
		}
		// col <= X reaches at most the first instant of the bucket holding X,
		// so that bucket is partial; the floored value is the exclusive
		// equivalent, and the rendered literal sits one tick below it.
		result.upper.inclusive = false
		result.upper.target.offset = bucket.step - tick
		rounded = true
	} else {
		if !sqlanalyzer.AlignedToBucket(result.upper.value, bucket.step, bucket.phase) {
			if !roundUnaligned {
				return ErrUnsafePredicate
			}
			result.upper.value = sqlanalyzer.FloorBucket(result.upper.value, bucket.step, bucket.phase)
			rounded = true
			result.dropsPartialBuckets = true
		}
		result.upper.target.offset = bucket.step
	}
	// Rounding inward can leave no complete bucket; fail closed rather than
	// requesting an inverted or empty window.
	if rounded && !result.upper.value.After(result.lower.value) {
		return ErrUnsafePredicate
	}
	return nil
}

// inclusiveUpperTick returns the resolution of a bound literal's style, used to
// render an inclusive upper bound exactly one tick below the exclusive
// boundary. A date-only literal cannot express that and fails closed.
func inclusiveUpperTick(style boundStyle) (time.Duration, bool) {
	switch style {
	case boundUnixSeconds:
		return time.Second, true
	case boundUnixMilli:
		return time.Millisecond, true
	case boundUnixMicro:
		return time.Microsecond, true
	case boundUnixNano, boundSQLDateTime, boundRFC3339, boundTimestampLiteral:
		return time.Nanosecond, true
	}
	return 0, false
}

func splitAnd(expr tree.Expr) (tree.Expr, tree.Expr, bool) {
	if and, ok := expr.(*tree.AndExpr); ok {
		return and.Left, and.Right, true
	}
	return nil, nil, false
}

func unwrapParens(expr tree.Expr) tree.Expr {
	for {
		paren, ok := expr.(*tree.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.Expr
	}
}

func containsUnsafeBoolean(expr tree.Expr) bool {
	unsafe := false
	walkExprTree(expr, func(node tree.Expr) bool {
		switch value := node.(type) {
		case *tree.OrExpr, *tree.NotExpr:
			unsafe = true
		case *tree.RangeCond:
			if value.Not {
				unsafe = true
			}
		case *tree.ComparisonExpr:
			if value.Operator.Symbol == treecmp.NE {
				unsafe = true
			}
		}
		return !unsafe
	})
	return unsafe
}

func analyzePredicate(expr tree.Expr, now time.Time) (predicateBound, bool, error) {
	expr = unwrapParens(expr)
	switch value := expr.(type) {
	case *tree.RangeCond:
		if value.Not {
			return predicateBound{}, false, ErrUnsafePredicate
		}
		field, ok := ColumnName(value.Left)
		if !ok {
			return predicateBound{}, false, nil
		}
		lower, ok := evaluateBound(value.From, true, now)
		if !ok {
			return predicateBound{}, false, nil
		}
		upper, ok := evaluateBound(value.To, true, now)
		if !ok {
			return predicateBound{}, false, nil
		}
		lower.target = &boundTarget{
			endpoint: endpointLower, style: lower.style, field: field,
			set: func(expr tree.Expr) { value.From = expr },
		}
		upper.target = &boundTarget{
			endpoint: endpointUpper, style: upper.style, field: field,
			set: func(expr tree.Expr) { value.To = expr },
		}
		return predicateBound{field: field, lower: &lower, upper: &upper}, true, nil
	case *tree.ComparisonExpr:
		operator, ok := comparisonSymbol(value.Operator)
		if !ok {
			return predicateBound{}, false, nil
		}
		field, fieldOnLeft := ColumnName(value.Left)
		boundExpr := value.Right
		setBound := func(expr tree.Expr) { value.Right = expr }
		if !fieldOnLeft {
			field, fieldOnLeft = ColumnName(value.Right)
			boundExpr = value.Left
			setBound = func(expr tree.Expr) { value.Left = expr }
			operator = invertSymbol(operator)
		}
		if !fieldOnLeft {
			return predicateBound{}, false, nil
		}
		inclusive := operator == treecmp.GE || operator == treecmp.LE
		bound, ok := evaluateBound(boundExpr, inclusive, now)
		if !ok {
			return predicateBound{}, false, nil
		}
		predicate := predicateBound{field: field}
		if operator == treecmp.GT || operator == treecmp.GE {
			bound.target = &boundTarget{
				endpoint: endpointLower, style: bound.style, field: field, set: setBound,
			}
			predicate.lower = &bound
		} else {
			bound.target = &boundTarget{
				endpoint: endpointUpper, style: bound.style, field: field, set: setBound,
			}
			predicate.upper = &bound
		}
		return predicate, true, nil
	default:
		return predicateBound{}, false, nil
	}
}

func comparisonSymbol(operator treecmp.ComparisonOperator) (treecmp.ComparisonOperatorSymbol, bool) {
	switch operator.Symbol {
	case treecmp.GT, treecmp.GE, treecmp.LT, treecmp.LE:
		return operator.Symbol, true
	default:
		return 0, false
	}
}

func invertSymbol(symbol treecmp.ComparisonOperatorSymbol) treecmp.ComparisonOperatorSymbol {
	switch symbol {
	case treecmp.GT:
		return treecmp.LT
	case treecmp.GE:
		return treecmp.LE
	case treecmp.LT:
		return treecmp.GT
	case treecmp.LE:
		return treecmp.GE
	default:
		return symbol
	}
}

// evaluateBound resolves a time-bound expression to a concrete time and the
// literal style needed to round-trip it when rendering extents.
func evaluateBound(expr tree.Expr, inclusive bool, now time.Time) (analyzedBound, bool) {
	expr = unwrapParens(expr)
	switch value := expr.(type) {
	case *tree.NumVal:
		integer, err := value.AsInt64()
		if err != nil {
			return analyzedBound{}, false
		}
		style, parsed, ok := timeFromEpoch(integer)
		return analyzedBound{value: parsed, inclusive: inclusive, style: style}, ok
	case *tree.StrVal:
		parsed, style, ok := parseSQLTime(value.RawString())
		return analyzedBound{value: parsed, inclusive: inclusive, style: style}, ok
	case *tree.CastExpr:
		parsed, ok := timestampLiteral(value)
		if !ok {
			return analyzedBound{}, false
		}
		return analyzedBound{value: parsed, inclusive: inclusive, style: boundTimestampLiteral}, true
	case *tree.FuncExpr:
		name := strings.ToLower(value.Func.String())
		if (name == "now" || name == "current_timestamp") && len(value.Exprs) == 0 {
			return analyzedBound{value: now, inclusive: inclusive, style: boundRFC3339}, true
		}
	case *tree.BinaryExpr:
		offset, ok := intervalDuration(value.Right)
		if !ok {
			return analyzedBound{}, false
		}
		left, ok := evaluateBound(value.Left, inclusive, now)
		if !ok {
			return analyzedBound{}, false
		}
		switch value.Operator.Symbol {
		case treebin.Plus:
			left.value = left.value.Add(offset)
		case treebin.Minus:
			left.value = left.value.Add(-offset)
		default:
			return analyzedBound{}, false
		}
		return left, true
	}
	return analyzedBound{}, false
}

// timeFromEpoch infers the epoch unit of an integer time bound by magnitude:
// values below 1e11 are seconds (through the year 5138), below 1e14
// milliseconds, below 1e17 microseconds, and nanoseconds beyond.
func timeFromEpoch(value int64) (boundStyle, time.Time, bool) {
	magnitude := value
	if magnitude < 0 {
		magnitude = -magnitude
	}
	switch {
	case magnitude < 100_000_000_000:
		return boundUnixSeconds, time.Unix(value, 0).UTC(), true
	case magnitude < 100_000_000_000_000:
		return boundUnixMilli, time.UnixMilli(value).UTC(), true
	case magnitude < 100_000_000_000_000_000:
		return boundUnixMicro, time.UnixMicro(value).UTC(), true
	default:
		return boundUnixNano, time.Unix(0, value).UTC(), true
	}
}

func parseSQLTime(value string) (time.Time, boundStyle, bool) {
	layouts := []struct {
		layout string
		style  boundStyle
	}{
		{"2006-01-02 15:04:05.999999999", boundSQLDateTime},
		{"2006-01-02 15:04:05", boundSQLDateTime},
		{"2006-01-02", boundSQLDate},
		{time.RFC3339Nano, boundRFC3339},
		{time.RFC3339, boundRFC3339},
	}
	for _, entry := range layouts {
		parsed, err := time.ParseInLocation(entry.layout, value, time.UTC)
		if err == nil {
			return parsed, entry.style, true
		}
	}
	return time.Time{}, 0, false
}

func inputTypeForBound(style boundStyle) timeseries.FieldDataType {
	switch style {
	case boundUnixMilli:
		return timeseries.DateTimeUnixMilli
	case boundUnixMicro:
		return timeseries.DateTimeUnixMicro
	case boundUnixNano:
		return timeseries.DateTimeUnixNano
	case boundSQLDateTime, boundTimestampLiteral:
		return timeseries.DateTimeSQL
	case boundSQLDate:
		return timeseries.DateSQL
	case boundRFC3339:
		return timeseries.DateTimeRFC3339
	default:
		return timeseries.DateTimeUnixSecs
	}
}

// placeholderExpr is a raw-token expression used to stamp extent placeholders
// into a serialized statement template. It is never type-checked or evaluated.
type placeholderExpr struct {
	token string
}

func (p *placeholderExpr) String() string                { return p.token }
func (p *placeholderExpr) Format(ctx *tree.FmtCtx)       { ctx.WriteString(p.token) }
func (p *placeholderExpr) Walk(_ tree.Visitor) tree.Expr { return p }
func (p *placeholderExpr) TypeCheck(
	_ context.Context, _ *tree.SemaContext, _ *types.T,
) (tree.TypedExpr, error) {
	return nil, errors.New("trickster placeholder expressions cannot be type-checked")
}

type cockroachRenderer struct {
	template string
	bounds   []rendererBound
	// numericAsRFC3339 renders numeric epoch bound styles as RFC3339 literals
	// for dialects that reject Timestamp-to-integer comparisons.
	numericAsRFC3339 bool
}

type rendererBound struct {
	token    string
	endpoint endpoint
	style    boundStyle
	offset   time.Duration
}

// RenderExtent implements sqlanalyzer.ExtentRenderer.
func (r *cockroachRenderer) RenderExtent(extent timeseries.Extent) (string, error) {
	statement := r.template
	for _, bound := range r.bounds {
		value := extent.Start
		if bound.endpoint == endpointUpper {
			value = extent.End
		}
		value = value.Add(bound.offset)
		style := bound.style
		if r.numericAsRFC3339 {
			switch style {
			case boundUnixSeconds, boundUnixMilli, boundUnixMicro, boundUnixNano:
				style = boundRFC3339
			}
		}
		statement = strings.ReplaceAll(statement, bound.token, boundLiteral(style, value))
	}
	return statement, nil
}

func boundLiteral(style boundStyle, value time.Time) string {
	switch style {
	case boundUnixMilli:
		return strconv.FormatInt(value.UnixMilli(), 10)
	case boundUnixMicro:
		return strconv.FormatInt(value.UnixMicro(), 10)
	case boundUnixNano:
		return strconv.FormatInt(value.UnixNano(), 10)
	case boundSQLDateTime, boundSQLDate:
		return "'" + formatSQLTimestamp(value) + "'"
	case boundRFC3339:
		// RFC3339Nano renders whole seconds without a fractional component and
		// preserves sub-second bound values when a dialect allows them.
		return "'" + value.UTC().Format(time.RFC3339Nano) + "'"
	case boundTimestampLiteral:
		return "TIMESTAMP '" + formatSQLTimestamp(value) + "'"
	default:
		return strconv.FormatInt(value.Unix(), 10)
	}
}

func formatSQLTimestamp(value time.Time) string {
	layout := "2006-01-02 15:04:05"
	if value.Nanosecond() != 0 {
		layout += ".999999999"
	}
	return value.UTC().Format(layout)
}

func buildQueryArtifacts(
	statement *tree.Select,
	clause *tree.SelectClause,
	ranges rangeAnalysis,
	bucket bucketSpec,
	numericAsRFC3339 bool,
) (string, *cockroachRenderer) {
	occupied := tree.AsString(statement)
	bounds := make([]rendererBound, 0, len(ranges.targets)+1)
	addBound := func(target endpoint, style boundStyle, offset time.Duration) tree.Expr {
		index := len(bounds)
		token := fmt.Sprintf("<$TRICKSTER_TS%d_%d$>", target+1, index)
		for strings.Contains(occupied, token) {
			index++
			token = fmt.Sprintf("<$TRICKSTER_TS%d_%d$>", target+1, index)
		}
		occupied += token
		bounds = append(bounds, rendererBound{token: token, endpoint: target, style: style, offset: offset})
		return &placeholderExpr{token: token}
	}
	for _, target := range ranges.targets {
		target.set(addBound(target.endpoint, target.style, target.offset))
	}
	if ranges.addSynthetic != nil {
		ranges.addSynthetic(&tree.ComparisonExpr{
			Operator: treecmp.MakeComparisonOperator(treecmp.LT),
			Left:     columnExpression(ranges.timeColumn),
			Right:    addBound(endpointUpper, ranges.lowerStyle, bucket.step),
		})
	}

	canonical := tree.AsString(statement)
	for _, bound := range bounds {
		canonical = strings.ReplaceAll(canonical, bound.token, placeholderFor(bound.endpoint))
	}
	_ = clause
	return canonical, &cockroachRenderer{
		template: tree.AsString(statement), bounds: bounds,
		numericAsRFC3339: numericAsRFC3339,
	}
}

func placeholderFor(target endpoint) string {
	if target == endpointLower {
		return "<$TS1$>"
	}
	return "<$TS2$>"
}

func columnExpression(name string) tree.Expr {
	return tree.NewUnresolvedName(name)
}
