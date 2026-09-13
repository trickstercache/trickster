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

package druid

import (
	"bytes"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/druid/model"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/parser"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"
)

const druidSQLPath = "/druid/v2/sql"

var (
	errDruidSQLUnsupported = errors.New("druid SQL query is not eligible for delta caching")

	// Druid SQL's TIME_FLOOR is deliberately the only bucket matcher enabled
	// here. The shared analyzer still owns all predicate, grouping, ordering,
	// canonicalization, and extent-rendering rules.
	druidSQLAnalyzer = cockroach.NewAnalyzer(cockroach.Options{
		BucketMatchers: []cockroach.BucketMatcher{druidTimeFloorMatcher},
		// Druid stores __time as a timestamp and accepts RFC3339 literals. This
		// also makes a numeric dashboard bound unambiguous at the origin.
		RenderNumericBoundsAsRFC3339: true,
		RoundUnalignedTimeBounds:     true,
	})
)

// isDruidSQLRequest recognizes the SQL endpoint after an optional upstream
// URL prefix has been prepended. SQL task endpoints intentionally do not match.
func isDruidSQLRequest(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	path := strings.TrimRight(r.URL.Path, "/")
	return strings.HasSuffix(path, druidSQLPath)
}

// parseSQLTimeRangeQuery adapts Druid's JSON SQL request envelope to the
// shared CockroachDB SQL analyzer. Object responses and array responses with a
// column-name header are admitted to the delta tier; other valid read queries
// remain safe OPC fallbacks.
func (c *Client) parseSQLTimeRangeQuery(r *http.Request) (
	*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error,
) {
	if r == nil || r.Method != http.MethodPost {
		return c.reject(nil, nil, false, modeProxy, reasonUnsupportedMethod,
			errInvalidRequest)
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get(headers.NameContentType))
	if err != nil || !strings.EqualFold(mediaType, headers.ValueApplicationJSON) {
		return c.reject(nil, nil, false, modeProxy, reasonUnsupportedContentType,
			errInvalidRequest)
	}
	body, err := request.GetBody(r)
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return c.reject(nil, nil, false, modeProxy, reasonInvalidJSON, errInvalidJSON)
	}
	document, err := decodeJSONObject(body)
	if err != nil {
		return c.reject(nil, nil, false, modeProxy, reasonInvalidJSON, errInvalidJSON)
	}

	originalBody := bytes.Clone(body)
	trq := &timeseries.TimeRangeQuery{
		OriginalBody: originalBody,
		CacheKeyElements: map[string]string{
			"query": string(originalBody),
		},
	}
	if r.URL != nil {
		trq.TemplateURL = urls.Clone(r.URL)
	}
	ro := &timeseries.RequestOptions{FastForwardDisable: true}
	resultFormat, formatOK := druidSQLResultFormat(document)
	// Build a canonical identity for every valid JSON envelope, including
	// object-cache fallbacks. This prevents transport-only SQL context values
	// such as queryId from fragmenting the fallback cache. An invalid context
	// or malformed resultFormat remains body-keyed and is therefore fail-closed.
	if sanitized, contextOK := canonicalDocument(document); contextOK {
		if _, exists := sanitized["resultFormat"]; !exists {
			sanitized["resultFormat"] = string(modeObject)
		} else if formatOK && resultFormat == string(modeObject) {
			sanitized["resultFormat"] = string(modeObject)
		}
		if canonicalFallback, _, _, marshalErr := marshalJSONObject(sanitized, nil); marshalErr == nil {
			trq.Statement = string(canonicalFallback)
			trq.CacheKeyElements["query"] = trq.Statement
			trq.ParsedQuery = model.NewSQLQueryPlan(nil, canonicalFallback)
		}
	}

	query, ok := document["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return c.reject(trq, ro, true, modeObject, reasonSQLInvalidQuery,
			errDruidSQLUnsupported)
	}
	responseFormat, header, shapeOK := druidSQLResponseShape(document, resultFormat, formatOK)
	if !shapeOK {
		return c.reject(trq, ro, true, modeObject, reasonSQLUnsupportedFormat,
			errDruidSQLUnsupported)
	}

	// The fallback identity was built above, before analysis. This keeps OPC
	// keys stable across JSON key ordering and transport-only context values.
	sanitized, contextOK := canonicalDocument(document)
	if !contextOK {
		return c.reject(trq, ro, true, modeObject, reasonInvalidContext,
			errDruidSQLUnsupported)
	}
	sanitized["resultFormat"] = resultFormat

	now := time.Now().UTC()
	// Grafana's official Druid datasource renders dashboard bounds as
	// MILLIS_TO_TIMESTAMP(<unix-millis>). Normalize only that exact literal
	// form into the timestamp literal understood by the shared analyzer. The
	// resulting renderer also sends valid Druid SQL for every missing extent.
	normalizedQuery := normalizeDruidSQLMillisBounds(query)
	analysis := druidSQLAnalyzer.Analyze(normalizedQuery, now)
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		if analysis.Mode == sqlanalyzer.CacheModeNone {
			return c.reject(nil, nil, false, modeProxy, reasonSQLUnsupported,
				errDruidSQLUnsupported)
		}
		return c.reject(trq, ro, true, modeObject, reasonSQLUnsupported,
			errDruidSQLUnsupported)
	}
	if !druidSQLPlanSupported(analysis.Plan) {
		return c.reject(trq, ro, true, modeObject, reasonUnsupportedShape,
			errDruidSQLUnsupported)
	}

	outputColumns, valueColumns, ok := druidSQLOutputColumns(normalizedQuery, analysis.Plan)
	if !ok {
		return c.reject(trq, ro, true, modeObject, reasonUnsupportedShape,
			errDruidSQLUnsupported)
	}
	// Query plans are immutable after analyzer return. Add provider-owned output
	// metadata to a shallow copy; all referenced analyzer fields remain read-only.
	planValue := *analysis.Plan
	planValue.ValueColumns = valueColumns
	plan := &planValue
	plan.ApplyToQuery(trq)
	sanitized["query"] = plan.CanonicalSQL
	canonicalBody, _, _, err := marshalJSONObject(sanitized, nil)
	if err != nil {
		return c.reject(nil, nil, false, modeProxy, reasonInvalidJSON, errInvalidJSON)
	}
	sqlPlan := model.NewSQLQueryPlanWithResponseShape(plan, canonicalBody,
		responseFormat, header, outputColumns...)
	trq.ParsedQuery = sqlPlan
	trq.Extent = plan.RequestExtent(now)
	trq.BackfillTolerance = druidBackfillTolerance(r)
	ro.BaseTimestampFieldName = plan.TimeColumn
	ro.ProviderRequest = sqlPlan

	// The canonical body is used as the request's body-aware cache identity;
	// SetExtent reconstructs the upstream body from OriginalBody so transient
	// context controls and formatting are preserved on the wire.
	trq.CacheKeyElements["query"] = string(canonicalBody)
	request.SetBody(r, canonicalBody)
	c.observeAnalysis(modeDelta, reasonSQLEligible)
	return trq, ro, true, nil
}

