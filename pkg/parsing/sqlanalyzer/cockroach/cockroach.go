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
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/parser"
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
)

// BucketMatch describes a recognized time-bucketing expression.
type BucketMatch struct {
	// TimeColumn is the source time column the bucket function operates on.
	TimeColumn string
	// Step is the bucket cadence.
	Step time.Duration
	// Phase is the bucket alignment offset from the Unix epoch.
	Phase time.Duration
}

// BucketMatcher inspects a lowercase function name and its arguments and
// reports the bucket cadence and time column when the call is a dialect
// time-bucketing function.
type BucketMatcher func(name string, args []tree.Expr) (BucketMatch, bool)

// Options configures an Analyzer for a specific backend dialect.
type Options struct {
	// BucketMatchers describe the dialect's time-bucketing functions.
	BucketMatchers []BucketMatcher
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
		if !ok {
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
	parsed, err := parser.ParseOne(statement)
	if err != nil {
		mode := sqlanalyzer.CacheModeNone
		if leadingKeywordIsSelect(statement) {
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
		return sqlanalyzer.Analysis{
			Mode:   sqlanalyzer.CacheModeNone,
			Reason: sqlanalyzer.ReasonUnsupportedStatement, Err: ErrUnsupportedStatement,
		}
	}
	if selectStmt.Limit != nil {
		return objectAnalysis(sqlanalyzer.ReasonUnsupportedLimit, ErrUnsupportedLimit)
	}
	if selectStmt.With != nil || clause.Distinct || len(clause.DistinctOn) > 0 ||
		clause.Having != nil || len(clause.Window) > 0 || containsSubquery(clause) {
		return objectAnalysis(sqlanalyzer.ReasonUnsupportedFormat, ErrUnsupportedStatement)
	}
	if containsVolatileFunction(clause.Exprs) {
		return sqlanalyzer.Analysis{
			Mode:   sqlanalyzer.CacheModeNone,
			Reason: sqlanalyzer.ReasonNondeterministic, Err: ErrUnsupportedStatement,
		}
	}

	bucket, bucketIndex, err := a.analyzeSelectList(clause.Exprs)
	if err != nil {
		return objectAnalysis(sqlanalyzer.ReasonUnsupportedBucket, err)
	}
	groups, err := analyzeGroupBy(clause.GroupBy, clause.Exprs, bucket, bucketIndex)
	if err != nil {
		return objectAnalysis(sqlanalyzer.ReasonUnsupportedGrouping, err)
	}
	ranges, err := analyzeRanges(clause, bucket, now)
	if err != nil {
		reason := sqlanalyzer.ReasonNotTimeRange
		if errors.Is(err, ErrUnsafePredicate) {
			reason = sqlanalyzer.ReasonUnsafePredicate
		} else if errors.Is(err, ErrAmbiguousTimeAxis) {
			reason = sqlanalyzer.ReasonAmbiguousTimeAxis
		}
		return objectAnalysis(reason, err)
	}

	canonical, renderer := buildQueryArtifacts(selectStmt, clause, ranges, bucket)
	plan := &sqlanalyzer.QueryPlan{
		CanonicalSQL: canonical,
		TimeColumn:   bucket.timeColumn,
		OutputColumn: bucket.outputColumn,
		Step:         bucket.step,
		Phase:        bucket.phase,
		OutputUnit:   timeseries.DateTimeRFC3339Nano,
		InputUnit:    inputTypeForBound(ranges.lower.style),
		LowerBound: &sqlanalyzer.Bound{
			Value: ranges.lower.value, Inclusive: ranges.lower.inclusive,
		},
		GroupColumns: groups,
		Renderer:     renderer,
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

func objectAnalysis(reason sqlanalyzer.AnalysisReason, err error) sqlanalyzer.Analysis {
	return sqlanalyzer.Analysis{Mode: sqlanalyzer.CacheModeObject, Reason: reason, Err: err}
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
}

func (a *Analyzer) analyzeSelectList(items tree.SelectExprs) (bucketSpec, int, error) {
	var found *bucketSpec
	foundIndex := -1
	for i, item := range items {
		function, ok := item.Expr.(*tree.FuncExpr)
		if !ok {
			continue
		}
		name := strings.ToLower(function.Func.String())
		for _, matcher := range a.opts.BucketMatchers {
			match, ok := matcher(name, function.Exprs)
			if !ok {
				continue
			}
			if found != nil {
				return bucketSpec{}, -1, ErrAmbiguousTimeAxis
			}
			bucket := bucketSpec{
				timeColumn: match.TimeColumn,
				step:       match.Step,
				phase:      match.Phase,
			}
			if item.As != "" {
				bucket.outputColumn = string(item.As)
			} else {
				bucket.outputColumn = tree.AsString(item.Expr)
			}
			found = &bucket
			foundIndex = i
			break
		}
	}
	if found == nil || found.step <= 0 {
		return bucketSpec{}, -1, ErrUnsupportedBucket
	}
	return *found, foundIndex, nil
}

func analyzeGroupBy(
	clause tree.GroupBy,
	items tree.SelectExprs,
	bucket bucketSpec,
	bucketIndex int,
) ([]string, error) {
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
	lower        analyzedBound
	upper        *analyzedBound
	targets      []*boundTarget
	addSynthetic func(tree.Expr)
	timeColumn   string
	lowerStyle   boundStyle
}

func analyzeRanges(
	clause *tree.SelectClause,
	bucket bucketSpec,
	now time.Time,
) (rangeAnalysis, error) {
	result := rangeAnalysis{timeColumn: bucket.timeColumn}
	if clause.Where == nil {
		return result, ErrNotTimeRangeQuery
	}
	conditions, err := flattenConjunction(clause.Where.Expr, nil)
	if err != nil {
		return result, err
	}
	var predicates []predicateBound
	for _, condition := range conditions {
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
	if err := normalizePrimaryBounds(&result, bucket); err != nil {
		return result, err
	}
	return result, nil
}

// normalizePrimaryBounds converts SQL predicates into Trickster's inclusive
// bucket extent convention. Raw timestamp predicates must describe complete
// buckets; otherwise a partial aggregate could be cached as a complete bucket.
// Predicates on the bucket output are discrete and can safely move by one
// cadence for strict comparisons.
func normalizePrimaryBounds(result *rangeAnalysis, bucket bucketSpec) error {
	lowerOnOutput := result.lower.target != nil &&
		strings.EqualFold(result.lower.target.field, bucket.outputColumn) &&
		!strings.EqualFold(bucket.outputColumn, bucket.timeColumn)
	if lowerOnOutput {
		if result.lower.inclusive {
			result.lower.value = ceilBucket(result.lower.value, bucket)
		} else {
			result.lower.value = floorBucket(result.lower.value, bucket)
			result.lower.target.offset = -bucket.step
		}
	} else if !result.lower.inclusive || !alignedToBucket(result.lower.value, bucket) {
		return ErrUnsafePredicate
	}

	if result.upper == nil {
		return nil
	}
	upperOnOutput := result.upper.target != nil &&
		strings.EqualFold(result.upper.target.field, bucket.outputColumn) &&
		!strings.EqualFold(bucket.outputColumn, bucket.timeColumn)
	if upperOnOutput {
		if result.upper.inclusive {
			result.upper.value = floorBucket(result.upper.value, bucket)
		} else {
			result.upper.value = ceilBucket(result.upper.value, bucket)
			result.upper.target.offset = bucket.step
		}
		return nil
	}
	if result.upper.inclusive || !alignedToBucket(result.upper.value, bucket) {
		return ErrUnsafePredicate
	}
	result.upper.target.offset = bucket.step
	return nil
}

func alignedToBucket(value time.Time, bucket bucketSpec) bool {
	if bucket.step <= 0 {
		return false
	}
	return (value.UnixNano()-bucket.phase.Nanoseconds())%bucket.step.Nanoseconds() == 0
}

func floorBucket(value time.Time, bucket bucketSpec) time.Time {
	step := bucket.step.Nanoseconds()
	phase := bucket.phase.Nanoseconds()
	epochNs := value.UnixNano()
	remainder := (epochNs - phase) % step
	if remainder < 0 {
		remainder += step
	}
	return time.Unix(0, epochNs-remainder).UTC()
}

func ceilBucket(value time.Time, bucket bucketSpec) time.Time {
	floor := floorBucket(value, bucket)
	if floor.Equal(value) {
		return floor
	}
	return floor.Add(bucket.step)
}

func flattenConjunction(expr tree.Expr, out []tree.Expr) ([]tree.Expr, error) {
	expr = unwrapParens(expr)
	if and, ok := expr.(*tree.AndExpr); ok {
		var err error
		out, err = flattenConjunction(and.Left, out)
		if err != nil {
			return nil, err
		}
		return flattenConjunction(and.Right, out)
	}
	if containsUnsafeBoolean(expr) {
		return nil, ErrUnsafePredicate
	}
	return append(out, expr), nil
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
		statement = strings.ReplaceAll(statement, bound.token, boundLiteral(bound.style, value))
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
		return "'" + value.UTC().Format("2006-01-02 15:04:05") + "'"
	case boundRFC3339:
		return "'" + value.UTC().Format("2006-01-02T15:04:05Z") + "'"
	case boundTimestampLiteral:
		return "TIMESTAMP '" + value.UTC().Format("2006-01-02 15:04:05") + "'"
	default:
		return strconv.FormatInt(value.Unix(), 10)
	}
}

func buildQueryArtifacts(
	statement *tree.Select,
	clause *tree.SelectClause,
	ranges rangeAnalysis,
	bucket bucketSpec,
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
	return canonical, &cockroachRenderer{template: tree.AsString(statement), bounds: bounds}
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
