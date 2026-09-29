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

// Package aftership adapts the AfterShip ClickHouse parser to Trickster's SQL analysis contract.
package aftership

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/contenttype"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/directives"

	chast "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// Options configures an Analyzer.
type Options struct{}

// Analyzer produces ClickHouse cache plans and is safe for concurrent use.
// Its zero value is ready to use.
type Analyzer struct {
	opts Options
}

var _ sqlanalyzer.DialectAnalyzer = (*Analyzer)(nil)

// NewAnalyzer returns a ClickHouse dialect analyzer.
func NewAnalyzer(opts Options) *Analyzer {
	return &Analyzer{opts: opts}
}

// IsSelectQuery reports whether the SQL query contains a SELECT keyword.
func IsSelectQuery(statement string) bool {
	return slices.Contains(strings.Fields(strings.ToLower(statement)), "select")
}

func parseSelect(statement string) (*chast.SelectQuery, string, error) {
	normalized := maskLegacyLineComments(statement)
	statements, err := chast.NewParser(normalized).ParseStmts()
	if err != nil {
		return nil, normalized, fmt.Errorf("%w: %w", ErrInvalidSQL, err)
	}
	if len(statements) != 1 {
		return nil, normalized, fmt.Errorf("%w: expected one statement, got %d", ErrInvalidSQL, len(statements))
	}
	selectQuery, ok := statements[0].(*chast.SelectQuery)
	if !ok {
		return nil, normalized, ErrUnsupportedStatement
	}
	return selectQuery, normalized, nil
}

func maskLegacyLineComments(statement string) string {
	source := []byte(statement)
	var quote byte
	inBlockComment := false
	for i := 0; i < len(source); i++ {
		if inBlockComment {
			if source[i] == '*' && i+1 < len(source) && source[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if quote != 0 {
			if source[i] == '\\' {
				i++
				continue
			}
			if source[i] == quote {
				if i+1 < len(source) && source[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch {
		case source[i] == '/' && i+1 < len(source) && source[i+1] == '*':
			inBlockComment = true
			i++
		case source[i] == '\'' || source[i] == '"' || source[i] == '`':
			quote = source[i]
		case source[i] == '/' && i+1 < len(source) && source[i+1] == '/':
			for i < len(source) && source[i] != '\n' && source[i] != '\r' {
				source[i] = ' '
				i++
			}
		}
	}
	return string(source)
}

func (a *Analyzer) Analyze(statement string, now time.Time) sqlanalyzer.Analysis {
	if strings.TrimSpace(statement) == "" {
		return sqlanalyzer.Analysis{Reason: sqlanalyzer.ReasonInvalidSQL, Err: ErrNotTimeRangeQuery}
	}

	selectQuery, _, err := parseSelect(statement)
	if err != nil {
		reason := sqlanalyzer.ReasonInvalidSQL
		if errors.Is(err, ErrUnsupportedStatement) {
			reason = sqlanalyzer.ReasonUnsupportedStatement
			return sqlanalyzer.Analysis{Mode: sqlanalyzer.CacheModeNone, Reason: reason, Err: err}
		}
		mode := sqlanalyzer.CacheModeNone
		if IsSelectQuery(statement) {
			mode = sqlanalyzer.CacheModeObject
		}
		return sqlanalyzer.Analysis{Mode: mode, Reason: reason, Err: err}
	}
	if hasSetOperation(selectQuery) {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedStatement, ErrUnsupportedStatement)
	}
	if selectQuery.Limit != nil || selectQuery.LimitBy != nil || selectQuery.Top != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedLimit, ErrLimitUnsupported)
	}
	// a sub-range fetch of these shapes computes different bucket values than the full query
	if err := checkBucketLocality(selectQuery); err != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedFormat, err)
	}

	constants := collectConstants(selectQuery.With)
	bucket, err := analyzeSelectList(selectQuery.SelectItems, constants)
	if err != nil {
		reason := sqlanalyzer.ReasonUnsupportedBucket
		if errors.Is(err, ErrMissingTimeseries) {
			reason = sqlanalyzer.ReasonNotTimeRange
		} else if errors.Is(err, ErrAmbiguousTimeAxis) {
			reason = sqlanalyzer.ReasonAmbiguousTimeAxis
		}
		return sqlanalyzer.ObjectAnalysis(reason, err)
	}
	groups, err := analyzeGroupBy(selectQuery.GroupBy, selectQuery.SelectItems, bucket)
	if err != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedGrouping, err)
	}
	if !ordersByBucket(selectQuery.OrderBy, selectQuery.SelectItems, bucket) {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedOrdering, ErrUnsupportedOrdering)
	}
	ranges, err := analyzeRanges(selectQuery, bucket, constants, now)
	if err != nil {
		reason := sqlanalyzer.ReasonNotTimeRange
		if errors.Is(err, ErrUnsafePredicate) {
			reason = sqlanalyzer.ReasonUnsafePredicate
		} else if errors.Is(err, ErrAmbiguousTimeAxis) {
			reason = sqlanalyzer.ReasonAmbiguousTimeAxis
		}
		return sqlanalyzer.ObjectAnalysis(reason, err)
	}
	outputFormat, err := analyzeFormat(selectQuery.Format)
	if err != nil {
		return sqlanalyzer.ObjectAnalysis(sqlanalyzer.ReasonUnsupportedFormat, err)
	}

	canonical, renderer := buildQueryArtifacts(selectQuery, ranges, bucket.step)
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
		RawLower:     &ranges.rawLower,
		RawUpper:     ranges.rawUpper,
		UpperIsNow:   ranges.upperIsNow,
		GroupColumns: groups,
		OutputFormat: outputFormat,
		Renderer:     renderer,
		Directives:   directives.Parse(statement, directives.SyntaxClickHouse),
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

func hasSetOperation(query *chast.SelectQuery) bool {
	return query.UnionAll != nil || query.UnionDistinct != nil || query.Except != nil || query.Intersect != nil
}

