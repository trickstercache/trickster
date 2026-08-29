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
	var hasTimeQueryParts bool
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

	// this field is used as part of the data that calculates the cache key
	trq.Statement = strings.Join(statements, " ; ")
	trq.ParsedQuery = q
	trq.CacheKeyElements = map[string]string{
		ParamQuery: trq.Statement,
	}
	return trq, canObjectCache, cacheError
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
	for _, s := range q.Statements {
		if sel, ok := s.(*influxql.SelectStatement); ok {
			// since setting timerange results in a clause of '>= start AND < end', we add the
			// size of 1 step onto the end time so as to ensure it is included in the results
			sel.SetTimeRange(extent.Start, extent.End.Add(trq.Step))
		}
	}
	var v url.Values
	switch r.Method {
	case http.MethodGet:
		// GET request, query param is in the url
		v, _, _ = params.GetRequestValues(r)
		v.Set(ParamQuery, q.String())
	case http.MethodPost:
		// POST request; query param is in the body, others are in the url
		rb := url.Values{ParamQuery: []string{q.String()}}.Encode()
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
