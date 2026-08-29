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
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"
	pe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
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
	ParamParams = "params"
)

// ErrParameterizedQuery indicates a request bearing bound parameters, which
// bypasses delta caching: Trickster cannot bind values into the canonical
// statement, so the request is served via the object cache with the parameter
// values folded into the cache identity.
var ErrParameterizedQuery = errors.New("parameterized v3 SQL queries bypass delta caching")

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

// v3Request holds the fields of a v3 query request that Trickster recognizes,
// merged from URL parameters and the request body (body values win for POST).
type v3Request struct {
	Query  string
	DB     string
	Format string
	Params map[string]json.RawMessage
	// Body is the original request body, retained for pass-through paths.
	Body []byte
}

// extractV3Request decodes a v3 request's query document. Supports GET URL
// params, POST application/json, POST application/x-www-form-urlencoded, and
// falls back to treating a raw POST body as SQL. When neither the URL nor the
// body names a format, the Accept header is consulted, mirroring the origin's
// content negotiation.
func extractV3Request(r *http.Request) (*v3Request, error) {
	out := &v3Request{}
	query := r.URL.Query()
	out.Query = query.Get(ParamQuery)
	out.DB = query.Get(ParamDB)
	out.Format = query.Get(ParamFormat)
	defer func() {
		if out.Format == "" {
			out.Format = formatFromAccept(r.Header.Get(headers.NameAccept))
		}
	}()
	if !methods.HasBody(r.Method) {
		return out, nil
	}
	b, err := request.GetBody(r)
	if err != nil {
		return nil, err
	}
	out.Body = b
	if len(b) == 0 {
		return out, nil
	}
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		var payload struct {
			Q      string                     `json:"q"`
			DB     string                     `json:"db"`
			Format string                     `json:"format"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(b, &payload); err != nil {
			return nil, err
		}
		out.Query = payload.Q
		if payload.DB != "" {
			out.DB = payload.DB
		}
		if payload.Format != "" && out.Format == "" {
			out.Format = payload.Format
		}
		out.Params = payload.Params
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		vals, err := url.ParseQuery(string(b))
		if err != nil {
			return nil, err
		}
		out.Query = vals.Get(ParamQuery)
		if v := vals.Get(ParamDB); v != "" {
			out.DB = v
		}
		if v := vals.Get(ParamFormat); v != "" && out.Format == "" {
			out.Format = v
		}
		if v := vals.Get(ParamParams); v != "" {
			// best-effort: an undecodable params value still marks the
			// request as parameterized so it is never delta-cached
			if json.Unmarshal([]byte(v), &out.Params) != nil {
				out.Params = map[string]json.RawMessage{"_raw": json.RawMessage(strconv.Quote(v))}
			}
		}
	default:
		out.Query = string(b)
	}
	return out, nil
}

// formatFromAccept maps the first recognized media type in an Accept header
// to its v3 format-parameter equivalent. Unrecognized or wildcard types yield
// an empty string, leaving the origin's default (json) in effect.
func formatFromAccept(accept string) string {
	for part := range strings.SplitSeq(accept, ",") {
		mediaType := strings.TrimSpace(part)
		if i := strings.IndexByte(mediaType, ';'); i >= 0 {
			mediaType = strings.TrimSpace(mediaType[:i])
		}
		switch strings.ToLower(mediaType) {
		case "application/json":
			return "json"
		case "application/jsonl", "application/x-ndjson":
			return "jsonl"
		case "text/csv":
			return "csv"
		case "application/vnd.apache.parquet":
			return "parquet"
		case "text/plain":
			return "pretty"
		}
	}
	return ""
}

// ExtractQuery returns the SQL query text from a v3 request.
func ExtractQuery(r *http.Request) (string, error) {
	v3r, err := extractV3Request(r)
	if err != nil {
		return "", err
	}
	return v3r.Query, nil
}

// SupportedV3Format reports whether the request asks for a response format
// Trickster can reserialize (json, jsonl, csv). Requests for other formats
// (parquet, pretty, ...) must be proxied through untouched.
func SupportedV3Format(r *http.Request) bool {
	v3r, err := extractV3Request(r)
	if err != nil {
		// let the parse path surface the error
		return true
	}
	_, ok := iofmt.V3OutputFormatByName(v3r.Format)
	return ok
}

// EncodeBody rewrites the request body's SQL statement while preserving the
// body's other fields (db, format, ...) and its Content-Type shape. Used when
// Trickster rewrites the upstream request (e.g. on SetExtent).
func EncodeBody(r *http.Request, sqlQuery string) []byte {
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		document := map[string]json.RawMessage{}
		if b, err := request.GetBody(r); err == nil && len(b) > 0 {
			// best-effort: an unparsable body still yields a valid document
			_ = json.Unmarshal(b, &document)
		}
		statement, err := json.Marshal(sqlQuery)
		if err != nil {
			return []byte(sqlQuery)
		}
		document[ParamQuery] = statement
		b, err := json.Marshal(document)
		if err != nil {
			return []byte(sqlQuery)
		}
		return b
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		vals := url.Values{}
		if b, err := request.GetBody(r); err == nil && len(b) > 0 {
			if parsed, err := url.ParseQuery(string(b)); err == nil {
				vals = parsed
			}
		}
		vals.Set(ParamQuery, sqlQuery)
		return []byte(vals.Encode())
	}
	return []byte(sqlQuery)
}

// applyRequestIdentity folds result-affecting request fields that live outside
// the SQL statement into the cache identity, so requests differing only by
// database or bound parameters never share a cache entry.
func applyRequestIdentity(trq *timeseries.TimeRangeQuery, v3r *v3Request) {
	if trq.CacheKeyElements == nil {
		trq.CacheKeyElements = make(map[string]string, 2)
	}
	if v3r.DB != "" {
		trq.CacheKeyElements[ParamDB] = v3r.DB
	}
	if len(v3r.Params) > 0 {
		trq.CacheKeyElements[ParamParams] = canonicalParams(v3r.Params)
	}
}

// canonicalParams serializes bound parameters deterministically for cache keys.
func canonicalParams(params map[string]json.RawMessage) string {
	keys := slices.Sorted(maps.Keys(params))
	var sb strings.Builder
	for _, key := range keys {
		sb.WriteString(key)
		sb.WriteByte('=')
		sb.Write(params[key])
		sb.WriteByte(';')
	}
	return sb.String()
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
	v3r, err := extractV3Request(r)
	if err != nil {
		return nil, nil, false, err
	}
	if !isBody {
		qi = r.URL.Query()
	}
	if v3r.Query == "" {
		return nil, nil, false, pe.MissingURLParam(ParamQuery)
	}
	outputFormat, _ := iofmt.V3OutputFormatByName(v3r.Format)

	// Parameterized queries bypass delta caching: Trickster cannot bind
	// values into the canonical statement. The request passes through with
	// its body untouched and is object-cached with the parameter values in
	// its cache identity.
	if len(v3r.Params) > 0 {
		trq := sqlanalyzer.NewTimeRangeQuery(v3r.Query)
		applyRequestIdentity(trq, v3r)
		trq.OriginalBody = v3r.Body
		return trq, &timeseries.RequestOptions{OutputFormat: outputFormat},
			true, ErrParameterizedQuery
	}

	trq, ro, canOPC, err := parse(v3r.Query)
	if trq != nil {
		applyRequestIdentity(trq, v3r)
		if isBody {
			trq.OriginalBody = v3r.Body
		}
	}
	if err != nil {
		return trq, ro, canOPC, err
	}
	if ro == nil {
		ro = &timeseries.RequestOptions{}
	}
	ro.OutputFormat = outputFormat
	// a backfill-tolerance directive embedded in the statement wins; otherwise
	// apply the backend default and floor it at one bucket for open-ended
	// queries, whose request extent runs to now: without the floor the final,
	// still-filling bucket would be cached as complete.
	if trq.BackfillTolerance == 0 {
		bf := time.Minute
		res := request.GetResources(r)
		if res != nil {
			bf = time.Duration(res.BackendOptions.BackfillTolerance)
		}
		if q, ok := trq.ParsedQuery.(*Query); ok && q.Plan != nil &&
			q.Plan.UpperBound == nil && bf < trq.Step {
			bf = trq.Step
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
