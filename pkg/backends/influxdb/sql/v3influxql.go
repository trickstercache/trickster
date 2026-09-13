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

package sql

import (
	"net/http"
	"time"

	ti "github.com/trickstercache/trickster/v2/pkg/backends/influxdb/influxql"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	pe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/influxdata/influxql"
)

// measurementField is the column InfluxDB 3 adds to v3 InfluxQL results
// naming each row's source measurement.
const measurementField = "iox::measurement"

// ParseV3InfluxQL parses a `/api/v3/query_influxql` request. The query arrives
// in the v3 request document (URL params, JSON body, or form body) and the
// response uses the v3 tabular formats, so this path shares the v3 SQL
// request/response plumbing while delegating statement analysis to the
// InfluxQL parser.
func ParseV3InfluxQL(r *http.Request, f iofmt.Format,
) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	if r == nil || !f.IsV3InfluxQL() {
		return nil, nil, false, iofmt.ErrSupportedQueryLanguage
	}
	v3r, err := extractV3Request(r)
	if err != nil {
		return nil, nil, false, err
	}
	if v3r.Query == "" {
		return nil, nil, false, pe.MissingURLParam(ParamQuery)
	}
	outputFormat, _ := iofmt.V3OutputFormatByName(v3r.Format)
	isBody := methods.HasBody(r.Method)

	// Parameterized queries bypass delta caching, same as the v3 SQL path.
	if len(v3r.Params) > 0 {
		trq := &timeseries.TimeRangeQuery{
			Statement:        v3r.Query,
			CacheKeyElements: map[string]string{ti.ParamQuery: v3r.Query},
		}
		applyRequestIdentity(trq, v3r)
		trq.OriginalBody = v3r.Body
		return trq, &timeseries.RequestOptions{OutputFormat: outputFormat},
			true, ErrParameterizedQuery
	}

	trq, canOPC, parseErr := ti.ParseStatement(v3r.Query, time.Now())
	if trq == nil {
		return nil, nil, false, parseErr
	}
	applyRequestIdentity(trq, v3r)
	if isBody {
		trq.OriginalBody = v3r.Body
	}
	trq.TemplateURL = urls.Clone(r.URL)
	if inner := trq.ParsedQuery; inner != nil {
		trq.ParsedQuery = &V3InfluxQLQuery{Inner: inner}
	}
	if parseErr != nil {
		// the object cache serves the response verbatim, so its identity must
		// be the original statement: the tokenized statement has its time
		// range zeroed and would alias different time windows
		trq.CacheKeyElements[ti.ParamQuery] = v3r.Query
		return trq, nil, canOPC, parseErr
	}

	// v3 responses are tabular with a naive-UTC "time" column regardless of
	// query language, so the v3 SQL marshal/unmarshal path handles them
	trq.TimestampDefinition = timeseries.FieldDefinition{
		Name:     DefaultTimestampField,
		DataType: timeseries.DateTimeRFC3339Nano,
		Role:     timeseries.RoleTimestamp,
	}
	// v3 InfluxQL rows carry the source measurement in an iox::measurement
	// column; treating it as a tag partitions multi-measurement results into
	// distinct series so same-epoch rows never collapse across measurements
	trq.TagFieldDefintions = append(timeseries.FieldDefinitions{
		{Name: measurementField, Role: timeseries.RoleTag},
	}, trq.TagFieldDefintions...)
	trq.ExtractBackfillTolerance(v3r.Query)
	if trq.BackfillTolerance == 0 {
		bf := time.Minute
		res := request.GetResources(r)
		if res != nil {
			bf = time.Duration(res.BackendOptions.BackfillTolerance)
		}
		// open-ended InfluxQL ranges run to now; flooring the tolerance at one
		// bucket keeps the still-filling final bucket out of the cache
		if bf < trq.Step {
			bf = trq.Step
		}
		trq.BackfillTolerance = bf
	}
	rlo := &timeseries.RequestOptions{
		OutputFormat:           outputFormat,
		BaseTimestampFieldName: DefaultTimestampField,
	}
	if !isBody {
		qi := r.URL.Query()
		qi.Set(ParamQuery, trq.Statement)
		trq.TemplateURL.RawQuery = qi.Encode()
	}
	return trq, rlo, canOPC, nil
}

// SetExtentV3InfluxQL rewrites a v3 InfluxQL upstream request to cover the
// provided extent. Unlike the v1 path, the rewritten statement is re-encoded
// into the v3 request document (JSON, form, or URL params) with the request's
// other fields (db, format, ...) preserved, and no v1-only parameters are
// injected.
func SetExtentV3InfluxQL(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent, q *influxql.Query,
) {
	for _, s := range q.Statements {
		if sel, ok := s.(*influxql.SelectStatement); ok {
			// SetTimeRange emits '>= start AND < end', so one step is added to
			// the end time to keep the final bucket in the results
			sel.SetTimeRange(extent.Start, extent.End.Add(trq.Step))
		}
	}
	statement := q.String()
	if methods.HasBody(r.Method) {
		request.SetBody(r, EncodeBody(r, statement))
		return
	}
	v := r.URL.Query()
	v.Set(ParamQuery, statement)
	r.URL.RawQuery = v.Encode()
}
