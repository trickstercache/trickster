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

package mysql

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"vitess.io/vitess/go/vt/sqlparser"
)

const (
	fromUnixTimeFunction  = "from_unixtime"
	unixTimestampFunction = "unix_timestamp"
)

var (
	// ErrInvalidSQL indicates that Vitess could not parse the statement.
	ErrInvalidSQL = errors.New("invalid MySQL SQL")
	// ErrUnsupportedStatement indicates a statement that cannot be cached safely.
	ErrUnsupportedStatement = errors.New("unsupported MySQL statement")
	// ErrNotTimeRangeQuery indicates a SELECT without a supported time range.
	ErrNotTimeRangeQuery = errors.New("not a MySQL time-range query")
	// ErrUnsupportedBucket indicates an unknown or inconsistent bucket expression.
	ErrUnsupportedBucket = errors.New("unsupported MySQL time bucket")
	// ErrUnsafePredicate indicates range semantics that could cache partial buckets.
	ErrUnsafePredicate = errors.New("unsafe MySQL time predicate")
	// ErrAmbiguousTimeAxis indicates more than one complete time range in a query.
	ErrAmbiguousTimeAxis = errors.New("ambiguous MySQL time axis")
	// ErrUnsupportedResultShape indicates outputs that cannot be modeled safely.
	ErrUnsupportedResultShape = errors.New("unsupported MySQL result shape")
	// ErrUnsupportedGrouping indicates a GROUP BY or ORDER BY layout that DPC cannot preserve.
	ErrUnsupportedGrouping = errors.New("unsupported MySQL grouping or ordering")
)

// Analyzer converts Vitess's MySQL AST into Trickster's dialect-independent
// cache plan. It contains no mutable per-query state and is safe for concurrent use.
type Analyzer struct {
	parser *sqlparser.Parser
}

// NewAnalyzer returns an analyzer configured for MySQL 8 syntax.
func NewAnalyzer() (*Analyzer, error) {
	p, err := sqlparser.New(sqlparser.Options{MySQLServerVersion: "8.0.0"})
	if err != nil {
		return nil, err
	}
	return &Analyzer{parser: p}, nil
}

var defaultAnalyzer = mustNewAnalyzer()

var _ sqlanalyzer.DialectAnalyzer = (*Analyzer)(nil)

func mustNewAnalyzer() *Analyzer {
	a, err := NewAnalyzer()
	if err != nil {
		panic(err)
	}
	return a
}

type bucketInfo struct {
	timeColumn   string
	timeAxis     string
	outputColumn string
	step         time.Duration
	unit         timeseries.FieldDataType
}

type rangeInfo struct {
	lower, upper *mysqlBound
}

type mysqlBound struct {
	value     time.Time
	inclusive bool
	unit      timeseries.FieldDataType
	node      sqlparser.Expr
	style     boundStyle
}

type boundStyle uint8

const (
	boundEpochSeconds boundStyle = iota
	boundEpochNanos
	boundFromUnixTime
)

func (a *Analyzer) Analyze(statement string, _ time.Time) sqlanalyzer.Analysis {
	if strings.TrimSpace(statement) == "" {
		return sqlanalyzer.Analysis{Reason: sqlanalyzer.ReasonInvalidSQL, Err: ErrInvalidSQL}
	}
	stmt, err := a.parser.Parse(statement)
	return a.analyzeParsed(statement, stmt, err)
}

