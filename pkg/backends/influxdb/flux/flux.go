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

package flux

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	te "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/util/timeconv"
)

const (
	FuncRange           = "|> range("
	FuncAggregateWindow = "|> aggregateWindow("
	FuncWindow          = "|> window("

	TokenEvery     = "every:"
	TokenStart     = "start:"
	TokenStop      = "stop:"
	TokenCommaStop = "," + TokenStop

	TokenPlaceholderTimeRange = "<TIMERANGE_TOKEN>"

	ParamOrg  = "org"
	AttrQuery = "query"
	AttrNow   = "now"
	AttrLang  = "type"
	LangFlux  = "flux"

	AnnotationDatatype = "datatype"
	AnnotationGroup    = "group"
	AnnotationDefault  = "default"
)

var ErrTimeRangeParsingFailed = errors.New("failed to parse time range")

type Query struct {
	original  string
	tokenized string
	step      time.Duration
	extent    timeseries.Extent
	dialect   JSONRequestBodyDialect
}

type JSONRequestBody struct {
	Query   string                 `json:"query"`
	Type    string                 `json:"type"`
	Dialect JSONRequestBodyDialect `json:"dialect,omitempty"`
	Params  map[string]any         `json:"params,omitempty"`
	Now     any                    `json:"now,omitempty"`
}

type JSONRequestBodyDialect struct {
	Annotations    []string `json:"annotations,omitempty"`
	Delimiter      string   `json:"delimiter,omitempty"`
	Header         *bool    `json:"header,omitempty"`
	CommentPrefix  string   `json:"commentPrefix,omitempty"`
	DateTimeFormat string   `json:"dateTimeFormat,omitempty"`
}

type FluxJSONResponse struct {
	Results []FluxResult `json:"results"`
}

type FluxResult struct {
	Tables []FluxTable `json:"tables"`
}

type FluxTable struct {
	Columns []FluxColumn `json:"columns"`
	Records []FluxRecord `json:"records"`
}

type FluxColumn struct {
	Name     string `json:"name"`
	Datatype string `json:"datatype"`
}

type FluxRecord struct {
	Values map[string]interface{} `json:"values"`
}

func DefaultJSONRequestBody() *JSONRequestBody {
	return &JSONRequestBody{
		Type: LangFlux,
		Dialect: JSONRequestBodyDialect{
			Annotations:    DefaultAnnotations(),
			DateTimeFormat: RFC3339,
			Delimiter:      ",",
		},
	}
}

func DefaultAnnotations() []string {
	return []string{AnnotationDatatype, AnnotationGroup, AnnotationDefault}
}

func ParseTimeRangeQuery(r *http.Request,
	f iofmt.Format) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions,
	bool, error) {

	if !f.IsFlux() {
		return nil, nil, false, iofmt.ErrSupportedQueryLanguage
	}

	trq := &timeseries.TimeRangeQuery{}
	rlo := &timeseries.RequestOptions{OutputFormat: byte(f)}

	frb := DefaultJSONRequestBody()
	b, err := request.GetBody(r)
	if err != nil {
		return nil, nil, false, err
	}
	// user is posting a JSON request
	if f.IsFluxInputJSON() {
		err := json.Unmarshal(b, frb)
		if err != nil {
			return nil, nil, false, err
		}
	} else { // user is posting a Raw Query body
		frb.Query = string(b)
	}
	if frb.Type != LangFlux {
		return nil, nil, false, iofmt.ErrSupportedQueryLanguage
	}
	if frb.Query == "" {
		return nil, nil, false, te.MissingRequestParam(AttrQuery)
	}
	trq.Statement = frb.Query
	tokenizedStmt, extent, step, err := ParseQuery(frb.Query)
	if err != nil {
		return nil, nil, false, err
	}
	q := &Query{
		original:  frb.Query,
		tokenized: tokenizedStmt,
		step:      step,
		extent:    extent,
	}
	trq.CacheKeyElements = map[string]string{AttrQuery: tokenizedStmt}
	qp := r.URL.Query()
	if qp != nil {
		if v := qp.Get("org"); v != "" {
			trq.CacheKeyElements[ParamOrg] = v
		}
	}
	if frb.Now != nil {
		trq.CacheKeyElements[AttrNow] = fmt.Sprintf("%v", frb.Now)
	}
	if frb.Params != nil {
		for k, v := range frb.Params {
			trq.CacheKeyElements["fluxParam-"+k] = fmt.Sprintf("%v", v)
		}
	}
	trq.ParsedQuery = q
	trq.Step = step
	trq.Statement = tokenizedStmt
	trq.TemplateURL = urls.Clone(r.URL)
	trq.Extent = extent
	rlo.ProviderRequest = frb
	return trq, rlo, false, nil
}

const setExtentErrorLogEvent = "read request body failed in flux.SetExtent"

// vndfluxToJSON takes a request body of application/vnd.flux and converts it to
// the corresponding JSON request body map
func vndfluxToJSON(b []byte) *JSONRequestBody {
	return &JSONRequestBody{
		Query: string(b),
		Type:  LangFlux,
	}
}

func SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	e *timeseries.Extent, q *Query) {

	start := e.Start.Unix()
	end := e.End.Unix()
	// This fixes the "cannot query an empty range" condition
	// see: https://github.com/influxdata/flux/issues/3543
	if start >= end {
		start = end - int64(trq.Step.Seconds())
	}

	// this creates a new flux query string with TokenPlaceholderTimeRange
	// replaced with the Start / End times from Extent e
	s := strings.ReplaceAll(q.tokenized, TokenPlaceholderTimeRange,
		fmt.Sprintf("%s %d, %s %d",
			TokenStart, start, TokenStop, end))
	// this reads the JSON body, unmarshals it to a map[string]any, swaps in the
	// transformed query, marshals it back to a []byte and sets r.Body to it.
	b, err := request.GetBody(r)
	if err != nil || len(b) == 0 {
		logger.Error(setExtentErrorLogEvent, logging.Pairs{"error": err})
		return
	}
	var rb *JSONRequestBody
	if headers.ProvidesContentType(r, headers.ValueApplicationFlux) {
		rb = vndfluxToJSON(b)
		r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	} else if headers.ProvidesContentType(r, headers.ValueApplicationJSON) {
		err = json.Unmarshal(b, &rb)
		if err != nil || rb == nil {
			logger.Error(setExtentErrorLogEvent, logging.Pairs{"error": err})
			return
		}
	} else {
		rb = &JSONRequestBody{}
		r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	}
	rb.Query = s
	rb.Dialect = JSONRequestBodyDialect{
		Annotations: DefaultAnnotations(),
	}
	b, _ = json.Marshal(rb)
	request.SetBody(r, b)
}

func ParseQuery(input string) (string, timeseries.Extent, time.Duration, error) {
	var e timeseries.Extent
	var d time.Duration
	var err error
	// // this puts all pipe operations on their own line
	input = strings.ReplaceAll(input, "|>", "\n|>")
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		ri := strings.Index(line, FuncRange)
		switch {
		case ri >= 0:
			e, err = parseRange(line)
			if err != nil {
				return "", e, d, err
			}
			lines[i] = tokenizeRangeLine(line, ri)
		case strings.Contains(line, FuncAggregateWindow),
			strings.Contains(line, FuncWindow):
			d, err = parseStep(line)
			if err != nil {
				return "", e, d, err
			}
		}
	}
	return strings.Join(lines, "\n"), e, d, err
}

func parseStep(input string) (time.Duration, error) {
	i := strings.Index(input, TokenEvery)
	if i < 0 {
		return 0, ErrTimeRangeParsingFailed
	}
	i += 6
	input = input[i:]
	i = strings.Index(input, ",")
	j := strings.Index(input, ")")
	if i >= 0 && i < j {
		input = strings.TrimSpace(input[:i])
	} else if j >= 0 {
		input = strings.TrimSpace(input[:j])
	} else {
		return 0, ErrTimeRangeParsingFailed
	}
	return time.ParseDuration(input)
}

func parseRange(input string) (timeseries.Extent, error) {
	var e timeseries.Extent
	input = strings.ReplaceAll(input, " ", "")
	i := strings.Index(input, TokenStart)
	if i < 0 {
		return e, ErrTimeRangeParsingFailed
	}
	i += 6
	input = input[i:]
	i = strings.Index(input, TokenCommaStop)
	if i < 0 {
		return e, ErrTimeRangeParsingFailed
	}
	input = strings.TrimSuffix(strings.ReplaceAll(input, TokenCommaStop, ","), ")")
	parts := strings.Split(input, ",")
	if len(parts) != 2 {
		return e, ErrTimeRangeParsingFailed
	}
	var err error
	e.Start, err = tryParseTimeField(parts[0])
	if err != nil {
		return e, err
	}
	e.End, err = tryParseTimeField(parts[1])
	if err != nil {
		return e, err
	}
	return e, nil
}

func tryParseTimeField(s string) (time.Time, error) {
	var t time.Time
	var erd, eat, eut error
	if t, erd = tryParseRelativeDuration(s); erd == nil {
		return t, nil
	}
	if t, eat = tryParseAbsoluteTime(s); eat == nil {
		return t, nil
	}
	if t, eut = tryParseUnixTimestamp(s); eut == nil {
		return t, nil
	}
	return time.Time{}, ErrTimeRangeParsingFailed
}

func tryParseRelativeDuration(s string) (time.Time, error) {
	d, err := timeconv.ParseDuration(s)
	if err != nil {
		return time.Time{}, err
	}
	return time.Now().Add(d), nil
}

func tryParseAbsoluteTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

func tryParseUnixTimestamp(s string) (time.Time, error) {
	unix, err := strconv.Atoi(s)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(int64(unix), 0).UTC(), nil
}

func tokenizeRangeLine(input string, funcStart int) string {
	i := strings.Index(input[funcStart:], ")")
	if i < 0 {
		return input
	}
	return input[:funcStart+len(FuncRange)] + TokenPlaceholderTimeRange + input[funcStart+i:]
}