// normalizeDruidSQLMillisBounds adapts the literal dashboard-bound function
// emitted by Grafana to a standard SQL timestamp literal. It deliberately
// walks only WHERE: changing a selected function would change result values.
// Invalid, qualified, or computed calls are left untouched and therefore fail
// closed to the object cache in the shared analyzer.
func normalizeDruidSQLMillisBounds(statement string) string {
	parsed, err := parser.ParseOne(statement)
	if err != nil {
		return statement
	}
	selectStmt, ok := parsed.AST.(*tree.Select)
	if !ok {
		return statement
	}
	clause, ok := selectStmt.Select.(*tree.SelectClause)
	if !ok || clause.Where == nil {
		return statement
	}
	visitor := &druidMillisBoundVisitor{}
	expr, changed := tree.WalkExpr(visitor, clause.Where.Expr)
	if !changed {
		return statement
	}
	clause.Where.Expr = expr
	return tree.AsString(selectStmt)
}

type druidMillisBoundVisitor struct{}

func (*druidMillisBoundVisitor) VisitPre(expr tree.Expr) (bool, tree.Expr) {
	function, ok := expr.(*tree.FuncExpr)
	if !ok {
		return true, expr
	}
	name, ok := function.Func.FunctionReference.(*tree.UnresolvedName)
	if !ok || name.NumParts != 1 || name.Star ||
		!strings.EqualFold(name.Parts[0], "MILLIS_TO_TIMESTAMP") ||
		len(function.Exprs) != 1 || function.Filter != nil ||
		function.WindowDef != nil || len(function.OrderBy) != 0 {
		return true, expr
	}
	millis, ok := druidSQLIntegerLiteral(function.Exprs[0])
	if !ok {
		return false, expr
	}
	timestamp := time.UnixMilli(millis).UTC()
	if timestamp.Year() < 1 || timestamp.Year() > 9999 {
		return false, expr
	}
	replacement, err := parser.ParseExpr("TIMESTAMP '" +
		timestamp.Format("2006-01-02 15:04:05.000") + "'")
	if err != nil {
		return false, expr
	}
	return false, replacement
}