func (a *Analyzer) analyzeParsed(statement string, stmt sqlparser.Statement,
	parseErr error,
) sqlanalyzer.Analysis {
	if strings.Contains(statement, "/*!") {
		return sqlanalyzer.Analysis{
			Mode:   sqlanalyzer.CacheModeNone,
			Reason: sqlanalyzer.ReasonNondeterministic, Err: ErrUnsupportedStatement,
		}
	}
	if parseErr != nil {
		return sqlanalyzer.Analysis{
			Mode: sqlanalyzer.CacheModeNone, Reason: sqlanalyzer.ReasonInvalidSQL,
			Err: fmt.Errorf("%w: %w", ErrInvalidSQL, parseErr),
		}
	}
	selectStmt, ok := stmt.(*sqlparser.Select)
	if !ok {
		return sqlanalyzer.Analysis{
			Mode:   sqlanalyzer.CacheModeNone,
			Reason: sqlanalyzer.ReasonUnsupportedStatement, Err: ErrUnsupportedStatement,
		}
	}
	if selectStmt.Cache != nil || selectStmt.Lock != sqlparser.NoLock || selectStmt.SQLCalcFoundRows ||
		selectStmt.Into != nil || isNondeterministic(selectStmt) {
		return sqlanalyzer.Analysis{
			Mode:   sqlanalyzer.CacheModeNone,
			Reason: sqlanalyzer.ReasonNondeterministic, Err: ErrUnsupportedStatement,
		}
	}
	if selectStmt.Limit != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedLimit, ErrUnsupportedStatement)
	}
	if selectStmt.Distinct || selectStmt.With != nil || selectStmt.Having != nil ||
		len(selectStmt.Windows) > 0 || containsSubquery(selectStmt) {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedFormat, ErrUnsupportedResultShape)
	}
	bucket, err := analyzeBucket(selectStmt)
	if err != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedBucket, err)
	}
	rng, err := analyzeRange(selectStmt.Where, bucket)
	if err != nil {
		reason := sqlanalyzer.ReasonNotTimeRange
		if errors.Is(err, ErrUnsafePredicate) {
			reason = sqlanalyzer.ReasonUnsafePredicate
		} else if errors.Is(err, ErrAmbiguousTimeAxis) {
			reason = sqlanalyzer.ReasonAmbiguousTimeAxis
		}
		return sqlanalyzer.ObjectAnalysis(reason, err)
	}
	groups, values, err := analyzeResultShape(selectStmt, bucket)
	if err != nil {
		reason := sqlanalyzer.ReasonUnsupportedFormat
		if errors.Is(err, ErrUnsupportedGrouping) {
			reason = sqlanalyzer.ReasonUnsupportedGrouping
		}
		return sqlanalyzer.ObjectAnalysis(reason, err)
	}

	canonical, renderer, err := buildArtifacts(selectStmt, rng, bucket.step)
	if err != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsafePredicate, err)
	}
	backfillTolerance := extractBackfillTolerance(statement, selectStmt)
	identitySuffix := ""
	if backfillTolerance > 0 {
		identitySuffix = fmt.Sprintf("backfill_tolerance=%d", int64(backfillTolerance/time.Second))
	}
	plan := &sqlanalyzer.QueryPlan{
		CanonicalSQL: canonical,
		TimeColumn:   bucket.timeColumn, OutputColumn: bucket.outputColumn,
		Step: bucket.step, Phase: 0, OutputUnit: bucket.unit, InputUnit: rng.lower.unit,
		LowerBound:   &sqlanalyzer.Bound{Value: rng.lower.value, Inclusive: rng.lower.inclusive},
		UpperBound:   &sqlanalyzer.Bound{Value: rng.upper.value, Inclusive: rng.upper.inclusive},
		GroupColumns: groups, ValueColumns: values, Renderer: renderer,
		BackfillTolerance: backfillTolerance, IdentitySuffix: identitySuffix,
	}
	return sqlanalyzer.Analysis{
		Mode:   sqlanalyzer.CacheModeDelta,
		Reason: sqlanalyzer.ReasonDeltaCacheable, Plan: plan,
	}
}

func containsSubquery(stmt sqlparser.SQLNode) bool {
	found := false
	_ = sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		if node == stmt {
			return true, nil
		}
		if _, ok := node.(*sqlparser.Subquery); ok {
			found = true
			return false, nil
		}
		return !found, nil
	}, stmt)
	return found
}

func isNondeterministic(stmt sqlparser.SQLNode) bool {
	unsafe := false
	_ = sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		switch n := node.(type) {
		case *sqlparser.FuncExpr:
			name := strings.ToLower(n.Name.String())
			switch name {
			case fromUnixTimeFunction, "coalesce", "floor", "ifnull", "round":
				// These are the deterministic general functions used by supported
				// cache plans. Function calls fail closed unless listed here.
			case unixTimestampFunction:
				// UNIX_TIMESTAMP(column) is deterministic; its zero-argument form is not.
				if len(n.Exprs) == 0 {
					unsafe = true
					return false, nil
				}
			default:
				unsafe = true
				return false, nil
			}
		case *sqlparser.CurTimeFuncExpr, *sqlparser.LockingFunc, *sqlparser.Variable:
			unsafe = true
			return false, nil
		}
		return !unsafe, nil
	}, stmt)
	return unsafe
}

