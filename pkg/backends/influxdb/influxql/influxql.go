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

package influxql

import (
	"errors"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	te "github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	pe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/influxdata/influxql"
)

var (
	// ErrUnsupportedTimeZone indicates a non-UTC tz() clause, which shifts GROUP BY time() buckets
	// by a zone offset that can change across daylight saving transitions
	ErrUnsupportedTimeZone = errors.New("non-UTC tz() clauses cannot be delta cached")
	// ErrCrossBucket indicates fill(previous|linear) or a transformation whose value in one bucket
	// depends on other buckets, so a sub-range fetch would compute a different value
	ErrCrossBucket = errors.New("values that depend on other time buckets cannot be delta cached")
	// ErrUnsupportedLimit indicates LIMIT, OFFSET, SLIMIT or SOFFSET, which apply to the whole
	// result rather than to each time bucket
	ErrUnsupportedLimit = errors.New("result limits cannot be delta cached")
	// ErrSubquery indicates a subquery, whose own time range is not rewritten for sub-range fetches
	ErrSubquery = errors.New("subqueries cannot be delta cached")
)

const statementSeparator = ";\n" // as influxql.Statements.String joins statements

var crossBucketCalls = map[string]struct{}{
	"derivative": {}, "non_negative_derivative": {}, "difference": {}, "non_negative_difference": {},
	"moving_average": {}, "cumulative_sum": {}, "elapsed": {}, "integral": {},
	"exponential_moving_average": {}, "double_exponential_moving_average": {},
	"triple_exponential_moving_average": {}, "triple_exponential_derivative": {},
	"relative_strength_index": {}, "kaufmans_efficiency_ratio": {},
	"kaufmans_adaptive_moving_average": {}, "chande_momentum_oscillator": {},
	"holt_winters": {}, "holt_winters_with_fit": {},
}

var epochToFlag = map[string]byte{
	"ns": 1,
	"u":  2, "µ": 2,
	"ms": 3,
	"s":  4,
	"m":  5,
	"h":  6,
}

// Common URL Parameter Names
const (
	ParamQuery   = "q"
	ParamDB      = "db"
	ParamEpoch   = "epoch"
	ParamPretty  = "pretty"
	ParamChunked = "chunked"
)

