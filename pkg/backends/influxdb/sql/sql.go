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

// Package sql implements InfluxDB 3 native SQL (Apache DataFusion) support for
// the `/api/v3/query_sql` endpoint. Statement analysis is delegated to the
// shared CockroachDB-parser adapter in pkg/parsing/sqlanalyzer/cockroach,
// configured with DataFusion's time-bucketing functions.
package sql

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"
	pe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Query marks a parsed v3 SQL statement and carries its dialect-independent
// cache plan, including the extent renderer used on cache misses.
type Query struct {
	Plan *sqlanalyzer.QueryPlan
}

// V3InfluxQLQuery marks a parsed InfluxQL query as originating from the v3
// native endpoint (`/api/v3/query_influxql`). Serialization and SetExtent use
// this to route through the v3 JSON format while reusing the v1 InfluxQL parser.
type V3InfluxQLQuery struct {
	Inner any // *influxql.Query from the v1 parser
}

// Common URL Parameter Names
const (
	ParamQuery  = "q"
	ParamDB     = "db"
	ParamFormat = "format"
)

// DefaultTimestampField is the default timestamp field name for v3 queries
const DefaultTimestampField = "time"

// dialectAnalyzer analyzes DataFusion SQL via the shared CockroachDB-parser
// adapter. It is stateless and safe for concurrent use.
var dialectAnalyzer sqlanalyzer.DialectAnalyzer = cockroach.NewAnalyzer(cockroach.Options{
	BucketMatchers: cockroach.DataFusionBucketMatchers(),
	// DataFusion rejects Timestamp-to-Int64 comparisons, so epoch-integer
	// bounds must be rendered back to the origin as RFC3339 literals.
	RenderNumericBoundsAsRFC3339: true,
	// v3 dashboard clients emit live, unaligned time ranges; round them
	// inward to complete buckets rather than failing closed.
	RoundUnalignedTimeBounds: true,
})

// ExtractQuery returns the SQL query text from a v3 request, decoding the
// POST body based on Content-Type. Supports GET (?q=), POST application/json
// ({"q":"..."}), POST application/x-www-form-urlencoded (q=...), and falls
// back to treating the raw POST body as SQL.
func ExtractQuery(r *http.Request) (string, error) {
	if !methods.HasBody(r.Method) {
		return r.URL.Query().Get(ParamQuery), nil
	}
	b, err := request.GetBody(r)
	if err != nil || len(b) == 0 {
		return "", err
	}
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		var payload struct {
			Q string `json:"q"`
		}
		if err := json.Unmarshal(b, &payload); err != nil {
			return "", err
		}
		return payload.Q, nil
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		vals, err := url.ParseQuery(string(b))
		if err != nil {
			return "", err
		}
		return vals.Get(ParamQuery), nil
	}
	return string(b), nil
}

// EncodeBody wraps a SQL statement in the body format matching the request's
// Content-Type. Used to preserve the inbound body shape when Trickster
// rewrites the upstream request (e.g. on SetExtent).
func EncodeBody(r *http.Request, sqlQuery string) []byte {
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		b, _ := json.Marshal(map[string]string{ParamQuery: sqlQuery})
		return b
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		return []byte(url.Values{ParamQuery: {sqlQuery}}.Encode())
	}
	return []byte(sqlQuery)
}

// parse adapts the dialect-independent analysis to Trickster's backend
// interface. A SELECT that cannot be delta-cached remains eligible for the
// object proxy cache.
func parse(statement string) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	now := time.Now()
	analysis := dialectAnalyzer.Analyze(statement, now)
	canObjectCache := analysis.Mode >= sqlanalyzer.CacheModeObject
	trq := sqlanalyzer.NewTimeRangeQuery(statement)
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		if !canObjectCache {
			return nil, nil, false, analysis.Err
		}
		return trq, nil, true, analysis.Err
	}

	plan := analysis.Plan
	plan.ApplyToQuery(trq)
	// This backend routes SetExtent by its own marker type rather than the
	// bare plan, so the plan is re-wrapped after ApplyToQuery installs it.
	trq.ParsedQuery = &Query{Plan: plan}
	trq.Extent = plan.RequestExtent(now)
	trq.ExtractBackfillTolerance(statement)

	options := &timeseries.RequestOptions{
		BaseTimestampFieldName: plan.TimeColumn,
	}
	options.ExtractFastForwardDisabled(statement)
	return trq, options, true, nil
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func ParseTimeRangeQuery(r *http.Request, f iofmt.Format,
) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	if r == nil || !f.IsV3SQL() {
		return nil, nil, false, iofmt.ErrSupportedQueryLanguage
	}
	var qi url.Values
	isBody := methods.HasBody(r.Method)
	sqlQuery, err := ExtractQuery(r)
	if err != nil {
		return nil, nil, false, err
	}
	if !isBody {
		qi = r.URL.Query()
	}
	if sqlQuery == "" {
		return nil, nil, false, pe.MissingURLParam(ParamQuery)
	}

	trq, ro, canOPC, err := parse(sqlQuery)
	if err != nil {
		return trq, ro, canOPC, err
	}
	if ro == nil {
		ro = &timeseries.RequestOptions{}
	}
	ro.OutputFormat = iofmt.V3OutputFormat(r)

	if isBody && trq != nil {
		trq.OriginalBody = []byte(sqlQuery)
	}
	if trq.BackfillTolerance == 0 {
		bf := time.Minute
		res := request.GetResources(r)
		if res != nil {
			bf = time.Duration(res.BackendOptions.BackfillTolerance)
		}
		trq.BackfillTolerance = bf
	}
	trq.TemplateURL = urls.Clone(r.URL)
	if isBody {
		request.SetBody(r, EncodeBody(r, trq.Statement))
	} else {
		qi.Set(ParamQuery, trq.Statement)
		trq.TemplateURL.RawQuery = qi.Encode()
	}
	return trq, ro, canOPC, nil
}