func (*druidMillisBoundVisitor) VisitPost(expr tree.Expr) tree.Expr { return expr }

func druidSQLIntegerLiteral(expr tree.Expr) (int64, bool) {
	switch value := expr.(type) {
	case *tree.NumVal:
		integer, err := value.AsInt64()
		return integer, err == nil
	case *tree.ParenExpr:
		return druidSQLIntegerLiteral(value.Expr)
	case *tree.UnaryExpr:
		integer, ok := druidSQLIntegerLiteral(value.Expr)
		if !ok {
			return 0, false
		}
		switch value.Operator.Symbol {
		case tree.UnaryPlus:
			return integer, true
		case tree.UnaryMinus:
			if integer == -1<<63 {
				return 0, false
			}
			return -integer, true
		}
	}
	return 0, false
}

// druidSQLOutputColumns records the exact SELECT-list order needed to rebuild
// an empty array response's header. Requiring deterministic output names also
// avoids relying on Druid's generated names for unaliased expressions.
func druidSQLOutputColumns(statement string, plan *sqlanalyzer.QueryPlan) (
	[]string, []string, bool,
) {
	if plan == nil {
		return nil, nil, false
	}
	parsed, err := parser.ParseOne(statement)
	if err != nil {
		return nil, nil, false
	}
	selectStmt, ok := parsed.AST.(*tree.Select)
	if !ok {
		return nil, nil, false
	}
	clause, ok := selectStmt.Select.(*tree.SelectClause)
	if !ok {
		return nil, nil, false
	}
	outputs := make([]string, 0, len(clause.Exprs))
	seen := make(map[string]struct{}, len(clause.Exprs))
	for _, item := range clause.Exprs {
		name := string(item.As)
		if name == "" {
			name, ok = cockroach.ColumnName(item.Expr)
		}
		name = strings.TrimSpace(name)
		key := strings.ToLower(name)
		if !ok || name == "" {
			return nil, nil, false
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, nil, false
		}
		seen[key] = struct{}{}
		outputs = append(outputs, name)
	}

	foundTime := false
	foundGroups := make(map[string]bool, len(plan.GroupColumns))
	values := make([]string, 0, len(outputs))
	for _, output := range outputs {
		if strings.EqualFold(output, plan.OutputColumn) {
			if foundTime {
				return nil, nil, false
			}
			foundTime = true
			continue
		}
		group := false
		for _, name := range plan.GroupColumns {
			if strings.EqualFold(output, name) {
				foundGroups[strings.ToLower(name)] = true
				group = true
				break
			}
		}
		if !group {
			values = append(values, output)
		}
	}
	if !foundTime || len(foundGroups) != len(plan.GroupColumns) || len(values) == 0 {
		return nil, nil, false
	}
	return outputs, values, true
}

func druidSQLResultFormat(document map[string]any) (string, bool) {
	value, exists := document["resultFormat"]
	if !exists {
		return string(modeObject), true
	}
	format, ok := value.(string)
	if !ok {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(format)), true
}