// Parse converts a MySQL statement into the same TimeRangeQuery contract used
// by the HTTP SQL backend. The protocol handler can use the returned cacheability
// flag to select DPC, OPC, or pass-through behavior without exposing Vitess ASTs.
func Parse(statement string, now time.Time) (*timeseries.TimeRangeQuery, bool, error) {
	analysis := defaultAnalyzer.Analyze(statement, now)
	if analysis.Mode == sqlanalyzer.CacheModeNone {
		return nil, false, analysis.Err
	}
	query := sqlanalyzer.NewTimeRangeQuery(statement)
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		return query, true, analysis.Err
	}
	plan := analysis.Plan
	plan.ApplyToQuery(query)
	if plan.IdentitySuffix != "" {
		query.CacheKeyElements["mysql_directives"] = plan.IdentitySuffix
	}
	window, windowErr := buildDeltaRequestWindow(plan)
	if windowErr != nil {
		return query, true, windowErr
	}
	query.Extent = window.output
	return query, true, nil
}

func extractBackfillTolerance(statement string, stmt *sqlparser.Select) time.Duration {
	query := &timeseries.TimeRangeQuery{}
	comments := sqlCommentText(statement)
	if stmt != nil && stmt.Comments != nil {
		comments += " " + strings.Join(stmt.Comments.GetComments(), " ")
	}
	query.ExtractBackfillTolerance("  " + comments)
	return query.BackfillTolerance
}