func checkBucketLocality(query *chast.SelectQuery) error {
	var err error
	tables := 0
	chast.Walk(query, func(node chast.Expr) bool {
		switch value := node.(type) {
		case *chast.SelectQuery:
			if value != query {
				err = ErrNestedQuery
			} else if value.WithTotal {
				err = ErrCrossRowQuery
			}
		case *chast.SubQuery:
			err = ErrNestedQuery
		case *chast.JoinTableExpr:
			// ARRAY JOIN expands rows in place, so only a second table source is a join
			tables++
			if tables > 1 {
				err = ErrNestedQuery
			}
		case *chast.GroupByClause:
			if value.WithTotals {
				err = ErrCrossRowQuery
			}
		case *chast.WindowClause, *chast.WindowExpr, *chast.WindowFunctionExpr,
			*chast.Fill, *chast.InterpolateClause:
			err = ErrCrossRowQuery
		case *chast.SettingsClause:
			err = ErrQuerySettings
		case *chast.FunctionExpr:
			if value.Name != nil && isCrossRowFunction(value.Name.Name) {
				err = ErrCrossRowQuery
			}
		}
		return err == nil
	})
	return err
}

func isCrossRowFunction(name string) bool {
	if len(name) > len(crossRowPrefix) && strings.EqualFold(name[:len(crossRowPrefix)], crossRowPrefix) {
		return true
	}
	for _, function := range crossRowFunctions {
		if len(name) == len(function) && strings.EqualFold(name, function) {
			return true
		}
	}
	return false
}

func ordersByBucket(clause *chast.OrderByClause, items []*chast.SelectItem, bucket bucketSpec) bool {
	if clause == nil {
		return true
	}
	// the response model emits rows ascending by bucket with ties in series order, so a
	// single ascending bucket term is the only ordering it reproduces
	if len(clause.Items) != 1 {
		return false
	}
	order, ok := clause.Items[0].(*chast.OrderExpr)
	if !ok || order.Alias != nil || order.Fill != nil ||
		(order.Direction != chast.OrderDirectionNone && order.Direction != chast.OrderDirectionAsc) {
		return false
	}
	switch value := unwrapColumnExpr(order.Expr).(type) {
	case *chast.Ident:
		return value.Name == bucket.outputColumn
	case *chast.NumberLiteral:
		position, err := strconv.Atoi(value.Literal)
		if value.Base != 10 || err != nil || position != bucket.index+1 {
			return false
		}
		return !slices.ContainsFunc(items[:bucket.index], expandsColumns)
	default:
		return expressionKey(value) == expressionKey(items[bucket.index].Expr)
	}
}

func expandsColumns(item *chast.SelectItem) bool {
	// a star or COLUMNS matcher can yield several result columns, shifting later positions
	expands := false
	chast.Walk(item, func(node chast.Expr) bool {
		switch value := node.(type) {
		case *chast.Ident:
			expands = value.Name == starColumn
		case *chast.FunctionExpr:
			expands = value.Name != nil && strings.EqualFold(value.Name.Name, columnsFunction)
		}
		return !expands
	})
	return expands
}

type clickHouseRenderer struct {
	template string
	// openTemplate is the template without the upper bound added for a statement that has none
	openTemplate   string
	explicitFormat bool
	bounds         []rendererBound
	step           time.Duration
	// upperTick is the resolution of an inclusive upper bound, which renders one tick below the
	// exclusive boundary
	upperTick time.Duration
}

func (r *clickHouseRenderer) RenderExtent(extent timeseries.Extent) (string, error) {
	statement := r.template
	for _, bound := range r.bounds {
		statement = strings.ReplaceAll(statement, bound.token, bound.render(extent))
	}
	return statement, nil
}

// RenderRange implements sqlanalyzer.RangeRenderer.
func (r *clickHouseRenderer) RenderRange(pb timeseries.PartialBucket) (string, error) {
	// raw lower bounds are inclusive, and only an inclusive upper bound yields an inclusive range
	if pb.LowerExclusive || pb.UpperInclusive && r.upperTick == 0 {
		return "", sqlanalyzer.ErrUnsupportedRange
	}
	open := pb.Upper.IsZero()
	statement := r.template
	if open && r.openTemplate != "" {
		statement = r.openTemplate
	}
	end := pb.Upper
	if pb.UpperInclusive {
		end = end.Add(r.upperTick)
	}
	// with its last label a step before the range's exclusive end, each bound's offset renders
	// the range through that bound's own comparator
	extent := timeseries.Extent{Start: pb.Lower, End: end.Add(-r.step)}
	for _, bound := range r.bounds {
		replacement := bound.original
		if !open || bound.endpoint == endpointLower {
			replacement = bound.render(extent)
		} else if replacement == "" {
			if !strings.Contains(statement, bound.token) {
				continue
			}
			return "", sqlanalyzer.ErrUnsupportedRange
		}
		statement = strings.ReplaceAll(statement, bound.token, replacement)
	}
	return statement, nil
}

type rendererBound struct {
	token    string
	endpoint endpoint
	style    boundStyle
	offset   time.Duration
	// original is a bare now() upper bound as written, which a range running to now keeps
	original string
}

func (b rendererBound) render(extent timeseries.Extent) string {
	if b.endpoint == endpointLower {
		extent.Start = extent.Start.Add(b.offset)
	} else {
		extent.End = extent.End.Add(b.offset)
	}
	return chast.Format(boundExpression(b.endpoint, b.style, extent))
}

const (
	day                = 24 * time.Hour
	week               = 7 * day
	toDateTimeFunction = "todatetime"
	crossRowPrefix     = "running"
	columnsFunction    = "columns"
	starColumn         = "*"
)

var crossRowFunctions = [...]string{
	"neighbor", "lagInFrame", "leadInFrame",
	"rowNumberInBlock", "rowNumberInAllBlocks", "blockNumber", "blockSize",
}

var fixedBucketDurations = map[string]time.Duration{
	"tomonday":                week,
	"tostartofday":            day,
	"tostartofhour":           time.Hour,
	"tostartofminute":         time.Minute,
	"tostartofsecond":         time.Second,
	"tostartofmillisecond":    time.Millisecond,
	"tostartofmicrosecond":    time.Microsecond,
	"tostartofnanosecond":     time.Nanosecond,
	"timeslot":                30 * time.Minute,
	"tostartofweek":           week,
	"tostartoffiveminute":     5 * time.Minute,
	"tostartoftenminutes":     10 * time.Minute,
	"tostartoffifteenminutes": 15 * time.Minute,
}

