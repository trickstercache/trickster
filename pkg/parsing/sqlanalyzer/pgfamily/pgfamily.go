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

// Package pgfamily adapts the PostgreSQL-family query parser in pkg/parsing/pgparser to Trickster's
// dialect-independent sqlanalyzer contract. That grammar covers the PostgreSQL-flavored SQL dialects
// spoken by engines that have no native Go parser — InfluxDB 3 (Apache DataFusion) and Apache Druid
// among them — so one adapter serves multiple backend providers. Dialect-specific
// time-bucketing functions are supplied per backend via Options.BucketMatchers;
// everything else (statement gating, range analysis, canonicalization, and
// extent rendering) is shared.
package pgfamily

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser"
	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlscan"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/directives"
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
	// ErrUnsupportedDialectSyntax indicates a dialect clause no matcher claims, or a function suffix.
	ErrUnsupportedDialectSyntax = errors.New("unsupported dialect syntax")
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
type BucketMatcher func(name string, args []ast.Expr) (BucketMatch, bool)

// ExprBucketMatcher recognizes a bucket written as an arbitrary select-list
// expression, such as epoch arithmetic, rather than as one function call.
type ExprBucketMatcher func(expr ast.Expr) (BucketMatch, bool)

// Clause is a dialect clause a ClauseMatcher claimed, with what it means for the cache plan.
type Clause struct {
	// Syntax is the clause as parsed.
	Syntax *ast.DialectClause
	// Payload is defined by the matcher and handed to ClauseBucketMatchers.
	Payload any
	// Bucket declares the time bucket when the clause itself defines it and no
	// select-list function does. Its TimeColumn must appear in the select list.
	Bucket *BucketMatch
	// InferTimeColumn asks the analyzer to use the first select-list item when
	// the dialect clause defines a bucket but does not name its timestamp
	// column. The item must be a plain column reference; ambiguous expressions
	// fail closed.
	InferTimeColumn bool
	// ImplicitGrouping marks a clause that groups by every plain select-list
	// column without a GROUP BY, as a sampling clause does.
	ImplicitGrouping bool
}

// ClauseMatcher claims a dialect clause the parser read, as Options.Syntax configures them. It
// returns nil when the clause is not its own, and an error when it is but cannot be delta cached.
type ClauseMatcher func(clause *ast.DialectClause) (*Clause, error)

// ClauseBucketMatcher is a BucketMatcher that also sees the statement's claimed clauses.
type ClauseBucketMatcher func(name string, args []ast.Expr, clauses []*Clause) (BucketMatch, bool)

// Options configures an Analyzer for a specific backend dialect.
type Options struct {
	// BucketMatchers describe the dialect's time-bucketing functions.
	BucketMatchers []BucketMatcher
	// ExprBucketMatchers run on select-list items no BucketMatcher recognized.
	ExprBucketMatchers []ExprBucketMatcher
	// Syntax is the dialect's syntax beyond the PostgreSQL family's: clauses, function suffixes,
	// duration literals and backtick-quoted identifiers.
	Syntax pgparser.DialectSpec
	// ClauseMatchers claim the dialect clauses Syntax names, in order. A statement with a clause no
	// matcher claims, or with a function suffix, is cached as a whole object.
	ClauseMatchers []ClauseMatcher
	// ClauseBucketMatchers describe bucketing functions whose cadence or
	// alignment comes from a claimed clause rather than from their arguments.
	ClauseBucketMatchers []ClauseBucketMatcher
	// RenderNumericBoundsAsRFC3339 renders time bounds that were written as
	// bare epoch integers back as quoted RFC3339 literals. Engines with strict
	// type coercion, such as Apache DataFusion (InfluxDB 3), reject
	// Timestamp-to-Int64 comparisons but coerce string literals to timestamps.
	// Bounds written as string or TIMESTAMP literals are unaffected.
	RenderNumericBoundsAsRFC3339 bool
	// NakedIntIsInt4 parses the bare INT and INTEGER type names as 4-byte
	// integers, as PostgreSQL defines them, instead of the parser's 8-byte default.
	NakedIntIsInt4 bool
	// BoundPrecision is the finest time resolution the engine stores. An inclusive
	// upper bound renders one such tick below the boundary; zero means 1ns.
	BoundPrecision time.Duration
	// RejectZonelessBounds fails closed on time bounds written without a zone,
	// for sessions where the engine would not read them as UTC.
	RejectZonelessBounds bool
	// IsVolatileFunction receives lowercase names and recognizes nondeterministic
	// dialect functions in addition to the PostgreSQL-compatible defaults.
	IsVolatileFunction func(name string) bool
}

// Analyzer converts PostgreSQL-family SQL into Trickster's dialect-independent
// cache plan. It contains no mutable per-query state and is safe for
// concurrent use.
type Analyzer struct {
	opts       Options
	parserOpts pgparser.Options
	fmtFlags   ast.FmtFlags
	suffixes   bool
}

var _ sqlanalyzer.DialectAnalyzer = (*Analyzer)(nil)

// NewAnalyzer returns an Analyzer configured with the provided Options.
func NewAnalyzer(opts Options) *Analyzer {
	a := &Analyzer{opts: opts, parserOpts: pgparser.Options{NakedIntIsInt4: opts.NakedIntIsInt4}}
	syntax := opts.Syntax
	if len(syntax.Clauses) > 0 || len(syntax.FunctionSuffixes) > 0 || syntax.DurationLiterals ||
		syntax.BacktickIdentifiers {
		a.parserOpts.Dialect = pgparser.NewDialect(syntax)
	}
	if syntax.BacktickIdentifiers {
		a.fmtFlags = ast.FmtBacktickIdentifiers
	}
	a.suffixes = len(syntax.FunctionSuffixes) > 0
	return a
}