func sqlCommentText(statement string) string {
	var comments strings.Builder
	for i := 0; i < len(statement); {
		switch statement[i] {
		case '\'', '"', '`':
			quote := statement[i]
			i++
			for i < len(statement) {
				if statement[i] == '\\' {
					i += min(2, len(statement)-i)
					continue
				}
				if statement[i] == quote {
					i++
					if i < len(statement) && statement[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
		case '/':
			if i+1 >= len(statement) || statement[i+1] != '*' {
				i++
				continue
			}
			end := strings.Index(statement[i+2:], "*/")
			if end < 0 {
				return comments.String()
			}
			comments.WriteString(statement[i+2 : i+2+end])
			comments.WriteByte(' ')
			i += end + 4
		case '#':
			end := strings.IndexByte(statement[i+1:], '\n')
			if end < 0 {
				comments.WriteString(statement[i+1:])
				return comments.String()
			}
			comments.WriteString(statement[i+1 : i+1+end])
			comments.WriteByte(' ')
			i += end + 2
		case '-':
			if i+2 >= len(statement) || statement[i+1] != '-' ||
				(statement[i+2] != ' ' && statement[i+2] != '\t') {
				i++
				continue
			}
			end := strings.IndexByte(statement[i+2:], '\n')
			if end < 0 {
				comments.WriteString(statement[i+2:])
				return comments.String()
			}
			comments.WriteString(statement[i+2 : i+2+end])
			comments.WriteByte(' ')
			i += end + 3
		default:
			i++
		}
	}
	return comments.String()
}

func analyzeBucket(stmt *sqlparser.Select) (bucketInfo, error) {
	if stmt.SelectExprs == nil {
		return bucketInfo{}, ErrUnsupportedBucket
	}
	var found *bucketInfo
	for _, item := range stmt.SelectExprs.Exprs {
		ae, ok := item.(*sqlparser.AliasedExpr)
		if !ok {
			continue
		}
		column, axis, seconds, unit, ok := matchBucketExpr(ae.Expr)
		if !ok {
			continue
		}
		alias := ae.As.String()
		if alias == "" {
			alias = "time"
		}
		if found != nil {
			return bucketInfo{}, ErrUnsupportedBucket
		}
		candidate := bucketInfo{
			timeColumn: column, outputColumn: alias,
			timeAxis: axis, step: seconds, unit: unit,
		}
		found = &candidate
	}
	if found == nil {
		return bucketInfo{}, ErrUnsupportedBucket
	}
	return *found, nil
}

func matchBucketExpr(expr sqlparser.Expr) (string, string, time.Duration,
	timeseries.FieldDataType, bool,
) {
	mulExpr, ok := unwrapSignedCasts(expr)
	if !ok {
		return "", "", 0, 0, false
	}
	mul, ok := mulExpr.(*sqlparser.BinaryExpr)
	if !ok || mul.Operator != sqlparser.MultOp {
		return "", "", 0, 0, false
	}
	factor, ok := intLiteral(mul.Right)
	if !ok || factor <= 0 {
		return "", "", 0, 0, false
	}
	divExpr := mul.Left
	castDivision := false
	if unwrapped, castsOK := unwrapSignedCasts(mul.Left); castsOK && unwrapped != mul.Left {
		divExpr = unwrapped
		castDivision = true
	}
	floorDivision := false
	if floor, floorOK := divExpr.(*sqlparser.FuncExpr); floorOK &&
		strings.EqualFold(floor.Name.String(), "floor") && len(floor.Exprs) == 1 {
		divExpr = floor.Exprs[0]
		floorDivision = true
	}
	div, ok := divExpr.(*sqlparser.BinaryExpr)
	if !ok || (div.Operator != sqlparser.DivOp && div.Operator != sqlparser.IntDivOp) {
		return "", "", 0, 0, false
	}
	// Ordinary division must be explicitly converted to an integer bucket.
	// MySQL's DIV operator already provides the integer semantics used by
	// Grafana's $__timeGroup and $__unixEpochGroup macro expansions.
	if div.Operator == sqlparser.DivOp && !castDivision && !floorDivision {
		return "", "", 0, 0, false
	}
	divisor, ok := intLiteral(div.Right)
	if !ok || divisor != factor {
		return "", "", 0, 0, false
	}

	if col, axis, ok := unixTimestampColumn(div.Left); ok {
		if factor > int64(time.Duration(1<<63-1)/time.Second) {
			return "", "", 0, 0, false
		}
		return col, axis, time.Duration(factor) * time.Second,
			timeseries.DateTimeUnixSecs, true
	}
	col, axis, ok := columnReference(div.Left)
	if !ok {
		return "", "", 0, 0, false
	}
	// Grafana's Unix-nanosecond grouping divisor is necessarily much larger
	// than a practical seconds cadence. This keeps the inference deterministic
	// without consulting schema metadata.
	if factor >= int64(time.Second) {
		return col, axis, time.Duration(factor), timeseries.DateTimeUnixNano, true
	}
	if factor > int64(time.Duration(1<<63-1)/time.Second) {
		return "", "", 0, 0, false
	}
	return col, axis, time.Duration(factor) * time.Second,
		timeseries.DateTimeUnixSecs, true
}

func unwrapSignedCasts(expr sqlparser.Expr) (sqlparser.Expr, bool) {
	for {
		cast, ok := expr.(*sqlparser.CastExpr)
		if !ok {
			return expr, true
		}
		if !strings.EqualFold(cast.Type.Type, "signed") {
			return nil, false
		}
		expr = cast.Expr
	}
}

func analyzeRange(where *sqlparser.Where, bucket bucketInfo) (rangeInfo, error) {
	if where == nil {
		return rangeInfo{}, ErrNotTimeRangeQuery
	}
	var out rangeInfo
	if between, ok := where.Expr.(*sqlparser.BetweenExpr); ok && between.IsBetween {
		if !sameTimeAxis(between.Left, bucket) {
			return rangeInfo{}, ErrNotTimeRangeQuery
		}
		// Grafana's $__timeFilter expands to BETWEEN. Its inclusive upper
		// timestamp selects only the boundary instant in the final bucket, so
		// that bucket cannot be reused as a complete DPC bucket.
		return rangeInfo{}, ErrUnsafePredicate
	}
	parts := sqlanalyzer.FlattenConjunction(where.Expr, nil, splitAnd, nil)
	for _, part := range parts {
		cmp, ok := part.(*sqlparser.ComparisonExpr)
		if !ok || !sameTimeAxis(cmp.Left, bucket) {
			if referencesTimeAxis(part, bucket) {
				return rangeInfo{}, ErrUnsafePredicate
			}
			continue
		}
		inclusive := cmp.Operator == sqlparser.GreaterEqualOp || cmp.Operator == sqlparser.LessEqualOp
		bound, err := parseBound(cmp.Right, inclusive)
		if err != nil {
			return rangeInfo{}, err
		}
		switch cmp.Operator {
		case sqlparser.GreaterThanOp, sqlparser.GreaterEqualOp:
			if !inclusive {
				return rangeInfo{}, ErrUnsafePredicate
			}
			if out.lower != nil {
				return rangeInfo{}, ErrUnsafePredicate
			}
			out.lower = bound
		case sqlparser.LessThanOp, sqlparser.LessEqualOp:
			if inclusive {
				return rangeInfo{}, ErrUnsafePredicate
			}
			if out.upper != nil {
				return rangeInfo{}, ErrUnsafePredicate
			}
			out.upper = bound
		default:
			return rangeInfo{}, ErrUnsafePredicate
		}
	}
	if hasSecondaryTimeAxis(parts, bucket) {
		return rangeInfo{}, ErrAmbiguousTimeAxis
	}
	if out.lower == nil || out.upper == nil || out.lower.unit != out.upper.unit {
		return rangeInfo{}, ErrNotTimeRangeQuery
	}
	if out.lower.value.After(out.upper.value) {
		return rangeInfo{}, ErrUnsafePredicate
	}
	return out, nil
}

func referencesTimeAxis(expr sqlparser.Expr, bucket bucketInfo) bool {
	found := false
	_ = sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		expression, ok := node.(sqlparser.Expr)
		if ok && sameTimeAxis(expression, bucket) {
			found = true
			return false, nil
		}
		return !found, nil
	}, expr)
	return found
}

func hasSecondaryTimeAxis(parts []sqlparser.Expr, bucket bucketInfo) bool {
	axes := make(map[string]uint8)
	for _, part := range parts {
		cmp, ok := part.(*sqlparser.ComparisonExpr)
		if !ok {
			continue
		}
		_, axis, ok := columnReference(cmp.Left)
		if !ok {
			_, axis, ok = unixTimestampColumn(cmp.Left)
		}
		if !ok || strings.EqualFold(axis, bucket.timeAxis) || !plausibleTimeBound(cmp.Right) {
			continue
		}
		switch cmp.Operator {
		case sqlparser.GreaterEqualOp:
			axes[axis] |= 1
		case sqlparser.LessThanOp:
			axes[axis] |= 2
		}
	}
	for _, mask := range axes {
		if mask == 3 {
			return true
		}
	}
	return false
}

func plausibleTimeBound(expr sqlparser.Expr) bool {
	if f, ok := expr.(*sqlparser.FuncExpr); ok &&
		strings.EqualFold(f.Name.String(), fromUnixTimeFunction) {
		return true
	}
	value, ok := intLiteral(expr)
	return ok && (value >= 100_000_000 || value <= -100_000_000)
}

func splitAnd(expr sqlparser.Expr) (sqlparser.Expr, sqlparser.Expr, bool) {
	if and, ok := expr.(*sqlparser.AndExpr); ok {
		return and.Left, and.Right, true
	}
	return nil, nil, false
}

func sameTimeAxis(expr sqlparser.Expr, bucket bucketInfo) bool {
	if _, axis, ok := columnReference(expr); ok {
		return strings.EqualFold(axis, bucket.timeAxis)
	}
	if _, axis, ok := unixTimestampColumn(expr); ok {
		return strings.EqualFold(axis, bucket.timeAxis)
	}
	return false
}

func parseBound(expr sqlparser.Expr, inclusive bool) (*mysqlBound, error) {
	if f, ok := expr.(*sqlparser.FuncExpr); ok &&
		strings.EqualFold(f.Name.String(), fromUnixTimeFunction) && len(f.Exprs) == 1 {
		seconds, ok := intLiteral(f.Exprs[0])
		if !ok {
			return nil, ErrUnsafePredicate
		}
		if !sqlanalyzer.SafeUnixSeconds(seconds) {
			return nil, ErrUnsafePredicate
		}
		return &mysqlBound{
			value: time.Unix(seconds, 0).UTC(), inclusive: inclusive,
			unit: timeseries.DateTimeSQL, node: expr, style: boundFromUnixTime,
		}, nil
	}
	value, ok := intLiteral(expr)
	if !ok {
		return nil, ErrUnsafePredicate
	}
	if value >= 1_000_000_000_000 || value <= -1_000_000_000_000 {
		return &mysqlBound{
			value: time.Unix(0, value).UTC(), inclusive: inclusive,
			unit: timeseries.DateTimeUnixNano, node: expr, style: boundEpochNanos,
		}, nil
	}
	if !sqlanalyzer.SafeUnixSeconds(value) {
		return nil, ErrUnsafePredicate
	}
	return &mysqlBound{
		value: time.Unix(value, 0).UTC(), inclusive: inclusive,
		unit: timeseries.DateTimeUnixSecs, node: expr, style: boundEpochSeconds,
	}, nil
}

type selectOutput struct {
	expr       sqlparser.Expr
	name       string
	alias      string
	sourceName string
	sourceAxis string
	bucket     bool
}

func analyzeResultShape(stmt *sqlparser.Select, bucket bucketInfo) ([]string, []string, error) {
	outputs, bucketIndex, err := selectOutputs(stmt, bucket)
	if err != nil {
		return nil, nil, err
	}
	if stmt.GroupBy == nil || stmt.GroupBy.WithRollup {
		return nil, nil, ErrUnsupportedResultShape
	}
	groupCapacity := len(stmt.GroupBy.Exprs)
	if groupCapacity > 0 {
		groupCapacity--
	}
	groups := make([]string, 0, groupCapacity)
	groupIndexes := make(map[int]struct{}, len(stmt.GroupBy.Exprs))
	foundTime := false
	for _, expr := range stmt.GroupBy.Exprs {
		index, ok := resolveOutputReference(expr, outputs)
		if !ok {
			return nil, nil, ErrUnsupportedResultShape
		}
		if index == bucketIndex {
			foundTime = true
			groupIndexes[index] = struct{}{}
			continue
		}
		if outputs[index].sourceAxis == "" {
			return nil, nil, ErrUnsupportedResultShape
		}
		if _, duplicate := groupIndexes[index]; duplicate {
			return nil, nil, ErrUnsupportedResultShape
		}
		groupIndexes[index] = struct{}{}
		groups = append(groups, outputs[index].name)
	}
	if !foundTime {
		return nil, nil, ErrUnsupportedResultShape
	}
	values := make([]string, 0, len(outputs)-len(groupIndexes))
	for index, output := range outputs {
		if _, grouped := groupIndexes[index]; grouped {
			continue
		}
		if ok, aggregate := numericValueExpression(output.expr); !ok || !aggregate {
			return nil, nil, ErrUnsupportedResultShape
		}
		values = append(values, output.name)
	}
	if len(values) == 0 {
		return nil, nil, ErrUnsupportedResultShape
	}
	expectedOrder := make([]int, 1, len(groups)+1)
	expectedOrder[0] = bucketIndex
	for index := range outputs {
		if index != bucketIndex {
			if _, grouped := groupIndexes[index]; grouped {
				expectedOrder = append(expectedOrder, index)
			}
		}
	}
	if len(stmt.OrderBy) > len(expectedOrder) {
		return nil, nil, ErrUnsupportedResultShape
	}
	for i, order := range stmt.OrderBy {
		index, ok := resolveOutputReference(order.Expr, outputs)
		if !ok || order.Direction != sqlparser.AscOrder || index != expectedOrder[i] {
			return nil, nil, fmt.Errorf("%w: %w", ErrUnsupportedGrouping, ErrUnsupportedResultShape)
		}
	}
	return groups, values, nil
}

func selectOutputs(stmt *sqlparser.Select, bucket bucketInfo) ([]selectOutput, int, error) {
	if stmt.SelectExprs == nil {
		return nil, -1, ErrUnsupportedResultShape
	}
	outputs := make([]selectOutput, 0, len(stmt.SelectExprs.Exprs))
	seenNames := make(map[string]struct{}, len(stmt.SelectExprs.Exprs))
	bucketIndex := -1
	for _, item := range stmt.SelectExprs.Exprs {
		aliased, ok := item.(*sqlparser.AliasedExpr)
		if !ok {
			return nil, -1, ErrUnsupportedResultShape
		}
		alias := aliased.As.String()
		sourceName, sourceAxis, _ := columnReference(aliased.Expr)
		name := alias
		if name == "" {
			if sourceName != "" {
				name = sourceName
			} else {
				name = sqlparser.String(aliased.Expr)
			}
		}
		if name == "" {
			return nil, -1, ErrUnsupportedResultShape
		}
		key := strings.ToLower(name)
		if _, duplicate := seenNames[key]; duplicate {
			return nil, -1, ErrUnsupportedResultShape
		}
		seenNames[key] = struct{}{}
		_, axis, _, _, isBucket := matchBucketExpr(aliased.Expr)
		output := selectOutput{
			expr: aliased.Expr, name: name, alias: alias,
			sourceName: sourceName, sourceAxis: sourceAxis, bucket: isBucket,
		}
		if isBucket {
			if bucketIndex >= 0 || !strings.EqualFold(axis, bucket.timeAxis) ||
				!strings.EqualFold(name, bucket.outputColumn) {
				return nil, -1, ErrUnsupportedResultShape
			}
			bucketIndex = len(outputs)
		}
		outputs = append(outputs, output)
	}
	if bucketIndex < 0 {
		return nil, -1, ErrUnsupportedResultShape
	}
	return outputs, bucketIndex, nil
}

func resolveOutputReference(expr sqlparser.Expr, outputs []selectOutput) (int, bool) {
	if ordinal, ok := intLiteral(expr); ok {
		if ordinal <= 0 || ordinal > int64(len(outputs)) {
			return 0, false
		}
		return int(ordinal - 1), true
	}
	name, axis, ok := columnReference(expr)
	if !ok {
		return 0, false
	}
	for index, output := range outputs {
		if output.alias != "" && strings.EqualFold(name, output.alias) {
			return index, true
		}
	}
	for index, output := range outputs {
		if output.sourceAxis != "" && strings.EqualFold(axis, output.sourceAxis) {
			return index, true
		}
	}
	found := -1
	for index, output := range outputs {
		if output.sourceName != "" && strings.EqualFold(name, output.sourceName) {
			if found >= 0 {
				return 0, false
			}
			found = index
		}
	}
	return found, found >= 0
}

func numericValueExpression(expr sqlparser.Expr) (bool, bool) {
	if window, ok := expr.(sqlparser.WindowFunc); ok && window.GetOverClause() != nil {
		return false, false
	}
	if aggregate, ok := expr.(sqlparser.AggrFunc); ok {
		switch aggregate.AggrName() {
		case "avg", "count", "max", "min", "sum":
			return true, true
		default:
			return false, false
		}
	}
	switch node := expr.(type) {
	case *sqlparser.Literal:
		return node.Type == sqlparser.IntVal || node.Type == sqlparser.FloatVal ||
			node.Type == sqlparser.DecimalVal, false
	case *sqlparser.BinaryExpr:
		leftSafe, leftAggregate := numericValueExpression(node.Left)
		rightSafe, rightAggregate := numericValueExpression(node.Right)
		return leftSafe && rightSafe, leftAggregate || rightAggregate
	case *sqlparser.UnaryExpr:
		safe, aggregate := numericValueExpression(node.Expr)
		return safe, aggregate
	case *sqlparser.FuncExpr:
		switch strings.ToLower(node.Name.String()) {
		case "coalesce", "ifnull", "round":
			hasAggregate := false
			for _, argument := range node.Exprs {
				safe, aggregate := numericValueExpression(argument)
				if !safe {
					return false, false
				}
				hasAggregate = hasAggregate || aggregate
			}
			return true, hasAggregate
		}
	}
	return false, false
}

func columnReference(expr sqlparser.Expr) (string, string, bool) {
	col, ok := expr.(*sqlparser.ColName)
	if !ok {
		return "", "", false
	}
	name := col.Name.String()
	database := col.Qualifier.Qualifier.String()
	table := col.Qualifier.Name.String()
	var axis strings.Builder
	axis.Grow(len(database) + len(table) + len(name) + 2)
	axis.WriteString(strings.ToLower(database))
	axis.WriteByte(0)
	axis.WriteString(strings.ToLower(table))
	axis.WriteByte(0)
	axis.WriteString(strings.ToLower(name))
	return name, axis.String(), true
}

func unixTimestampColumn(expr sqlparser.Expr) (string, string, bool) {
	f, ok := expr.(*sqlparser.FuncExpr)
	if !ok || !strings.EqualFold(f.Name.String(), unixTimestampFunction) || len(f.Exprs) != 1 {
		return "", "", false
	}
	return columnReference(f.Exprs[0])
}

func intLiteral(expr sqlparser.Expr) (int64, bool) {
	if unary, ok := expr.(*sqlparser.UnaryExpr); ok {
		value, literal := intLiteral(unary.Expr)
		if !literal {
			return 0, false
		}
		switch unary.Operator {
		case sqlparser.UPlusOp:
			return value, true
		case sqlparser.UMinusOp:
			if value == -1<<63 {
				return 0, false
			}
			return -value, true
		default:
			return 0, false
		}
	}
	lit, ok := expr.(*sqlparser.Literal)
	if !ok || lit.Type != sqlparser.IntVal {
		return 0, false
	}
	v, err := strconv.ParseInt(lit.Val, 10, 64)
	return v, err == nil
}

const (
	lowerPlaceholder = "__trickster_mysql_lower"
	upperPlaceholder = "__trickster_mysql_upper"
)

type mysqlRenderer struct {
	template   sqlparser.Statement
	lowerToken string
	upperToken string
	lower      boundStyle
	upper      boundStyle
	step       time.Duration
	// QueryPlan extents are inclusive bucket starts. These flags retain the
	// source comparators so rendering a cache miss preserves their semantics.
	lowerInclusive bool
	upperInclusive bool
}

func buildArtifacts(stmt *sqlparser.Select, rng rangeInfo, step time.Duration) (string, *mysqlRenderer, error) {
	lowerName, upperName := uniqueTokens(stmt)
	replaced := 0
	template := sqlparser.CopyOnRewrite(stmt, nil, func(cursor *sqlparser.CopyOnWriteCursor) {
		expr, ok := cursor.Node().(sqlparser.Expr)
		if !ok {
			return
		}
		switch expr {
		case rng.lower.node:
			cursor.Replace(sqlparser.NewArgument(lowerName))
			replaced++
		case rng.upper.node:
			cursor.Replace(sqlparser.NewArgument(upperName))
			replaced++
		}
	}, nil).(*sqlparser.Select)
	if replaced != 2 {
		return "", nil, ErrUnsafePredicate
	}
	return sqlparser.String(template), &mysqlRenderer{
		template:   template,
		lowerToken: lowerName, upperToken: upperName,
		lower: rng.lower.style, upper: rng.upper.style,
		lowerInclusive: rng.lower.inclusive, upperInclusive: rng.upper.inclusive,
		step: step,
	}, nil
}

func uniqueTokens(stmt sqlparser.Statement) (string, string) {
	used := make(map[string]struct{})
	_ = sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		if arg, ok := node.(*sqlparser.Argument); ok {
			used[arg.Name] = struct{}{}
		}
		return true, nil
	}, stmt)
	unique := func(base string) string {
		name := base
		for suffix := 2; ; suffix++ {
			if _, exists := used[name]; !exists {
				used[name] = struct{}{}
				return name
			}
			name = base + strconv.Itoa(suffix)
		}
	}
	return unique(lowerPlaceholder), unique(upperPlaceholder)
}