var intervalDurations = map[string]time.Duration{
	"nanosecond":  time.Nanosecond,
	"microsecond": time.Microsecond,
	"millisecond": time.Millisecond,
	"second":      time.Second,
	"minute":      time.Minute,
	"hour":        time.Hour,
	"day":         day,
	"week":        week,
}

var supportedFormats = map[string]byte{
	"native":                        6,
	contenttype.JSON:                0,
	contenttype.CSV:                 1,
	"csvwithnames":                  2,
	"tabseparated":                  3,
	contenttype.TSV:                 3,
	"tabseparatedwithnames":         4,
	"tsvwithnames":                  4,
	"tabseparatedwithnamesandtypes": 5,
	"tsvwithnamesandtypes":          5,
}

type bucketSpec struct {
	timeColumn   string
	outputColumn string
	index        int
	step         time.Duration
	phase        time.Duration
	outputUnit   timeseries.FieldDataType
}

func analyzeSelectList(items []*chast.SelectItem, constants map[string]int64) (bucketSpec, error) {
	var found *bucketSpec
	var rejected bool
	for i, item := range items {
		if item == nil || item.Expr == nil {
			continue
		}
		bucket, ok := matchBucket(item.Expr, constants)
		if !ok {
			// a bucket function the matcher cannot support is a different
			// failure than a select list with no time axis at all
			rejected = rejected || containsBucketCandidate(item.Expr)
			continue
		}
		if found != nil {
			return bucketSpec{}, ErrAmbiguousTimeAxis
		}
		bucket.index = i
		if item.Alias != nil {
			bucket.outputColumn = item.Alias.Name
		} else {
			bucket.outputColumn = chast.Format(item.Expr)
		}
		found = &bucket
	}
	if found == nil {
		if rejected {
			return bucketSpec{}, ErrUnsupportedBucket
		}
		return bucketSpec{}, ErrMissingTimeseries
	}
	return *found, nil
}

// bucketFunctionNames are the non-fixed-duration function names that indicate
// an attempt to bucket a time axis, supported by the matcher or not.
var bucketFunctionNames = map[string]struct{}{
	"date_trunc": {}, "datetrunc": {}, "tostartofinterval": {}, "intdiv": {},
}

func isBucketFunctionName(name string) bool {
	if _, ok := fixedBucketDurations[name]; ok {
		return true
	}
	_, ok := bucketFunctionNames[name]
	return ok
}

// containsBucketCandidate reports whether an expression references a bucket
// function at any depth, including under casts the matcher does not unwrap.
func containsBucketCandidate(expression chast.Expr) bool {
	switch value := unwrapColumnExpr(expression).(type) {
	case *chast.FunctionExpr:
		if value.Name != nil && isBucketFunctionName(strings.ToLower(value.Name.Name)) {
			return true
		}
		if slices.ContainsFunc(functionArgs(value), containsBucketCandidate) {
			return true
		}
	case *chast.BinaryOperation:
		return containsBucketCandidate(value.LeftExpr) ||
			containsBucketCandidate(value.RightExpr)
	}
	return false
}

func matchBucket(expression chast.Expr, constants map[string]int64) (bucketSpec, bool) {
	expression, converted := unwrapBucketExpression(expression)
	if function, ok := expression.(*chast.FunctionExpr); ok && function.Name != nil {
		name := strings.ToLower(function.Name.Name)
		if step, exists := fixedBucketDurations[name]; exists {
			args := functionArgs(function)
			if len(args) != 1 {
				return bucketSpec{}, false
			}
			column, ok := sourceColumn(args[0])
			if !ok {
				return bucketSpec{}, false
			}
			if converted && step < time.Second {
				return bucketSpec{}, false
			}
			unit := timeseries.DateTimeSQL
			if name == "tomonday" || name == "tostartofweek" {
				unit = timeseries.DateSQL
			} else if converted {
				unit = timeseries.DateTimeUnixSecs
			}
			phase := time.Duration(0)
			switch name {
			case "tomonday":
				phase = 4 * day
			case "tostartofweek":
				phase = 3 * day
			}
			return bucketSpec{
				timeColumn: column, step: step, phase: phase, outputUnit: unit,
			}, true
		}
		if name == "date_trunc" || name == "datetrunc" {
			args := functionArgs(function)
			if len(args) != 2 {
				return bucketSpec{}, false
			}
			unit, ok := unwrapColumnExpr(args[0]).(*chast.StringLiteral)
			if !ok {
				return bucketSpec{}, false
			}
			step, ok := intervalDurations[strings.ToLower(unit.Literal)]
			if !ok {
				return bucketSpec{}, false
			}
			column, ok := sourceColumn(args[1])
			if !ok {
				return bucketSpec{}, false
			}
			phase := time.Duration(0)
			if strings.EqualFold(unit.Literal, "week") {
				phase = 4 * day
			}
			return bucketSpec{timeColumn: column, step: step, phase: phase, outputUnit: timeseries.DateTimeSQL}, true
		}
		if name == "tostartofinterval" {
			args := functionArgs(function)
			if len(args) != 2 {
				return bucketSpec{}, false
			}
			column, ok := sourceColumn(args[0])
			if !ok {
				return bucketSpec{}, false
			}
			interval, ok := unwrapColumnExpr(args[1]).(*chast.IntervalExpr)
			if !ok || interval.Unit == nil {
				return bucketSpec{}, false
			}
			count, countOK := evalInteger(interval.Expr, constants)
			unit, unitOK := intervalDurations[strings.ToLower(interval.Unit.Name)]
			if !countOK || !unitOK || count <= 0 || count > int64((1<<63-1)/unit) {
				return bucketSpec{}, false
			}
			phase := time.Duration(0)
			if strings.EqualFold(interval.Unit.Name, "week") {
				// ClickHouse anchors interval weeks on Monday, 1970-01-05.
				phase = 4 * day
			}
			return bucketSpec{
				timeColumn: column,
				step:       time.Duration(count) * unit,
				phase:      phase,
				outputUnit: timeseries.DateTimeSQL,
			}, true
		}
	}
	return matchIntDivBucket(expression, constants)
}