const (
	day            = 24 * time.Hour
	punctOpenParen = "("
)

var queryKeywords = [...]string{"select", "with", "values", "table"}

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
	// fields alternate count, unit; FieldsSeq walks them without building a slice
	var total time.Duration
	var count int64
	pending := false
	for field := range strings.FieldsSeq(strings.ToLower(s)) {
		if !pending {
			n, err := strconv.ParseInt(field, base10, 64)
			if err != nil || n <= 0 {
				return 0, false
			}
			count, pending = n, true
			continue
		}
		unit, ok := intervalUnits[field]
		if !ok || count > int64((1<<63-1-total)/unit) {
			return 0, false
		}
		total += time.Duration(count) * unit
		pending = false
	}
	if pending || total <= 0 {
		return 0, false
	}
	return total, true
}

// ColumnName returns the bare column name of a simple column reference.
func ColumnName(expr ast.Expr) (string, bool) {
	name, ok := expr.(*ast.UnresolvedName)
	if !ok || name.Star || name.NumParts < 1 {
		return "", false
	}
	return name.Parts[0], true
}

// intervalDuration extracts a fixed duration from an INTERVAL expression.
func intervalDuration(expr ast.Expr) (time.Duration, bool) {
	cast, ok := expr.(*ast.CastExpr)
	if !ok {
		return 0, false
	}
	castType, ok := ast.GetStaticallyKnownType(cast.Type)
	if !ok || castType.Family() != ast.IntervalFamily {
		return 0, false
	}
	literal, ok := cast.Expr.(*ast.StrVal)
	if !ok {
		return 0, false
	}
	return ParseIntervalDuration(literal.RawString())
}

// timestampLiteral extracts a concrete time from a TIMESTAMP '...' style cast
// or a bare SQL datetime string literal.
func timestampLiteral(expr ast.Expr) (time.Time, bool) {
	switch value := expr.(type) {
	case *ast.StrVal:
		parsed, _, ok := parseSQLTime(value.RawString())
		return parsed, ok
	case *ast.CastExpr:
		castType, ok := ast.GetStaticallyKnownType(value.Type)
		if !ok {
			return time.Time{}, false
		}
		switch castType.Family() {
		case ast.TimestampFamily, ast.TimestampTZFamily, ast.DateFamily:
			if literal, ok := value.Expr.(*ast.StrVal); ok {
				parsed, _, ok := parseSQLTime(literal.RawString())
				return parsed, ok
			}
		}
	}
	return time.Time{}, false
}

// DateBinMatcher matches DataFusion's date_bin(INTERVAL, column[, origin]).
func DateBinMatcher(name string, args []ast.Expr) (BucketMatch, bool) {
	if name != "date_bin" || len(args) < 2 || len(args) > 3 {
		return BucketMatch{}, false
	}
	step, ok := intervalDuration(args[0])
	if !ok {
		return BucketMatch{}, false
	}
	return dateBinMatch(step, args)
}