// ParseStatement parses one or more InfluxQL statements and returns a
// TimeRangeQuery carrying the time-range-tokenized statement, cadence, extent,
// GROUP BY tag fields, and parse tree. The second return reports whether the
// statement remains eligible for the object proxy cache; a non-nil error means
// the statement cannot be delta-cached (the TimeRangeQuery is still returned
// when the statement at least parsed).
func ParseStatement(statement string, now time.Time,
) (*timeseries.TimeRangeQuery, bool, error) {
	trq := &timeseries.TimeRangeQuery{Statement: statement}
	valuer := &influxql.NowValuer{Now: now}

	var cacheError error

	p := influxql.NewParser(strings.NewReader(trq.Statement))
	q, err := p.ParseQuery()
	if err != nil {
		return nil, false, err
	}

	trq.Step = -1
	var hasTimeQueryParts, hasPhase bool
	statements := make([]string, 0, len(q.Statements))
	var canObjectCache bool
	for _, v := range q.Statements {
		sel, ok := v.(*influxql.SelectStatement)
		if !ok {
			// non-SELECT statements (SHOW ..., etc.) cannot be delta-cached;
			// they ride along verbatim in the tokenized statement
			cacheError = pe.ErrNotTimeRangeQuery
			statements = append(statements, v.String())
			continue
		}
		if sel.Condition == nil {
			cacheError = pe.ErrNotTimeRangeQuery
		} else {
			canObjectCache = true
		}
		step, err := sel.GroupByInterval()
		if err != nil {
			cacheError = err
		} else {
			if trq.Step == -1 && step > 0 {
				trq.Step = step
			} else if trq.Step != step {
				// this condition means multiple queries were present, and had
				// different step widths
				cacheError = pe.ErrStepParse
			}
			if phase, err := groupByPhase(sel, step); err != nil {
				cacheError = err
			} else if !hasPhase {
				trq.Phase, hasPhase = phase, true
			} else if trq.Phase != phase {
				cacheError = pe.ErrStepParse
			}
		}
		if sel.Location != nil && sel.Location != time.UTC {
			cacheError = ErrUnsupportedTimeZone
		}
		if err := checkDeltaSafe(sel); err != nil {
			cacheError = err
		}
		_, tr, err := influxql.ConditionExpr(sel.Condition, valuer)
		if err != nil {
			cacheError = err
		}

		// this section determines the time range of the query
		ex := timeseries.Extent{Start: tr.Min, End: tr.Max}
		if ex.Start.IsZero() {
			ex.Start = time.Unix(0, 0)
		}
		if ex.End.IsZero() {
			ex.End = now
		}
		if trq.Extent.Start.IsZero() {
			trq.Extent = ex
			// timestamps are nanoseconds, so the parsed inclusive maximum ends a half-open range 1ns later
			trq.Requested = timeseries.RequestedRange{Start: ex.Start, End: ex.End, OpenEnded: tr.Max.IsZero()}
			if !trq.Requested.OpenEnded {
				trq.Requested.End = ex.End.Add(time.Nanosecond)
			}
		} else if trq.Extent != ex {
			// this condition means multiple queries were present, and had
			// different time ranges
			cacheError = pe.ErrNotTimeRangeQuery
		}

		if len(trq.TagFieldDefintions) == 0 {
			trq.TagFieldDefintions = dimensionTagFields(sel)
		}

		// this sets a zero time range for normalizing the query for cache key hashing
		sel.SetTimeRange(time.Time{}, time.Time{})
		statements = append(statements, sel.String())

		hasTimeQueryParts = true
	}

	if !hasTimeQueryParts {
		cacheError = pe.ErrNotTimeRangeQuery
	}

	if trq.Step > 0 {
		trq.SampleModel = timeseries.SampleModelBucket
	}
	trq.StepAlignments, trq.StepAlignment = StepAlignments, DefaultStepAlignment

	// this field is used as part of the data that calculates the cache key
	trq.Statement = strings.Join(statements, " ; ")
	trq.ParsedQuery = q
	trq.CacheKeyElements = map[string]string{
		ParamQuery: trq.Statement,
	}
	return trq, canObjectCache, cacheError
}

// StepAlignments are the step alignment modes an InfluxQL query supports
const StepAlignments = timeseries.StepAlignmentAll

// DefaultStepAlignment is the mode an InfluxQL query uses when none is configured
const DefaultStepAlignment = timeseries.StepAlignmentTruncate

// RenderTimeRange returns q's statements with every SELECT limited to [start, end). It renders
// clones and never modifies q, which the concurrent fetches of one request share.
func RenderTimeRange(q *influxql.Query, start, end time.Time) string {
	var b strings.Builder
	for i, s := range q.Statements {
		if i > 0 {
			b.WriteString(statementSeparator)
		}
		if sel, ok := s.(*influxql.SelectStatement); ok {
			sel = sel.Clone()
			sel.SetTimeRange(start, end)
			s = sel
		}
		b.WriteString(s.String())
	}
	return b.String()
}

func checkDeltaSafe(sel *influxql.SelectStatement) error {
	if sel.Fill == influxql.PreviousFill || sel.Fill == influxql.LinearFill {
		return ErrCrossBucket
	}
	if sel.Limit > 0 || sel.Offset > 0 || sel.SLimit > 0 || sel.SOffset > 0 {
		return ErrUnsupportedLimit
	}
	for _, source := range sel.Sources {
		if _, ok := source.(*influxql.SubQuery); ok {
			return ErrSubquery
		}
	}
	var crossBucket bool
	for _, field := range sel.Fields {
		influxql.WalkFunc(field.Expr, func(node influxql.Node) {
			if call, ok := node.(*influxql.Call); ok && !crossBucket {
				_, crossBucket = crossBucketCalls[strings.ToLower(call.Name)]
			}
		})
		if crossBucket {
			return ErrCrossBucket
		}
	}
	return nil
}