func unwrapBucketExpression(expression chast.Expr) (chast.Expr, bool) {
	converted := false
	for {
		expression = unwrapColumnExpr(expression)
		function, ok := expression.(*chast.FunctionExpr)
		if !ok || function.Name == nil {
			return expression, converted
		}
		name := strings.ToLower(function.Name.Name)
		if name != "toint32" && name != "touint32" {
			return expression, converted
		}
		args := functionArgs(function)
		if len(args) != 1 {
			return expression, converted
		}
		converted = true
		expression = args[0]
	}
}

func matchIntDivBucket(expression chast.Expr, constants map[string]int64) (bucketSpec, bool) {
	factors := flattenMultiplication(expression, nil)
	var intDiv *chast.FunctionExpr
	intDivIndex := -1
	for i, factor := range factors {
		function, ok := unwrapColumnExpr(factor).(*chast.FunctionExpr)
		if ok && function.Name != nil && strings.EqualFold(function.Name.Name, "intDiv") {
			if intDiv != nil {
				return bucketSpec{}, false
			}
			intDiv = function
			intDivIndex = i
		}
	}
	if intDiv == nil {
		return bucketSpec{}, false
	}
	args := functionArgs(intDiv)
	if len(args) != 2 {
		return bucketSpec{}, false
	}
	column, ok := sourceColumn(args[0])
	if !ok {
		return bucketSpec{}, false
	}
	stepSeconds, ok := evalInteger(args[1], constants)
	if !ok || stepSeconds <= 0 {
		return bucketSpec{}, false
	}

	consumedStep := false
	outputMultiplier := int64(1)
	for i, factor := range factors {
		if i == intDivIndex {
			continue
		}
		value, valueOK := evalInteger(factor, constants)
		if !valueOK {
			return bucketSpec{}, false
		}
		if !consumedStep && value == stepSeconds {
			consumedStep = true
			continue
		}
		outputMultiplier *= value
	}
	if !consumedStep {
		return bucketSpec{}, false
	}
	outputUnit, ok := outputUnitForMultiplier(outputMultiplier)
	if !ok {
		return bucketSpec{}, false
	}
	return bucketSpec{
		timeColumn: column,
		step:       time.Duration(stepSeconds) * time.Second,
		outputUnit: outputUnit,
	}, true
}

func flattenMultiplication(expression chast.Expr, out []chast.Expr) []chast.Expr {
	expression = unwrapColumnExpr(expression)
	multiply, ok := expression.(*chast.BinaryOperation)
	if !ok || multiply.Operation != chast.TokenKindMul {
		return append(out, expression)
	}
	out = flattenMultiplication(multiply.LeftExpr, out)
	return flattenMultiplication(multiply.RightExpr, out)
}

func outputUnitForMultiplier(multiplier int64) (timeseries.FieldDataType, bool) {
	switch multiplier {
	case 1:
		return timeseries.DateTimeUnixSecs, true
	case 1_000:
		return timeseries.DateTimeUnixMilli, true
	case 1_000_000:
		return timeseries.DateTimeUnixMicro, true
	case 1_000_000_000:
		return timeseries.DateTimeUnixNano, true
	default:
		return timeseries.Unknown, false
	}
}

func functionArgs(function *chast.FunctionExpr) []chast.Expr {
	if function == nil || function.Params == nil || function.Params.Items == nil {
		return nil
	}
	return function.Params.Items.Items
}

func sourceColumn(expression chast.Expr) (string, bool) {
	expression = unwrapColumnExpr(expression)
	if function, ok := expression.(*chast.FunctionExpr); ok && function.Name != nil {
		name := strings.ToLower(function.Name.Name)
		if name == "toint32" || name == "touint32" || name == toDateTimeFunction {
			args := functionArgs(function)
			if len(args) == 1 {
				return sourceColumn(args[0])
			}
		}
	}
	switch identifier := expression.(type) {
	case *chast.Ident:
		return identifier.Name, true
	case *chast.NestedIdentifier:
		return chast.Format(identifier), true
	case *chast.Path:
		if len(identifier.Fields) > 0 {
			return chast.Format(identifier), true
		}
	default:
		return "", false
	}
	return "", false
}

func unwrapColumnExpr(expression chast.Expr) chast.Expr {
	for {
		switch value := expression.(type) {
		case *chast.ColumnExpr:
			if value.Expr == nil || value.Alias != nil {
				return expression
			}
			expression = value.Expr
		case *chast.ParamExprList:
			if value.ColumnArgList != nil || value.Items == nil || len(value.Items.Items) != 1 {
				return expression
			}
			expression = value.Items.Items[0]
		default:
			return expression
		}
	}
}

func collectConstants(clause *chast.WithClause) map[string]int64 {
	constants := make(map[string]int64)
	if clause == nil {
		return constants
	}
	for _, cte := range clause.CTEs {
		if cte == nil {
			continue
		}
		alias, ok := sourceColumn(cte.Alias)
		if !ok {
			continue
		}
		value, ok := evalInteger(cte.Expr, constants)
		if ok {
			constants[alias] = value
		}
	}
	return constants
}

func evalInteger(expression chast.Expr, constants map[string]int64) (int64, bool) {
	expression = unwrapColumnExpr(expression)
	switch value := expression.(type) {
	case *chast.NumberLiteral:
		integer, err := strconv.ParseInt(value.Literal, value.Base, 64)
		return integer, err == nil
	case *chast.Ident, *chast.NestedIdentifier:
		name, ok := sourceColumn(value)
		if !ok {
			return 0, false
		}
		integer, exists := constants[name]
		return integer, exists
	case *chast.BinaryOperation:
		left, leftOK := evalInteger(value.LeftExpr, constants)
		right, rightOK := evalInteger(value.RightExpr, constants)
		if !leftOK || !rightOK {
			return 0, false
		}
		switch value.Operation {
		case chast.TokenKindPlus:
			return left + right, true
		case chast.TokenKindMinus:
			return left - right, true
		}
	}
	return 0, false
}