func dateBinMatch(step time.Duration, args []ast.Expr) (BucketMatch, bool) {
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
func DateTruncMatcher(name string, args []ast.Expr) (BucketMatch, bool) {
	if name != "date_trunc" || len(args) != 2 {
		return BucketMatch{}, false
	}
	literal, ok := args[0].(*ast.StrVal)
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
	// only a query reaches the parser, whose grammar holds no other kind of statement
	if !leadingKeywordIsQuery(statement) {
		return sqlanalyzer.Analysis{
			Mode:   sqlanalyzer.CacheModeNone,
			Reason: sqlanalyzer.ReasonUnsupportedStatement, Err: ErrUnsupportedStatement,
		}
	}
	selectStmt, err := a.parse(statement)
	if err != nil {
		err = fmt.Errorf("%w: %w", ErrInvalidSQL, err)
		if leadingKeywordIsSelect(original) {
			return a.objectAnalysis(original, sqlanalyzer.ReasonInvalidSQL, err)
		}
		return sqlanalyzer.Analysis{Reason: sqlanalyzer.ReasonInvalidSQL, Err: err}
	}
	clause, ok := selectStmt.Select.(*ast.SelectClause)
	if !ok || len(selectStmt.Locking) > 0 {
		if _, compound := selectStmt.Select.(*ast.UnionClause); compound &&
			len(selectStmt.Locking) == 0 {
			return a.objectAnalysis(original,
				sqlanalyzer.ReasonUnsupportedFormat, ErrUnsupportedStatement)
		}
		return sqlanalyzer.Analysis{
			Mode:   sqlanalyzer.CacheModeNone,
			Reason: sqlanalyzer.ReasonUnsupportedStatement, Err: ErrUnsupportedStatement,
		}
	}
	scan := &exprScan{isVolatile: a.opts.IsVolatileFunction}
	if selectStmt.Limit != nil {
		return a.objectAnalysis(original, sqlanalyzer.ReasonUnsupportedLimit, ErrUnsupportedLimit)
	}
	if selectStmt.With != nil || clause.Distinct || len(clause.DistinctOn) > 0 ||
		clause.Having != nil || len(clause.Window) > 0 || containsSubquery(scan, clause) {
		return a.objectAnalysis(original, sqlanalyzer.ReasonUnsupportedFormat, ErrUnsupportedStatement)
	}
	// Joins and multi-table selects can carry time predicates in ON clauses
	// this analyzer never inspects, and inline window frames span rows across
	// bucket boundaries; per-extent delta computation would return wrong
	// values for either, so both fail closed to the object cache.
	if !singleTableFrom(clause) || scan.inItems(scanWindow, clause.Exprs) {
		return a.objectAnalysis(original, sqlanalyzer.ReasonUnsupportedFormat, ErrUnsupportedStatement)
	}
	if scan.inItems(scanVolatile, clause.Exprs) {
		return sqlanalyzer.Analysis{
			Mode:   sqlanalyzer.CacheModeNone,
			Reason: sqlanalyzer.ReasonNondeterministic, Err: ErrUnsupportedStatement,
		}
	}
	if a.suffixes && containsSuffix(scan, selectStmt, clause) {
		return a.objectAnalysis(original, sqlanalyzer.ReasonUnsupportedFormat, ErrUnsupportedDialectSyntax)
	}
	claimed, err := a.claimClauses(clause.Dialect)
	if err != nil {
		reason := sqlanalyzer.ReasonUnsupportedBucket
		if errors.Is(err, ErrUnsupportedDialectSyntax) {
			reason = sqlanalyzer.ReasonUnsupportedFormat
		}
		return a.objectAnalysis(original, reason, err)
	}

	bucket, bucketIndex, err := a.analyzeSelectList(clause.Exprs, claimed)
	if err != nil {
		return a.objectAnalysis(original, sqlanalyzer.ReasonUnsupportedBucket, err)
	}
	groups, bucketGroup, err := analyzeGroupBy(clause.GroupBy, clause.Exprs, bucket, bucketIndex)
	if err != nil {
		return a.objectAnalysis(original, sqlanalyzer.ReasonUnsupportedGrouping, err)
	}
	ordering, err := analyzeOrderBy(selectStmt.OrderBy, clause.Exprs)
	if err != nil {
		return a.objectAnalysis(original, sqlanalyzer.ReasonUnsupportedOrdering, err)
	}
	ranges, err := a.analyzeRanges(scan, clause, bucket, now)
	if err != nil {
		reason := sqlanalyzer.ReasonNotTimeRange
		if errors.Is(err, ErrUnsafePredicate) {
			reason = sqlanalyzer.ReasonUnsafePredicate
		} else if errors.Is(err, ErrAmbiguousTimeAxis) {
			reason = sqlanalyzer.ReasonAmbiguousTimeAxis
		}
		return a.objectAnalysis(original, reason, err)
	}

	canonical, renderer := buildQueryArtifacts(selectStmt, ranges, bucket,
		a.opts.RenderNumericBoundsAsRFC3339, a.fmtFlags)
	renderer.compile()
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
		RawLower:            &ranges.rawLower,
		RawUpper:            ranges.rawUpper,
		UpperIsNow:          ranges.upperIsNow,
		GroupColumns:        groups,
		BucketGroupIndex:    bucketGroup,
		DropsPartialBuckets: ranges.dropsPartialBuckets,
		Ordering:            ordering,
		Renderer:            renderer,
		Directives:          directives.Parse(original, directives.SyntaxSQL),
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

func (a *Analyzer) objectAnalysis(statement string, reason sqlanalyzer.AnalysisReason, err error) sqlanalyzer.Analysis {
	// Unsupported syntax still needs a volatility check before its result can be cached.
	// Scan tokens so strings and comments cannot be mistaken for function calls.
	scanner := sqlscan.New(statement, sqlscan.Options{})
	var previous sqlscan.Token
	for {
		token, ok := scanner.Next()
		if !ok {
			break
		}
		if token.Kind == sqlscan.Punct && scanner.Text(token) == "(" &&
			(previous.Kind == sqlscan.Word || previous.Kind == sqlscan.QuotedIdent) {
			name := scanner.Text(previous)
			if previous.Kind == sqlscan.QuotedIdent {
				if name[0] != '"' {
					// Unicode escapes are not decoded by the scanner, so do not cache these calls.
					return sqlanalyzer.Analysis{Reason: sqlanalyzer.ReasonNondeterministic, Err: ErrUnsupportedStatement}
				}
				name = strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
			}
			if isVolatileFunction(strings.ToLower(name), a.opts.IsVolatileFunction) {
				return sqlanalyzer.Analysis{Reason: sqlanalyzer.ReasonNondeterministic, Err: ErrUnsupportedStatement}
			}
		}
		previous = token
	}
	return sqlanalyzer.ObjectAnalysis(reason, err)
}

func (a *Analyzer) parse(statement string) (*ast.Select, error) {
	return parseGuarded(statement, a.parserOpts)
}

var (
	// ErrParserFailure indicates a statement the parser panicked on
	ErrParserFailure = errors.New("SQL parser failure")
	// ErrNoStatement indicates input holding no statement, such as a lone comment
	ErrNoStatement = errors.New("no SQL statement")
)

// parseQuery is the parser entry point, a variable so tests can make it fail
var parseQuery = pgparser.ParseQuery

// ParseOne parses one query statement, reporting a parser panic as an error
func ParseOne(statement string) (*ast.Select, error) {
	return parseGuarded(statement, pgparser.Options{})
}

func parseGuarded(statement string, opts pgparser.Options) (parsed *ast.Select, err error) {
	defer func() {
		if recover() != nil {
			parsed, err = nil, ErrParserFailure
		}
	}()
	if parsed, err = parseQuery(statement, opts); err == nil && parsed == nil {
		err = ErrNoStatement
	}
	return parsed, err
}

func (a *Analyzer) claimClauses(clauses []*ast.DialectClause) ([]*Clause, error) {
	if len(clauses) == 0 {
		return nil, nil
	}
	claimed := make([]*Clause, 0, len(clauses))
	for _, syntax := range clauses {
		var found *Clause
		for _, matcher := range a.opts.ClauseMatchers {
			clause, err := matcher(syntax)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", ErrUnsupportedBucket, err)
			}
			if clause != nil {
				clause.Syntax = syntax
				found = clause
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedDialectSyntax, syntax.Keyword)
		}
		claimed = append(claimed, found)
	}
	return claimed, nil
}

// leadingKeywordIsQuery reports whether a statement opens the way a query can: with SELECT, WITH,
// VALUES, TABLE or a parenthesis, after any comments
func leadingKeywordIsQuery(statement string) bool {
	scanner := sqlscan.New(statement, sqlscan.Options{})
	token, ok := scanner.Next()
	if !ok {
		return false
	}
	if token.Kind == sqlscan.Punct {
		return scanner.Text(token) == punctOpenParen
	}
	if token.Kind != sqlscan.Word {
		return false
	}
	for _, keyword := range queryKeywords {
		if scanner.IsWord(token, keyword) {
			return true
		}
	}
	return false
}

// leadingKeywordIsSelect reports whether an unparsable statement still begins
// with a SELECT, keeping it eligible for the object proxy cache.
func leadingKeywordIsSelect(statement string) bool {
	fields := strings.Fields(strings.ToLower(statement))
	return len(fields) > 0 && fields[0] == "select"
}

// singleTableFrom reports whether the select reads from exactly one plain
// table. Joins (including parenthesized ones) and comma-joined table lists
// fail the check; FROM-subqueries are separately rejected by containsSubquery.
func singleTableFrom(clause *ast.SelectClause) bool {
	if len(clause.From.Tables) != 1 {
		return false
	}
	aliased, ok := clause.From.Tables[0].(*ast.AliasedTableExpr)
	if !ok {
		return false
	}
	_, ok = aliased.Expr.(*ast.TableName)
	return ok
}

var volatileFunctions = map[string]struct{}{
	"now": {}, "current_timestamp": {}, "current_date": {}, "current_time": {},
	"random": {}, "gen_random_uuid": {}, "uuid_generate_v4": {},
}

func isVolatileFunction(name string, isDialectVolatile func(string) bool) bool {
	_, unsafe := volatileFunctions[name]
	return unsafe || isDialectVolatile != nil && isDialectVolatile(name)
}

type bucketSpec struct {
	timeColumn   string
	outputColumn string
	step         time.Duration
	phase        time.Duration
	outputUnit   timeseries.FieldDataType
	columnUnit   timeseries.FieldDataType
	// implicitGrouping is set when a dialect clause, not a GROUP BY, groups the rows.
	implicitGrouping bool
}

// matchBucket tries the plain matchers, then the ones that read dialect clauses.
func (a *Analyzer) matchBucket(name string, args []ast.Expr, clauses []*Clause) (BucketMatch, bool) {
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

func (a *Analyzer) matchItem(expr ast.Expr, clauses []*Clause) (BucketMatch, bool) {
	if function, ok := expr.(*ast.FuncExpr); ok {
		name := strings.ToLower(function.Func.Name())
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

// clauseBucket finds the select-list column a dialect clause buckets by.
func clauseBucket(items ast.SelectExprs, clauses []*Clause) (bucketSpec, int, error) {
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
			if clause.InferTimeColumn {
				// Sampling clauses such as QuestDB's SAMPLE BY apply to the
				// designated timestamp, which is the first selected bare column.
				// Do not guess from later group columns or computed expressions.
				if i != 0 || !ok {
					continue
				}
			} else if !ok || name != clause.Bucket.TimeColumn {
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

func (a *Analyzer) analyzeSelectList(items ast.SelectExprs, clauses []*Clause) (bucketSpec, int, error) {
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
			bucket.outputColumn = ast.AsString(item.Expr)
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
func implicitGroups(clause ast.GroupBy, items ast.SelectExprs, bucketIndex int) ([]string, error) {
	if len(clause) != 0 {
		return nil, ErrInvalidGroupByClause
	}
	var groups []string
	for index, item := range items {
		// A star expands to columns this analyzer cannot name, so the series
		// identity the grouping implies would be unknown.
		switch expr := item.Expr.(type) {
		case ast.UnqualifiedStar:
			return nil, ErrInvalidGroupByClause
		case *ast.UnresolvedName:
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

// analyzeGroupBy returns the grouping columns other than the bucket, in GROUP BY order, and the
// bucket's place among the GROUP BY terms
func analyzeGroupBy(
	clause ast.GroupBy,
	items ast.SelectExprs,
	bucket bucketSpec,
	bucketIndex int,
) ([]string, int, error) {
	if bucket.implicitGrouping {
		groups, err := implicitGroups(clause, items, bucketIndex)
		return groups, 0, err
	}
	if len(clause) == 0 {
		return nil, 0, ErrInvalidGroupByClause
	}
	groups := make([]string, 0, len(clause)-1)
	seen := make(map[int]struct{}, len(clause))
	bucketGroup := -1
	for term, expr := range clause {
		index, ok := resolveOutputReference(expr, items)
		if !ok {
			return nil, 0, ErrInvalidGroupByClause
		}
		if _, duplicate := seen[index]; duplicate {
			return nil, 0, ErrInvalidGroupByClause
		}
		seen[index] = struct{}{}
		if index == bucketIndex {
			bucketGroup = term
			continue
		}
		name, ok := outputName(items[index])
		if !ok {
			return nil, 0, ErrInvalidGroupByClause
		}
		groups = append(groups, name)
	}
	if bucketGroup < 0 {
		return nil, 0, ErrInvalidGroupByClause
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
			return nil, 0, ErrInvalidGroupByClause
		}
	}
	return groups, bucketGroup, nil
}

// analyzeOrderBy resolves an ORDER BY clause to result-column terms the delta
// tier can reproduce when rebuilding a response. Every term must reference a
// select-list output; anything else (expressions absent from the select list,
// index ordering) fails closed to the object cache, where responses are
// returned byte-verbatim.
func analyzeOrderBy(clause ast.OrderBy, items ast.SelectExprs) ([]sqlanalyzer.OrderTerm, error) {
	if len(clause) == 0 {
		return nil, nil
	}
	terms := make([]sqlanalyzer.OrderTerm, 0, len(clause))
	seen := make(map[int]struct{}, len(clause))
	for _, order := range clause {
		if order == nil || order.OrderType != ast.OrderByColumn {
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
		descending := order.Direction == ast.Descending
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
func nullsFirst(order ast.NullsOrder, descending bool) bool {
	switch order {
	case ast.NullsFirst:
		return true
	case ast.NullsLast:
		return false
	}
	return descending
}

// resolveOutputReference resolves a GROUP BY item to a select-list index via
// ordinal, alias, or source column name.
func resolveOutputReference(expr ast.Expr, items ast.SelectExprs) (int, bool) {
	if number, ok := expr.(*ast.NumVal); ok {
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

func outputName(item ast.SelectExpr) (string, bool) {
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
	set      func(ast.Expr)
	original string
}

type analyzedBound struct {
	value     time.Time
	inclusive bool
	style     boundStyle
	target    *boundTarget
	// now reports a bound written as a bare now()
	now bool
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
	addSynthetic        func(ast.Expr)
	timeColumn          string
	lowerStyle          boundStyle
	rawLower            sqlanalyzer.Bound
	rawUpper            *sqlanalyzer.Bound
	upperIsNow          bool
	dropsPartialBuckets bool
}

func (r *rangeAnalysis) recordRaw() {
	r.rawLower = sqlanalyzer.Bound{Value: r.lower.value, Inclusive: r.lower.inclusive}
	if r.upper != nil {
		r.rawUpper = &sqlanalyzer.Bound{Value: r.upper.value, Inclusive: r.upper.inclusive}
	}
}

func (a *Analyzer) analyzeRanges(
	scan *exprScan,
	clause *ast.SelectClause,
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
		if scan.in(scanUnsafe, condition) {
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
		result.addSynthetic = func(expr ast.Expr) {
			where.Expr = &ast.AndExpr{Left: where.Expr, Right: expr}
		}
	}
	if !a.boundStyleAllowed(result.lower.style, bucket) ||
		result.upper != nil && !a.boundStyleAllowed(result.upper.style, bucket) {
		return result, ErrUnsafePredicate
	}
	// keep the bounds as the statement wrote them, before rounding them to the grid
	result.recordRaw()
	if err := normalizePrimaryBounds(&result, bucket, a.opts.BoundPrecision,
		a.opts.RenderNumericBoundsAsRFC3339); err != nil {
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

func normalizePrimaryBounds(
	result *rangeAnalysis, bucket bucketSpec, precision time.Duration, numericAsRFC3339 bool,
) error {
	// rounds unaligned raw bounds inward (kept raw for the planner), so no partial bucket is cached
	// whole; an inclusive upper floors, and output-column bounds shift a step
	rounded := false
	lowerOnOutput := result.lower.target != nil &&
		strings.EqualFold(result.lower.target.field, bucket.outputColumn) &&
		!strings.EqualFold(bucket.outputColumn, bucket.timeColumn)
	if lowerOnOutput {
		first := timeseries.CeilToGrid(result.lower.value, bucket.step, bucket.phase)
		if result.lower.inclusive {
			result.lower.value = first
		} else {
			result.lower.value = timeseries.FloorToGrid(result.lower.value, bucket.step, bucket.phase)
			result.lower.target.offset = -bucket.step
			first = result.lower.value.Add(bucket.step)
		}
		// output labels are discrete, so the raw range starts at the first label, on the grid
		result.rawLower = sqlanalyzer.Bound{Value: first, Inclusive: true}
	} else {
		if !result.lower.inclusive {
			return ErrUnsafePredicate
		}
		if !timeseries.OnGrid(result.lower.value, bucket.step, bucket.phase) {
			result.lower.value = timeseries.CeilToGrid(result.lower.value, bucket.step, bucket.phase)
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
		end := timeseries.CeilToGrid(result.upper.value, bucket.step, bucket.phase)
		if result.upper.inclusive {
			result.upper.value = timeseries.FloorToGrid(result.upper.value, bucket.step, bucket.phase)
			end = result.upper.value.Add(bucket.step)
		} else {
			result.upper.value = end
			result.upper.target.offset = bucket.step
		}
		result.rawUpper = &sqlanalyzer.Bound{Value: end}
		return nil
	}
	result.upperIsNow = result.upper.now
	if result.upper.inclusive {
		if result.upper.target == nil {
			return ErrUnsafePredicate
		}
		// the tick of the literal the renderer writes, which an epoch integer rendered as RFC3339 refines
		style := result.upper.target.style
		if numericAsRFC3339 && numericStyle(style) {
			style = boundRFC3339
		}
		tick, ok := inclusiveUpperTick(style)
		tick = max(tick, precision)
		if !ok || tick > bucket.step {
			return ErrUnsafePredicate
		}
		switch {
		case timeseries.OnGrid(result.upper.value.Add(tick), bucket.step, bucket.phase):
			// col <= X with X one tick below a boundary covers that bucket whole; it is
			// the form this renderer writes, so a rendered statement reads back unchanged
			result.upper.value = result.upper.value.Add(tick)
			result.rawUpper = &sqlanalyzer.Bound{Value: result.upper.value}
		case !timeseries.OnGrid(result.upper.value, bucket.step, bucket.phase):
			result.upper.value = timeseries.FloorToGrid(result.upper.value, bucket.step, bucket.phase)
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
		if !timeseries.OnGrid(result.upper.value, bucket.step, bucket.phase) {
			result.upper.value = timeseries.FloorToGrid(result.upper.value, bucket.step, bucket.phase)
			rounded = true
			result.dropsPartialBuckets = true
		}
		result.upper.target.offset = bucket.step
	}
	// rounding inward can leave no complete bucket, which the planner sends as written; both bounds
	// sit at the rounded-up lower boundary, so no inverted window is described
	if rounded && !result.upper.value.After(result.lower.value) {
		result.upper.value = result.lower.value
	}
	return nil
}

func numericStyle(style boundStyle) bool {
	switch style {
	case boundUnixSeconds, boundUnixMilli, boundUnixMicro, boundUnixNano:
		return true
	}
	return false
}

// inclusiveUpperTick returns a literal style's resolution, the tick an inclusive upper bound
// renders below the exclusive boundary; a date-only literal has none and fails closed
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

func splitAnd(expr ast.Expr) (ast.Expr, ast.Expr, bool) {
	if and, ok := expr.(*ast.AndExpr); ok {
		return and.Left, and.Right, true
	}
	return nil, nil, false
}

func unwrapParens(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.Expr
	}
}

func analyzePredicate(expr ast.Expr, now time.Time) (predicateBound, bool, error) {
	expr = unwrapParens(expr)
	switch value := expr.(type) {
	case *ast.RangeCond:
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
			set: func(expr ast.Expr) { value.From = expr },
		}
		upper.target = &boundTarget{
			endpoint: endpointUpper, style: upper.style, field: field,
			set: func(expr ast.Expr) { value.To = expr },
		}
		return predicateBound{field: field, lower: &lower, upper: &upper}, true, nil
	case *ast.ComparisonExpr:
		operator, ok := comparisonSymbol(value.Operator)
		if !ok {
			return predicateBound{}, false, nil
		}
		field, fieldOnLeft := ColumnName(value.Left)
		boundExpr := value.Right
		setBound := func(expr ast.Expr) { value.Right = expr }
		if !fieldOnLeft {
			field, fieldOnLeft = ColumnName(value.Right)
			boundExpr = value.Left
			setBound = func(expr ast.Expr) { value.Left = expr }
			operator = invertSymbol(operator)
		}
		if !fieldOnLeft {
			return predicateBound{}, false, nil
		}
		inclusive := operator == ast.GE || operator == ast.LE
		bound, ok := evaluateBound(boundExpr, inclusive, now)
		if !ok {
			return predicateBound{}, false, nil
		}
		predicate := predicateBound{field: field}
		if operator == ast.GT || operator == ast.GE {
			bound.target = &boundTarget{
				endpoint: endpointLower, style: bound.style, field: field, set: setBound,
			}
			predicate.lower = &bound
		} else {
			bound.target = &boundTarget{
				endpoint: endpointUpper, style: bound.style, field: field, set: setBound,
			}
			if bound.now {
				bound.target.original = ast.AsString(boundExpr)
			}
			predicate.upper = &bound
		}
		return predicate, true, nil
	default:
		return predicateBound{}, false, nil
	}
}

func comparisonSymbol(operator ast.ComparisonOperator) (ast.ComparisonOperatorSymbol, bool) {
	switch operator.Symbol {
	case ast.GT, ast.GE, ast.LT, ast.LE:
		return operator.Symbol, true
	default:
		return 0, false
	}
}

func invertSymbol(symbol ast.ComparisonOperatorSymbol) ast.ComparisonOperatorSymbol {
	switch symbol {
	case ast.GT:
		return ast.LT
	case ast.GE:
		return ast.LE
	case ast.LT:
		return ast.GT
	case ast.LE:
		return ast.GE
	default:
		return symbol
	}
}

// evaluateBound resolves a time-bound expression to a concrete time and the
// literal style needed to round-trip it when rendering extents.
func evaluateBound(expr ast.Expr, inclusive bool, now time.Time) (analyzedBound, bool) {
	expr = unwrapParens(expr)
	switch value := expr.(type) {
	case *ast.NumVal:
		integer, err := value.AsInt64()
		if err != nil {
			return analyzedBound{}, false
		}
		style, parsed, ok := timeFromEpoch(integer)
		return analyzedBound{value: parsed, inclusive: inclusive, style: style}, ok
	case *ast.StrVal:
		parsed, style, ok := parseSQLTime(value.RawString())
		return analyzedBound{value: parsed, inclusive: inclusive, style: style}, ok
	case *ast.CastExpr:
		parsed, ok := timestampLiteral(value)
		if !ok {
			return analyzedBound{}, false
		}
		return analyzedBound{value: parsed, inclusive: inclusive, style: boundTimestampLiteral}, true
	case *ast.FuncExpr:
		name := strings.ToLower(value.Func.Name())
		if (name == "now" || name == "current_timestamp") && len(value.Exprs) == 0 {
			// current_timestamp formats with parentheses, which not every dialect accepts
			return analyzedBound{value: now, inclusive: inclusive, style: boundRFC3339, now: name == "now"}, true
		}
	case *ast.BinaryExpr:
		offset, ok := intervalDuration(value.Right)
		if !ok {
			return analyzedBound{}, false
		}
		left, ok := evaluateBound(value.Left, inclusive, now)
		if !ok {
			return analyzedBound{}, false
		}
		switch value.Operator.Symbol {
		case ast.Plus:
			left.value = left.value.Add(offset)
		case ast.Minus:
			left.value = left.value.Add(-offset)
		default:
			return analyzedBound{}, false
		}
		left.now = false
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

func (p *placeholderExpr) String() string              { return p.token }
func (p *placeholderExpr) Format(ctx *ast.FmtCtx)      { ctx.WriteString(p.token) }
func (p *placeholderExpr) Walk(_ ast.Visitor) ast.Expr { return p }

type pgRenderer struct {
	template string
	// openTemplate is the template without the upper bound added for a statement that has none
	openTemplate string
	bounds       []rendererBound
	// numericAsRFC3339 renders numeric epoch bound styles as RFC3339 literals
	// for dialects that reject Timestamp-to-integer comparisons.
	numericAsRFC3339 bool
	step             time.Duration
	// upperTick is the resolution of an inclusive upper bound, which renders one tick below the
	// exclusive boundary
	upperTick time.Duration
	// closed and open are template and openTemplate split around their bound tokens
	closed, open compiledTemplate
}

type rendererBound struct {
	token    string
	endpoint endpoint
	style    boundStyle
	offset   time.Duration
	// original is a bare now() upper bound as written, which a range running to now keeps
	original string
}

// templatePart is a run of template text and the bound that follows it, or noSlot for the last run
type templatePart struct {
	text string
	slot int
}

const noSlot = -1

// compiledTemplate is a statement template split around its bound tokens
type compiledTemplate struct {
	parts []templatePart
	// size is a capacity estimate for one rendered statement
	size int
}

// boundLiteralSize is a capacity estimate for one rendered bound literal
const boundLiteralSize = len("TIMESTAMP '2006-01-02 15:04:05.999999999'")

func compileTemplate(template string, bounds []rendererBound) compiledTemplate {
	slots := 0
	for i := range bounds {
		slots += strings.Count(template, bounds[i].token)
	}
	t := compiledTemplate{parts: make([]templatePart, 0, slots+1), size: len(template) + boundLiteralSize*slots}
	for rest := template; ; {
		at, slot := -1, noSlot
		for i := range bounds {
			if j := strings.Index(rest, bounds[i].token); j >= 0 && (at < 0 || j < at) {
				at, slot = j, i
			}
		}
		if at < 0 {
			t.parts = append(t.parts, templatePart{text: rest, slot: noSlot})
			return t
		}
		t.parts = append(t.parts, templatePart{text: rest[:at], slot: slot})
		rest = rest[at+len(bounds[slot].token):]
	}
}

// compile splits the finished templates around their bound tokens; it runs once, before the plan is shared
func (r *pgRenderer) compile() {
	r.closed = compileTemplate(r.template, r.bounds)
	if r.openTemplate != "" {
		r.open = compileTemplate(r.openTemplate, r.bounds)
	}
}

// RenderExtent implements sqlanalyzer.ExtentRenderer.
func (r *pgRenderer) RenderExtent(extent timeseries.Extent) (string, error) {
	buf, err := r.AppendExtent(make([]byte, 0, r.closed.size), extent)
	return string(buf), err
}

// AppendExtent implements sqlanalyzer.ExtentAppender.
func (r *pgRenderer) AppendExtent(dst []byte, extent timeseries.Extent) ([]byte, error) {
	for _, part := range r.closed.parts {
		dst = append(dst, part.text...)
		if part.slot != noSlot {
			dst = r.appendBound(dst, r.bounds[part.slot], extent)
		}
	}
	return dst, nil
}

// RenderRange implements sqlanalyzer.RangeRenderer.
func (r *pgRenderer) RenderRange(pb timeseries.PartialBucket) (string, error) {
	// raw lower bounds are inclusive, and only an inclusive upper bound yields an inclusive range
	if pb.LowerExclusive || pb.UpperInclusive && r.upperTick == 0 {
		return "", sqlanalyzer.ErrUnsupportedRange
	}
	open := pb.Upper.IsZero()
	tmpl := &r.closed
	if open && r.openTemplate != "" {
		tmpl = &r.open
	}
	end := pb.Upper
	if pb.UpperInclusive {
		end = end.Add(r.upperTick)
	}
	// with its last label a step before the range's exclusive end, each bound's offset renders
	// the range through that bound's own comparator
	extent := timeseries.Extent{Start: pb.Lower, End: end.Add(-r.step)}
	buf := make([]byte, 0, tmpl.size)
	for _, part := range tmpl.parts {
		buf = append(buf, part.text...)
		if part.slot == noSlot {
			break
		}
		bound := r.bounds[part.slot]
		switch {
		case !open || bound.endpoint == endpointLower:
			buf = r.appendBound(buf, bound, extent)
		case bound.original != "":
			buf = append(buf, bound.original...)
		default:
			return "", sqlanalyzer.ErrUnsupportedRange
		}
	}
	return string(buf), nil
}

func (r *pgRenderer) appendBound(dst []byte, bound rendererBound, extent timeseries.Extent) []byte {
	value := extent.Start
	if bound.endpoint == endpointUpper {
		value = extent.End
	}
	style := bound.style
	if r.numericAsRFC3339 && numericStyle(style) {
		style = boundRFC3339
	}
	return appendBoundLiteral(dst, style, value.Add(bound.offset))
}

const (
	sqlTimestampLayout   = "2006-01-02 15:04:05"
	sqlFractionLayout    = sqlTimestampLayout + ".999999999"
	timestampLiteralHead = "TIMESTAMP '"
	quoteChar            = '\''
	base10               = 10
)

func appendBoundLiteral(dst []byte, style boundStyle, value time.Time) []byte {
	switch style {
	case boundUnixMilli:
		return strconv.AppendInt(dst, value.UnixMilli(), base10)
	case boundUnixMicro:
		return strconv.AppendInt(dst, value.UnixMicro(), base10)
	case boundUnixNano:
		return strconv.AppendInt(dst, value.UnixNano(), base10)
	case boundSQLDateTime, boundSQLDate:
		dst = append(dst, quoteChar)
		return append(appendSQLTimestamp(dst, value), quoteChar)
	case boundRFC3339:
		// RFC3339Nano renders whole seconds without a fractional component and
		// preserves sub-second bound values when a dialect allows them.
		dst = append(dst, quoteChar)
		return append(value.UTC().AppendFormat(dst, time.RFC3339Nano), quoteChar)
	case boundTimestampLiteral:
		dst = append(dst, timestampLiteralHead...)
		return append(appendSQLTimestamp(dst, value), quoteChar)
	default:
		return strconv.AppendInt(dst, value.Unix(), base10)
	}
}

func appendSQLTimestamp(dst []byte, value time.Time) []byte {
	if value.Nanosecond() != 0 {
		return value.UTC().AppendFormat(dst, sqlFractionLayout)
	}
	return value.UTC().AppendFormat(dst, sqlTimestampLayout)
}

func buildQueryArtifacts(
	statement *ast.Select,
	ranges rangeAnalysis,
	bucket bucketSpec,
	numericAsRFC3339 bool,
	flags ast.FmtFlags,
) (string, *pgRenderer) {
	occupied := ast.AsStringWithFlags(statement, flags)
	bounds := make([]rendererBound, 0, len(ranges.targets)+1)
	addBound := func(target endpoint, style boundStyle, offset time.Duration, original string) ast.Expr {
		index := len(bounds)
		token := fmt.Sprintf("<$TRICKSTER_TS%d_%d$>", target+1, index)
		for strings.Contains(occupied, token) {
			index++
			token = fmt.Sprintf("<$TRICKSTER_TS%d_%d$>", target+1, index)
		}
		occupied += token
		bounds = append(bounds, rendererBound{
			token: token, endpoint: target, style: style, offset: offset, original: original,
		})
		return &placeholderExpr{token: token}
	}
	for _, target := range ranges.targets {
		target.set(addBound(target.endpoint, target.style, target.offset, target.original))
	}
	renderer := &pgRenderer{numericAsRFC3339: numericAsRFC3339, step: bucket.step}
	if ranges.rawUpper != nil && ranges.rawUpper.Inclusive && ranges.upper != nil {
		renderer.upperTick = bucket.step - ranges.upper.target.offset
	}
	if ranges.addSynthetic != nil {
		// a range running to now is rendered with no upper bound, as the statement was written
		renderer.openTemplate = ast.AsStringWithFlags(statement, flags)
		ranges.addSynthetic(&ast.ComparisonExpr{
			Operator: ast.MakeComparisonOperator(ast.LT),
			Left:     columnExpression(ranges.timeColumn),
			Right:    addBound(endpointUpper, ranges.lowerStyle, bucket.step, ""),
		})
	}

	template := ast.AsStringWithFlags(statement, flags)
	canonical := template
	for _, bound := range bounds {
		canonical = strings.ReplaceAll(canonical, bound.token, placeholderFor(bound.endpoint))
	}
	renderer.template, renderer.bounds = template, bounds
	return canonical, renderer
}

func placeholderFor(target endpoint) string {
	if target == endpointLower {
		return "<$TS1$>"
	}
	return "<$TS2$>"
}

func columnExpression(name string) ast.Expr {
	return ast.NewUnresolvedName(name)
}
