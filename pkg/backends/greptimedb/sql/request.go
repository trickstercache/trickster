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

// Package sql adapts GreptimeDB HTTP SQL requests to the shared SQL analyzer.
package sql

import (
	"errors"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlscan"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

var (
	errRequest = errors.New("unsupported GreptimeDB SQL request")
	errObject  = errors.New("GreptimeDB SQL request requires object caching")
)

type sqlRequest struct {
	query, form url.Values
	values      url.Values
	body        []byte
}

func extract(r *http.Request) (*sqlRequest, error) {
	if r == nil || r.URL == nil || (r.Method != http.MethodGet && r.Method != http.MethodPost) {
		return nil, errRequest
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	out := &sqlRequest{query: query, values: maps.Clone(query)}
	if r.Method == http.MethodPost {
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/x-www-form-urlencoded" {
			return nil, errRequest
		}
		out.body, err = request.GetBody(r)
		if err != nil {
			return nil, err
		}
		out.form, err = url.ParseQuery(string(out.body))
		if err != nil {
			return nil, err
		}
		for k, v := range out.form {
			// An explicitly empty URL value still overrides the form value.
			if _, exists := query[k]; !exists {
				out.values[k] = v
			}
		}
	}
	for _, values := range []url.Values{out.query, out.form} {
		for _, v := range values {
			if len(v) != 1 {
				return nil, errRequest
			}
		}
	}
	if out.values.Get("sql") == "" {
		return nil, errRequest
	}
	return out, nil
}

// ParseTimeRangeQuery uses the same analyzer as the provider's native SQL path.
func ParseTimeRangeQuery(r *http.Request, analyzer sqlanalyzer.DialectAnalyzer,
) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	input, err := extract(r)
	if err != nil || analyzer == nil {
		return nil, nil, false, errRequest
	}
	now := time.Now()
	statement := input.values.Get("sql")
	if !singleSelect(statement) {
		return nil, nil, false, errRequest
	}
	analysis := analyzer.Analyze(statement, now)
	if analysis.Mode == sqlanalyzer.CacheModeNone {
		return nil, nil, false, errRequest
	}
	trq := sqlanalyzer.NewTimeRangeQuery(statement)
	trq.OriginalBody = input.body
	trq.CacheKeyElements["greptime.http"] = input.values.Encode()
	ro := &timeseries.RequestOptions{FastForwardDisable: true, ResponseContentType: "application/json", FallbackToProxyOnError: true}
	format := strings.ToLower(input.values.Get("format"))
	_, limited := input.values["limit"]
	known := true
	for key := range input.values {
		switch key {
		case "sql", "db", "format", "epoch", "compression":
		default:
			known = false
		}
	}
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil || limited || !known ||
		(format != "" && format != "greptimedb_v1") {
		return trq, ro, true, errObject
	}
	plan := analysis.Plan
	if plan.DropsPartialBuckets {
		return trq, ro, true, errObject
	}
	plan.ApplyToQuery(trq)
	trq.Extent = plan.RequestExtent(now)
	input.values.Set("sql", plan.CanonicalSQL)
	trq.CacheKeyElements["greptime.http"] = input.values.Encode()
	trq.CacheKeyElements["sql"] = plan.CanonicalSQL
	trq.TemplateURL = urls.Clone(r.URL)
	trq.TemplateURL.RawQuery = input.values.Encode()
	ro.BaseTimestampFieldName = plan.TimeColumn
	if trq.BackfillTolerance == 0 {
		bf := time.Minute
		if res := request.GetResources(r); res != nil && res.BackendOptions != nil {
			bf = time.Duration(res.BackendOptions.BackfillTolerance)
		}
		if plan.UpperBound == nil && bf < trq.Step {
			bf = trq.Step
		}
		trq.BackfillTolerance = bf
	}
	return trq, ro, true, nil
}

func singleSelect(statement string) bool {
	scanner := sqlscan.New(statement, sqlscan.Options{})
	seen, ended := false, false
	for {
		token, more := scanner.Next()
		if !more {
			return seen && !scanner.Unterminated
		}
		if token.Kind == sqlscan.Punct && scanner.Text(token) == ";" {
			ended = true
			continue
		}
		if ended || (!seen && !scanner.IsWord(token, "select")) {
			return false
		}
		seen = true
	}
}

// SetExtent rewrites the effective SQL field, leaving all other parameters intact.
func SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery, extent *timeseries.Extent) error {
	if trq == nil || extent == nil {
		return errRequest
	}
	plan, ok := trq.ParsedQuery.(*sqlanalyzer.QueryPlan)
	if !ok || plan == nil {
		return errRequest
	}
	input, err := extract(r)
	if err != nil {
		return err
	}
	statement, err := plan.RenderExtent(*extent)
	if err != nil {
		return err
	}
	if _, inURL := input.query["sql"]; inURL {
		input.query.Set("sql", statement)
		r.URL.RawQuery = input.query.Encode()
	} else {
		input.form.Set("sql", statement)
		request.SetBody(r, []byte(input.form.Encode()))
	}
	return nil
}