func groupByPhase(sel *influxql.SelectStatement, step time.Duration) (time.Duration, error) {
	if step <= 0 {
		return 0, nil
	}
	offset, err := sel.GroupByOffset()
	if err != nil {
		return 0, err
	}
	// InfluxDB starts each bucket at the Unix-epoch grid shifted by the offset
	phase := offset % step
	if phase < 0 {
		phase += step
	}
	return phase, nil
}

// dimensionTagFields extracts the plain tag names from a statement's GROUP BY
// dimensions (time buckets and wildcards are skipped).
func dimensionTagFields(sel *influxql.SelectStatement) timeseries.FieldDefinitions {
	var fields timeseries.FieldDefinitions
	for _, dimension := range sel.Dimensions {
		if ref, ok := dimension.Expr.(*influxql.VarRef); ok {
			fields = append(fields, timeseries.FieldDefinition{
				Name: ref.Val, Role: timeseries.RoleTag,
			})
		}
	}
	return fields
}

func ParseTimeRangeQuery(r *http.Request,
	f iofmt.Format) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions,
	bool, error,
) {
	if r == nil || !f.IsInfluxQL() {
		return nil, nil, false, iofmt.ErrSupportedQueryLanguage
	}

	uv := r.URL.Query()
	bv := make(url.Values)
	if r.Method == http.MethodPost {
		bv, _, _ = params.GetRequestValues(r)
	} else if r.Method != http.MethodGet {
		logger.Error("unuspported method in influxql.ParseTimeRangeQuery",
			logging.Pairs{keys.Method: r.Method})
		return nil, nil, false, te.ErrInvalidMethod
	}
	cv := maps.Clone(uv)
	maps.Copy(cv, bv)

	rlo := &timeseries.RequestOptions{OutputFormat: 0}

	statement := cv.Get(ParamQuery)
	if statement == "" {
		return nil, nil, false, pe.MissingURLParam(ParamQuery)
	}

	if x, ok := epochToFlag[cv.Get(ParamEpoch)]; ok {
		rlo.TimeFormat = x
	}

	if cv.Get(ParamPretty) == "true" {
		rlo.OutputFormat = 1
	}

	trq, canObjectCache, cacheError := ParseStatement(statement, time.Now())
	if trq == nil {
		return nil, nil, false, cacheError
	}
	trq.TemplateURL = urls.Clone(r.URL)

	if f.IsPost() {
		b, err := request.GetBody(r)
		if err != nil {
			return nil, nil, false, err
		}
		trq.OriginalBody = b
	} else {
		qv := url.Values(http.Header(uv).Clone())
		qv.Set(ParamQuery, trq.Statement)
		// Swap in the Tokenized Query in the Url Params
		trq.TemplateURL.RawQuery = qv.Encode()
	}
	if cacheError != nil {
		return nil, nil, true, cacheError
	}
	return trq, rlo, canObjectCache, nil
}

func SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent, q *influxql.Query,
) {
	// the time range clause is '>= start AND < end', so one step is added to keep the last bucket
	statement := RenderTimeRange(q, extent.Start, extent.End.Add(trq.Step))
	var v url.Values
	switch r.Method {
	case http.MethodGet:
		// GET request, query param is in the url
		v, _, _ = params.GetRequestValues(r)
		v.Set(ParamQuery, statement)
	case http.MethodPost:
		// POST request; query param is in the body, others are in the url
		rb := url.Values{ParamQuery: []string{statement}}.Encode()
		request.SetBody(r, []byte(rb))
		v = r.URL.Query()
	default:
		logger.Error("unuspported method in influxql.SetExtent",
			logging.Pairs{keys.Method: r.Method})
		return
	}

	v.Set(ParamEpoch, "ns") // request nanosecond epoch timestamp format from server
	v.Del(ParamChunked)     // we do not support chunked output or handling chunked server responses
	v.Del(ParamPretty)
	r.URL.RawQuery = v.Encode()
}