// druidSQLResponseShape validates the small set of tabular response shapes
// that the Druid SQL model can round-trip. Druid's array format is useful to
// Grafana's datasource plugin only when its first row contains column names;
// metadata rows (typesHeader/sqlTypesHeader) are deliberately rejected.
func druidSQLResponseShape(document map[string]any, resultFormat string,
	formatOK bool,
) (byte, bool, bool) {
	if !formatOK {
		return model.SQLResponseObject, false, false
	}
	header, headerOK := optionalSQLBool(document, "header")
	typesHeader, typesHeaderOK := optionalSQLBool(document, "typesHeader")
	sqlTypesHeader, sqlTypesHeaderOK := optionalSQLBool(document, "sqlTypesHeader")
	if !headerOK || !typesHeaderOK || !sqlTypesHeaderOK || typesHeader || sqlTypesHeader {
		return model.SQLResponseObject, false, false
	}
	var responseFormat byte
	switch resultFormat {
	case string(modeObject):
		if header {
			return model.SQLResponseObject, false, false
		}
		responseFormat = model.SQLResponseObject
	case "array":
		if !header {
			return model.SQLResponseObject, false, false
		}
		responseFormat = model.SQLResponseArray
	default:
		return model.SQLResponseObject, false, false
	}
	context, exists := document["context"]
	if !exists {
		return responseFormat, header, true
	}
	contextMap, ok := context.(map[string]any)
	if !ok {
		return model.SQLResponseObject, false, false
	}
	serializeLong, serializeLongOK := optionalSQLBool(contextMap, "serializeDateTimeAsLong")
	serializeLongInner, serializeLongInnerOK := optionalSQLBool(contextMap,
		"serializeDateTimeAsLongInner")
	if !serializeLongOK || !serializeLongInnerOK || serializeLong || serializeLongInner {
		return model.SQLResponseObject, false, false
	}
	if zone, exists := contextMap["sqlTimeZone"]; exists {
		zoneName, ok := zone.(string)
		if !ok || !isUTCZone(zoneName) {
			return model.SQLResponseObject, false, false
		}
	}
	return responseFormat, header, true
}

func optionalSQLBool(document map[string]any, key string) (bool, bool) {
	value, exists := document[key]
	if !exists {
		return false, true
	}
	flag, ok := value.(bool)
	return flag, ok
}

func druidSQLPlanSupported(plan *sqlanalyzer.QueryPlan) bool {
	if plan == nil || plan.Renderer == nil || plan.Step <= 0 ||
		!strings.EqualFold(plan.TimeColumn, "__time") {
		return false
	}
	// Without an alias Cockroach's canonical output name is the complete
	// TIME_FLOOR expression, while Druid's generated response name is version
	// dependent. Requiring a simple/explicit output name keeps the wire model
	// deterministic and fails closed for ambiguous SQL.
	name := strings.TrimSpace(plan.OutputColumn)
	return name != "" && !strings.ContainsAny(name, "()")
}

// druidTimeFloorMatcher recognizes TIME_FLOOR(__time, fixed-period[, origin,
// timezone]). Calendar periods and non-UTC zones are rejected because their
// bucket boundaries cannot be represented by a single fixed Step/Phase pair.
func druidTimeFloorMatcher(name string, args []tree.Expr) (cockroach.BucketMatch, bool) {
	if name != "time_floor" || len(args) < 2 || len(args) > 4 {
		return cockroach.BucketMatch{}, false
	}
	column, ok := cockroach.ColumnName(args[0])
	if !ok || !strings.EqualFold(column, "__time") {
		return cockroach.BucketMatch{}, false
	}
	period, ok := args[1].(*tree.StrVal)
	if !ok {
		return cockroach.BucketMatch{}, false
	}
	step, defaultOrigin, calendar, ok := fixedPeriod(strings.ToUpper(period.RawString()))
	if !ok || calendar || step <= 0 {
		return cockroach.BucketMatch{}, false
	}
	origin := defaultOrigin
	if len(args) >= 3 {
		var originOK bool
		origin, originOK = druidSQLTimestampLiteral(args[2])
		if !originOK || !unixNanoRepresentable(origin) {
			return cockroach.BucketMatch{}, false
		}
	}
	if len(args) == 4 {
		tz, ok := args[3].(*tree.StrVal)
		if !ok || !isUTCZone(tz.RawString()) {
			return cockroach.BucketMatch{}, false
		}
	}
	var phase time.Duration
	if !origin.IsZero() {
		phase = time.Duration(origin.UnixNano() % step.Nanoseconds())
		if phase < 0 {
			phase += step
		}
	}
	return cockroach.BucketMatch{TimeColumn: column, Step: step, Phase: phase}, true
}

func druidSQLTimestampLiteral(expr tree.Expr) (time.Time, bool) {
	switch value := expr.(type) {
	case *tree.StrVal:
		parsed, err := parseDruidTime(value.RawString())
		return parsed, err == nil
	case *tree.CastExpr:
		literal, ok := value.Expr.(*tree.StrVal)
		if !ok {
			return time.Time{}, false
		}
		parsed, err := parseDruidTime(literal.RawString())
		return parsed, err == nil
	default:
		return time.Time{}, false
	}
}