func analyzeGroupBy(
	clause *chast.GroupByClause,
	selectItems []*chast.SelectItem,
	bucket bucketSpec,
) ([]string, error) {
	if clause == nil || clause.Expr == nil || clause.AggregateType != "" || clause.WithCube || clause.WithRollup {
		return nil, ErrInvalidGroupByClause
	}

	timestampKeys := make(map[string]struct{}, 2)
	tagOutputs := make(map[string]string)
	requiredTags := make(map[string]struct{})
	for _, item := range selectItems {
		if item == nil || item.Expr == nil {
			continue
		}
		if (item.Alias != nil && identifierKey(item.Alias.Name) == identifierKey(bucket.outputColumn)) ||
			expressionKey(item.Expr) == identifierKey(bucket.outputColumn) {
			if item.Alias != nil {
				timestampKeys[identifierKey(item.Alias.Name)] = struct{}{}
			}
			timestampKeys[expressionKey(item.Expr)] = struct{}{}
			continue
		}
		name, ok := simpleColumn(item.Expr)
		if !ok {
			continue
		}
		output := name
		if item.Alias != nil {
			output = item.Alias.Name
			tagOutputs[identifierKey(item.Alias.Name)] = output
		}
		tagOutputs[identifierKey(name)] = output
		tagOutputs[expressionKey(item.Expr)] = output
		requiredTags[output] = struct{}{}
	}

	items := expressionItems(clause.Expr)
	groups := make([]string, 0, len(items))
	seenTags := make(map[string]struct{}, len(items))
	timestampGrouped := false
	for _, item := range items {
		key := expressionKey(item)
		if _, ok := timestampKeys[key]; ok {
			timestampGrouped = true
			continue
		}
		if name, ok := simpleColumn(item); ok {
			key = identifierKey(name)
			if _, timestamp := timestampKeys[key]; timestamp {
				timestampGrouped = true
				continue
			}
		}
		output, ok := tagOutputs[key]
		if !ok {
			return nil, ErrInvalidGroupByClause
		}
		if _, duplicate := seenTags[output]; !duplicate {
			groups = append(groups, output)
			seenTags[output] = struct{}{}
		}
	}
	if !timestampGrouped || len(seenTags) != len(requiredTags) {
		return nil, ErrInvalidGroupByClause
	}
	return groups, nil
}

func simpleColumn(expression chast.Expr) (string, bool) {
	expression = unwrapColumnExpr(expression)
	switch identifier := expression.(type) {
	case *chast.Ident:
		return identifier.Name, true
	case *chast.NestedIdentifier:
		return chast.Format(identifier), true
	case *chast.Path:
		if len(identifier.Fields) > 0 {
			return chast.Format(identifier), true
		}
	default:
		return "", false
	}
	return "", false
}

func expressionKey(expression chast.Expr) string {
	return chast.Format(unwrapColumnExpr(expression))
}

func identifierKey(name string) string {
	return name
}

func expressionItems(expression chast.Expr) []chast.Expr {
	expression = unwrapColumnExpr(expression)
	if list, ok := expression.(*chast.ColumnExprList); ok {
		return list.Items
	}
	return []chast.Expr{expression}
}

type boundStyle uint8

const (
	boundUnixSeconds boundStyle = iota
	boundUnixMilli
	boundUnixMicro
	boundUnixNano
	boundSQLDateTime
	boundToDateTime
	boundToDate
	boundToDateTime64
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
	set      func(chast.Expr)
	original string
}

type analyzedBound struct {
	value     time.Time
	inclusive bool
	style     boundStyle
	target    *boundTarget
	// now reports a bound written as a bare now() or now64()
	now bool
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
	addSynthetic func(chast.Expr)
	timeColumn   string
	lowerStyle   boundStyle
	rawLower     sqlanalyzer.Bound
	rawUpper     *sqlanalyzer.Bound
	upperIsNow   bool
}

func (r *rangeAnalysis) recordRaw() {
	r.rawLower = sqlanalyzer.Bound{Value: r.lower.value, Inclusive: r.lower.inclusive}
	if r.upper != nil {
		r.rawUpper = &sqlanalyzer.Bound{Value: r.upper.value, Inclusive: r.upper.inclusive}
	}
}