func (r *mysqlRenderer) RenderExtent(extent timeseries.Extent) (string, error) {
	lower := extent.Start
	if !r.lowerInclusive {
		lower = lower.Add(-r.step)
	}
	upper := extent.End
	if !r.upperInclusive {
		upper = upper.Add(r.step)
	}
	return r.renderTimeRange(lower, upper)
}

func (r *mysqlRenderer) renderTimeRange(lower, upper time.Time) (string, error) {
	stmt := sqlparser.CloneSQLNode(r.template).(sqlparser.Statement)
	stmt = sqlparser.Rewrite(stmt, func(cursor *sqlparser.Cursor) bool {
		arg, ok := cursor.Node().(*sqlparser.Argument)
		if !ok {
			return true
		}
		switch arg.Name {
		case r.lowerToken:
			cursor.Replace(renderBound(lower, r.lower))
		case r.upperToken:
			cursor.Replace(renderBound(upper, r.upper))
		}
		return false
	}, nil).(sqlparser.Statement)
	return sqlparser.String(stmt), nil
}

func renderBound(value time.Time, style boundStyle) sqlparser.Expr {
	switch style {
	case boundEpochNanos:
		return sqlparser.NewIntLiteral(strconv.FormatInt(value.UnixNano(), 10))
	case boundFromUnixTime:
		return &sqlparser.FuncExpr{
			Name:  sqlparser.NewIdentifierCI("FROM_UNIXTIME"),
			Exprs: []sqlparser.Expr{sqlparser.NewIntLiteral(strconv.FormatInt(value.Unix(), 10))},
		}
	default:
		return sqlparser.NewIntLiteral(strconv.FormatInt(value.Unix(), 10))
	}
}