func analyzeRanges(
	query *chast.SelectQuery,
	bucket bucketSpec,
	constants map[string]int64,
	now time.Time,
) (rangeAnalysis, error) {
	result := rangeAnalysis{timeColumn: bucket.timeColumn}
	var predicates []predicateBound
	clauses := make([]chast.Expr, 0, 2)
	if query.Prewhere != nil {
		clauses = append(clauses, query.Prewhere.Expr)
	}
	if query.Where != nil {
		clauses = append(clauses, query.Where.Expr)
	}
	if len(clauses) == 0 {
		return result, ErrNotTimeRangeQuery
	}
	for _, clause := range clauses {
		conditions, err := flattenConjunction(clause)
		if err != nil {
			return result, err
		}
		for _, condition := range conditions {
			predicate, ok, err := analyzePredicate(condition, bucket, constants, now)
			if err != nil {
				return result, err
			}
			if ok {
				predicates = append(predicates, predicate)
			}
		}
	}

	primaryFields := []string{bucket.timeColumn, bucket.outputColumn}
	for _, predicate := range predicates {
		if !slices.Contains(primaryFields, predicate.field) {
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
		return result, fmt.Errorf("%w: bucket field %q did not match a lower range predicate",
			ErrNoLowerBound, bucket.timeColumn)
	}
	result.lowerStyle = result.lower.style
	result.targets = append(result.targets, result.lower.target)
	if result.upper != nil {
		result.targets = append(result.targets, result.upper.target)
	} else if query.Where != nil {
		result.addSynthetic = func(expression chast.Expr) {
			query.Where.Expr = &chast.BinaryOperation{
				LeftExpr: query.Where.Expr, Operation: chast.TokenKind(chast.KeywordAnd), RightExpr: expression,
			}
		}
	} else if query.Prewhere != nil {
		result.addSynthetic = func(expression chast.Expr) {
			query.Prewhere.Expr = &chast.BinaryOperation{
				LeftExpr: query.Prewhere.Expr, Operation: chast.TokenKind(chast.KeywordAnd), RightExpr: expression,
			}
		}
	} else {
		return result, ErrNoUpperBound
	}

	for _, predicate := range predicates {
		if slices.Contains(primaryFields, predicate.field) || !looksLikeTimeColumn(predicate.field) {
			continue
		}
		if predicate.lower != nil && predicate.lower.value.Equal(result.lower.value) {
			result.targets = append(result.targets, predicate.lower.target)
		}
		if result.upper != nil && predicate.upper != nil && predicate.upper.value.Equal(result.upper.value) {
			result.targets = append(result.targets, predicate.upper.target)
		}
	}
	// keep the bounds as the statement wrote them, before rounding them to the grid
	result.recordRaw()
	if err := normalizePrimaryBounds(&result, bucket); err != nil {
		return result, err
	}
	return result, nil
}

func normalizePrimaryBounds(result *rangeAnalysis, bucket bucketSpec) error {
	// rounds unaligned raw bounds inward (kept raw for the planner), so no partial bucket is cached
	// whole; an inclusive upper floors, and output-column bounds shift a step
	rounded := false
	if bucket.step < time.Second {
		for _, target := range result.targets {
			if target.style == boundUnixSeconds || target.style == boundToDateTime {
				target.style = boundToDateTime64 + 9
			}
		}
		if result.lowerStyle == boundUnixSeconds || result.lowerStyle == boundToDateTime {
			result.lowerStyle = boundToDateTime64 + 9
		}
	}

	lowerOnOutput := result.lower.target != nil && result.lower.target.field == bucket.outputColumn
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
		}
	}

	if result.upper == nil {
		return nil
	}
	upperOnOutput := result.upper.target != nil && result.upper.target.field == bucket.outputColumn
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
		style, tick, ok := inclusiveUpperRender(result.upper.target.style)
		if !ok || tick > bucket.step {
			return ErrUnsafePredicate
		}
		if !timeseries.OnGrid(result.upper.value, bucket.step, bucket.phase) {
			result.upper.value = timeseries.FloorToGrid(result.upper.value, bucket.step, bucket.phase)
		}
		// col <= X reaches at most the first instant of the bucket holding X,
		// so that bucket is partial; the floored value is the exclusive
		// equivalent, and the rendered literal sits one tick below it.
		result.upper.inclusive = false
		result.upper.target.style = style
		result.upper.target.offset = bucket.step - tick
		rounded = true
	} else {
		if !timeseries.OnGrid(result.upper.value, bucket.step, bucket.phase) {
			result.upper.value = timeseries.FloorToGrid(result.upper.value, bucket.step, bucket.phase)
			rounded = true
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

// inclusiveUpperRender returns the literal style and resolution to use when
// rendering an inclusive upper bound, so the emitted value is exactly one tick
// below the exclusive boundary. A style whose tick cannot be established, such
// as a bare date, fails closed.
func inclusiveUpperRender(style boundStyle) (boundStyle, time.Duration, bool) {
	if style >= boundToDateTime64 && style <= boundToDateTime64+9 {
		return boundToDateTime64 + 9, time.Nanosecond, true
	}
	switch style {
	case boundUnixSeconds:
		return boundUnixSeconds, time.Second, true
	case boundUnixMilli:
		return boundUnixMilli, time.Millisecond, true
	case boundUnixMicro:
		return boundUnixMicro, time.Microsecond, true
	case boundUnixNano:
		return boundUnixNano, time.Nanosecond, true
	case boundToDateTime, boundSQLDateTime:
		return boundToDateTime64 + 9, time.Nanosecond, true
	}
	return style, 0, false
}

func flattenConjunction(expression chast.Expr) ([]chast.Expr, error) {
	conditions := sqlanalyzer.FlattenConjunction(expression, unwrapColumnExpr, splitConjunction, nil)
	if slices.ContainsFunc(conditions, containsUnsafeBoolean) {
		return nil, ErrUnsafePredicate
	}
	return conditions, nil
}

func splitConjunction(expression chast.Expr) (chast.Expr, chast.Expr, bool) {
	binary, ok := expression.(*chast.BinaryOperation)
	if !ok || !strings.EqualFold(string(binary.Operation), chast.KeywordAnd) {
		return nil, nil, false
	}
	return binary.LeftExpr, binary.RightExpr, true
}

func containsUnsafeBoolean(expression chast.Expr) bool {
	unsafe := false
	chast.Walk(expression, func(node chast.Expr) bool {
		switch value := node.(type) {
		case *chast.NotExpr:
			unsafe = true
			return false
		case *chast.UnaryExpr:
			if strings.EqualFold(string(value.Kind), "NOT") {
				unsafe = true
				return false
			}
		case *chast.BinaryOperation:
			if strings.EqualFold(string(value.Operation), chast.KeywordOr) || value.HasNot {
				unsafe = true
				return false
			}
		case *chast.BetweenClause:
			if value.Not {
				unsafe = true
				return false
			}
		case *chast.FunctionExpr:
			if value.Name != nil && strings.EqualFold(value.Name.Name, "not") {
				unsafe = true
				return false
			}
		}
		return true
	})
	return unsafe
}

func analyzePredicate(
	expression chast.Expr,
	bucket bucketSpec,
	constants map[string]int64,
	now time.Time,
) (predicateBound, bool, error) {
	expression = unwrapColumnExpr(expression)
	switch value := expression.(type) {
	case *chast.BetweenClause:
		if value.Not {
			return predicateBound{}, false, ErrUnsafePredicate
		}
		field, ok := sourceColumn(value.Expr)
		if !ok {
			return predicateBound{}, false, nil
		}
		numericStyle := numericBoundStyle(field, bucket)
		lower, ok := evaluateBound(value.Between, true, numericStyle, constants, now)
		if !ok {
			return predicateBound{}, false, nil
		}
		upper, ok := evaluateBound(value.And, true, numericStyle, constants, now)
		if !ok {
			return predicateBound{}, false, nil
		}
		lower.target = &boundTarget{endpoint: endpointLower, style: lower.style, field: field, set: func(expr chast.Expr) {
			value.Between = expr
		}}
		upper.target = &boundTarget{endpoint: endpointUpper, style: upper.style, field: field, set: func(expr chast.Expr) {
			value.And = expr
		}}
		return predicateBound{field: field, lower: &lower, upper: &upper}, true, nil
	case *chast.BinaryOperation:
		operator, ok := comparisonOperator(value.Operation)
		if !ok {
			return predicateBound{}, false, nil
		}
		field, fieldOnLeft := sourceColumn(value.LeftExpr)
		boundExpression := value.RightExpr
		setBound := func(expr chast.Expr) { value.RightExpr = expr }
		if !fieldOnLeft {
			field, fieldOnLeft = sourceColumn(value.RightExpr)
			boundExpression = value.LeftExpr
			setBound = func(expr chast.Expr) { value.LeftExpr = expr }
			operator = invertOperator(operator)
		}
		if !fieldOnLeft {
			return predicateBound{}, false, nil
		}
		inclusive := operator == ">=" || operator == "<="
		bound, ok := evaluateBound(
			boundExpression, inclusive, numericBoundStyle(field, bucket), constants, now,
		)
		if !ok {
			return predicateBound{}, false, nil
		}
		predicate := predicateBound{field: field}
		if operator == ">" || operator == ">=" {
			bound.target = &boundTarget{
				endpoint: endpointLower, style: bound.style, field: field, set: setBound,
			}
			predicate.lower = &bound
		} else {
			bound.target = &boundTarget{
				endpoint: endpointUpper, style: bound.style, field: field, set: setBound,
			}
			if !inclusive {
				bound.target.offset = bucket.step
			}
			if bound.now {
				bound.target.original = chast.Format(boundExpression)
			}
			predicate.upper = &bound
		}
		return predicate, true, nil
	default:
		return predicateBound{}, false, nil
	}
}

func comparisonOperator(operator chast.TokenKind) (string, bool) {
	switch operator {
	case chast.TokenKindGT:
		return ">", true
	case chast.TokenKindGE:
		return ">=", true
	case chast.TokenKindLT:
		return "<", true
	case chast.TokenKindLE:
		return "<=", true
	default:
		return "", false
	}
}

func invertOperator(operator string) string {
	switch operator {
	case ">":
		return "<"
	case ">=":
		return "<="
	case "<":
		return ">"
	case "<=":
		return ">="
	default:
		return operator
	}
}

func numericBoundStyle(field string, bucket bucketSpec) boundStyle {
	if field != bucket.outputColumn || field == bucket.timeColumn {
		return boundUnixSeconds
	}
	switch bucket.outputUnit {
	case timeseries.DateTimeUnixMilli:
		return boundUnixMilli
	case timeseries.DateTimeUnixMicro:
		return boundUnixMicro
	case timeseries.DateTimeUnixNano:
		return boundUnixNano
	default:
		return boundUnixSeconds
	}
}

func timeFromInteger(value int64, style boundStyle) time.Time {
	return sqlanalyzer.UnixTime(value, inputTypeForBound(style))
}

func evaluateBound(
	expression chast.Expr,
	inclusive bool,
	numericStyle boundStyle,
	constants map[string]int64,
	now time.Time,
) (analyzedBound, bool) {
	expression = unwrapColumnExpr(expression)
	switch value := expression.(type) {
	case *chast.NumberLiteral:
		integer, err := strconv.ParseInt(value.Literal, value.Base, 64)
		return analyzedBound{
			value: timeFromInteger(integer, numericStyle), inclusive: inclusive, style: numericStyle,
		}, err == nil
	case *chast.StringLiteral:
		parsed, ok := sqlanalyzer.ParseSQLTime(value.Literal)
		return analyzedBound{value: parsed, inclusive: inclusive, style: boundSQLDateTime}, ok
	case *chast.FunctionExpr:
		if value.Name == nil {
			return analyzedBound{}, false
		}
		name := strings.ToLower(value.Name.Name)
		if name == "now" && len(functionArgs(value)) == 0 {
			return analyzedBound{value: now, inclusive: inclusive, style: boundUnixSeconds, now: true}, true
		}
		if name == "todatetime64" || name == "now64" {
			args := functionArgs(value)
			precision := int64(3)
			if name == "now64" {
				if len(args) > 1 {
					return analyzedBound{}, false
				}
				if len(args) == 1 {
					var ok bool
					precision, ok = evalInteger(args[0], constants)
					if !ok {
						return analyzedBound{}, false
					}
				}
				if precision < 0 || precision > 9 {
					return analyzedBound{}, false
				}
				return analyzedBound{
					value: truncatePrecision(now, precision), inclusive: inclusive,
					style: boundToDateTime64 + boundStyle(precision), now: true,
				}, true
			}
			if len(args) != 2 {
				return analyzedBound{}, false
			}
			var ok bool
			precision, ok = evalInteger(args[1], constants)
			if !ok || precision < 0 || precision > 9 {
				return analyzedBound{}, false
			}
			inner, ok := evaluateBound(args[0], inclusive, boundUnixSeconds, constants, now)
			if !ok {
				return analyzedBound{}, false
			}
			inner.value = truncatePrecision(inner.value, precision)
			inner.style = boundToDateTime64 + boundStyle(precision)
			inner.now = false
			return inner, true
		}
		if name == toDateTimeFunction || name == "todate" {
			args := functionArgs(value)
			if len(args) != 1 {
				return analyzedBound{}, false
			}
			inner, ok := evaluateBound(args[0], inclusive, boundUnixSeconds, constants, now)
			if !ok {
				return analyzedBound{}, false
			}
			if name == toDateTimeFunction {
				inner.style = boundToDateTime
			} else {
				inner.style = boundToDate
			}
			inner.now = false
			return inner, true
		}
	case *chast.Ident, *chast.NestedIdentifier:
		name, ok := sourceColumn(value)
		if !ok {
			return analyzedBound{}, false
		}
		integer, exists := constants[name]
		return analyzedBound{
			value: timeFromInteger(integer, numericStyle), inclusive: inclusive, style: numericStyle,
		}, exists
	case *chast.BinaryOperation:
		left, leftOK := evaluateBound(value.LeftExpr, inclusive, numericStyle, constants, now)
		right, rightOK := evalInteger(value.RightExpr, constants)
		if !leftOK || !rightOK {
			return analyzedBound{}, false
		}
		switch value.Operation {
		case chast.TokenKindPlus:
			left.value = left.value.Add(time.Duration(right) * time.Second)
		case chast.TokenKindMinus:
			left.value = left.value.Add(-time.Duration(right) * time.Second)
		default:
			return analyzedBound{}, false
		}
		left.now = false
		return left, true
	}
	return analyzedBound{}, false
}

func analyzeFormat(format *chast.FormatClause) (byte, error) {
	if format == nil || format.Format == nil {
		return supportedFormats["tabseparated"], nil
	}
	outputFormat, ok := supportedFormats[strings.ToLower(format.Format.Name)]
	if !ok {
		return 0, ErrUnsupportedOutputFormat
	}
	return outputFormat, nil
}

func buildQueryArtifacts(query *chast.SelectQuery, ranges rangeAnalysis, step time.Duration) (string, *clickHouseRenderer) {
	occupied := chast.Format(query)
	bounds := make([]rendererBound, 0, len(ranges.targets)+1)
	addBound := func(target endpoint, style boundStyle, offset time.Duration, original string) chast.Expr {
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
		return &chast.PlaceHolder{Type: token}
	}
	for _, target := range ranges.targets {
		target.set(addBound(target.endpoint, target.style, target.offset, target.original))
	}
	renderer := &clickHouseRenderer{explicitFormat: query.Format != nil, step: step}
	if ranges.rawUpper != nil && ranges.rawUpper.Inclusive && ranges.upper != nil {
		renderer.upperTick = step - ranges.upper.target.offset
	}
	format := query.Format
	if ranges.addSynthetic != nil {
		// a range running to now is rendered with no upper bound, as the statement was written
		query.Format = templateFormat()
		renderer.openTemplate = chast.Format(query)
		query.Format = format
		ranges.addSynthetic(&chast.BinaryOperation{
			LeftExpr:  identifierExpression(ranges.timeColumn),
			Operation: chast.TokenKindLT,
			RightExpr: addBound(endpointUpper, ranges.lowerStyle, step, ""),
		})
	}

	canonical := chast.Format(query)
	for _, bound := range bounds {
		canonical = strings.ReplaceAll(canonical, bound.token, placeholderFor(bound.endpoint))
	}
	query.Format = templateFormat()
	renderer.template, renderer.bounds = chast.Format(query), bounds
	return canonical, renderer
}

func templateFormat() *chast.FormatClause {
	return &chast.FormatClause{Format: &chast.Ident{Name: "TSVWithNamesAndTypes"}}
}

func placeholderFor(target endpoint) string {
	if target == endpointLower {
		return "<$TS1$>"
	}
	return "<$TS2$>"
}

func boundExpression(target endpoint, style boundStyle, extent timeseries.Extent) chast.Expr {
	value := extent.Start
	if target == endpointUpper {
		value = extent.End
	}
	if style >= boundToDateTime64 && style <= boundToDateTime64+9 {
		return functionExpression("toDateTime64", &chast.StringLiteral{Literal: value.UTC().Format("2006-01-02 15:04:05.999999999")},
			&chast.NumberLiteral{Literal: strconv.Itoa(int(style - boundToDateTime64)), Base: 10})
	}
	seconds := &chast.NumberLiteral{Literal: strconv.FormatInt(value.Unix(), 10), Base: 10}
	switch style {
	case boundUnixMilli:
		return &chast.NumberLiteral{Literal: strconv.FormatInt(value.UnixMilli(), 10), Base: 10}
	case boundUnixMicro:
		return &chast.NumberLiteral{Literal: strconv.FormatInt(value.UnixMicro(), 10), Base: 10}
	case boundUnixNano:
		return &chast.NumberLiteral{Literal: strconv.FormatInt(value.UnixNano(), 10), Base: 10}
	case boundSQLDateTime:
		return &chast.StringLiteral{Literal: value.UTC().Format("2006-01-02 15:04:05.999999999")}
	case boundToDateTime:
		return functionExpression("toDateTime", seconds)
	case boundToDate:
		return functionExpression("toDate", seconds)
	default:
		return seconds
	}
}

func functionExpression(name string, args ...chast.Expr) *chast.FunctionExpr {
	return &chast.FunctionExpr{
		Name: &chast.Ident{Name: name},
		Params: &chast.ParamExprList{
			Items: &chast.ColumnExprList{Items: args},
		},
	}
}

func identifierExpression(name string) chast.Expr {
	parts := strings.SplitN(name, ".", 2)
	if len(parts) == 2 {
		return &chast.NestedIdentifier{
			Ident: &chast.Ident{Name: parts[0]}, DotIdent: &chast.Ident{Name: parts[1]},
		}
	}
	return &chast.Ident{Name: name}
}

func inputTypeForBound(style boundStyle) timeseries.FieldDataType {
	switch style {
	case boundUnixMilli:
		return timeseries.DateTimeUnixMilli
	case boundUnixMicro:
		return timeseries.DateTimeUnixMicro
	case boundUnixNano:
		return timeseries.DateTimeUnixNano
	case boundSQLDateTime:
		return timeseries.DateTimeSQL
	default:
		return timeseries.DateTimeUnixSecs
	}
}

func looksLikeTimeColumn(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "time") || strings.Contains(name, "date")
}

func truncatePrecision(value time.Time, precision int64) time.Time {
	quantum := time.Nanosecond
	for i := precision; i < 9; i++ {
		quantum *= 10
	}
	return value.Truncate(quantum)
}
