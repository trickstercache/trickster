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
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	te "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	FuncRange           = "|> range("
	FuncAggregateWindow = "|> aggregateWindow("
	FuncWindow          = "|> window("

	TokenEvery     = "every:"
	TokenStart     = "start:"
	TokenStop      = "stop:"
	TokenCommaStop = "," + TokenStop
	TokenTimeSrc   = "timeSrc:"
	TokenOffset    = "offset:"
	TokenPeriod    = "period:"
	TokenLocation  = "location:"

	timeSrcStart = `"_start"`
	timeSrcStop  = `"_stop"`

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

// ErrUnsupportedWindow indicates a windowing function whose output timestamps do not fall on a
// fixed step grid that Trickster can map to and from the query's time range
var ErrUnsupportedWindow = errors.New("unsupported flux window for delta caching")

// ErrCrossBucket indicates a stage whose value in one bucket depends on other buckets or on the
// whole range, so a sub-range fetch would compute a different value
var ErrCrossBucket = errors.New("flux stages that span time buckets cannot be delta cached")

// ErrMultipleSources indicates more than one from(), whose ranges are not rewritten together
var ErrMultipleSources = errors.New("flux queries with more than one from() cannot be delta cached")

const (
	pipeForward         = "|>"
	funcFrom            = "from"
	funcAggregateWindow = "aggregateWindow"
	funcFill            = "fill"
	funcTimeShift       = "timeShift"
	tokenUsePrevious    = "usePrevious:"
	fluxTrue            = "true"
)

var crossBucketStages = map[string]struct{}{
	"limit": {}, "tail": {}, "derivative": {}, "difference": {}, "cumulativeSum": {},
	"movingAverage": {}, "exponentialMovingAverage": {}, "doubleEMA": {}, "tripleEMA": {},
	"timedMovingAverage": {}, "kaufmansAMA": {}, "kaufmansER": {}, "chandeMomentumOscillator": {},
	"relativeStrengthIndex": {}, "tripleExponentialDerivative": {}, "holtWinters": {},
	"elapsed": {}, "stateDuration": {}, "stateCount": {}, "integral": {},
}

var wholeRangeStages = map[string]struct{}{
	"mean": {}, "median": {}, "mode": {}, "sum": {}, "count": {}, "min": {}, "max": {},
	"first": {}, "last": {}, "quantile": {}, "spread": {}, "stddev": {}, "top": {}, "bottom": {},
	"highestMax": {}, "highestAverage": {}, "highestCurrent": {}, "lowestMin": {},
	"lowestAverage": {}, "lowestCurrent": {}, "unique": {}, "distinct": {}, "sample": {},
	"reduce": {}, "sort": {},
}

type Query struct {
	original      string
	tokenized     string
	step          time.Duration
	extent        timeseries.Extent
	labelsAtStart bool
}

type windowSpec struct {
	step, phase   time.Duration
	labelsAtStart bool
}

func (q *Query) rangeBounds(e timeseries.Extent, step time.Duration) (time.Time, time.Time) {
	// returns the range() bounds whose windows are labeled e.Start through e.End, inclusive;
	// aggregateWindow labels each window with its stop time unless timeSrc is "_start"
	if q.labelsAtStart {
		return e.Start, e.End.Add(step)
	}
	return e.Start.Add(-step), e.End
}

type JSONRequestBody struct {
	Query   string                 `json:"query"`
	Type    string                 `json:"type"`
	Dialect JSONRequestBodyDialect `json:"dialect,omitzero"`
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

type Response struct {
	Results []Result `json:"results"`
}

type Result struct {
	Tables []Table `json:"tables"`
}

type Table struct {
	Columns []Column `json:"columns"`
	Records []Record `json:"records"`
}

type Column struct {
	Name     string `json:"name"`
	Datatype string `json:"datatype"`
}

type Record struct {
	Values map[string]any `json:"values"`
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

const stepAlignments = timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate // _stop labels move partial buckets

func ParseTimeRangeQuery(r *http.Request,
	f iofmt.Format) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions,
	bool, error,
) {
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
	tokenizedStmt, extent, w, err := parseQuery(frb.Query)
	if err != nil {
		return nil, nil, false, err
	}
	q := &Query{
		original:      frb.Query,
		tokenized:     tokenizedStmt,
		step:          w.step,
		extent:        extent,
		labelsAtStart: w.labelsAtStart,
	}
	trq.Requested = timeseries.RequestedRange{Start: extent.Start, End: extent.End}
	trq.StepAlignments, trq.StepAlignment = stepAlignments, timeseries.StepAlignmentTruncate
	if w.step > 0 {
		trq.SampleModel = timeseries.SampleModelBucket
		if !w.labelsAtStart {
			trq.SampleModel = timeseries.SampleModelBucketStop
			// windows labeled by stop time put the first label at or after the range start
			// one step beyond it
			if extent.Start = extent.Start.Add(w.step); extent.Start.After(extent.End) {
				extent.Start = extent.End
			}
		}
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
	trq.Step = w.step
	trq.Phase = w.phase
	trq.Statement = tokenizedStmt
	trq.TemplateURL = urls.Clone(r.URL)
	trq.Extent = extent
	rlo.ProviderRequest = frb
	return trq, rlo, false, nil
}

func formatRangeTime(t time.Time) string {
	// whole seconds keep the compact Unix form; sub-second bounds need a time literal
	if t.Nanosecond() == 0 {
		return strconv.FormatInt(t.Unix(), 10)
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func rangeExtent(trq *timeseries.TimeRangeQuery) timeseries.Extent {
	if trq == nil {
		return timeseries.Extent{}
	}
	if q, ok := trq.ParsedQuery.(*Query); ok {
		start, stop := q.rangeBounds(trq.Extent, trq.Step)
		return timeseries.Extent{Start: start, End: stop}
	}
	return trq.Extent
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
	e *timeseries.Extent, q *Query,
) {
	// the bounds span at least one window, so a single-window extent never hits Flux's
	// "cannot query an empty range" error
	start, stop := q.rangeBounds(*e, trq.Step)

	// this creates a new flux query string with TokenPlaceholderTimeRange
	// replaced with the range bounds for Extent e
	s := strings.ReplaceAll(q.tokenized, TokenPlaceholderTimeRange,
		TokenStart+" "+formatRangeTime(start)+", "+TokenStop+" "+formatRangeTime(stop))
	// this reads the JSON body, unmarshals it to a map[string]any, swaps in the
	// transformed query, marshals it back to a []byte and sets r.Body to it.
	b, err := request.GetBody(r)
	if err != nil || len(b) == 0 {
		logger.Error(setExtentErrorLogEvent, logging.Pairs{keys.Error: err})
		return
	}
	var rb *JSONRequestBody
	switch {
	case headers.ProvidesContentType(r, headers.ValueApplicationFlux):
		rb = vndfluxToJSON(b)
		r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	case headers.ProvidesContentType(r, headers.ValueApplicationJSON):
		err = json.Unmarshal(b, &rb)
		if err != nil || rb == nil {
			logger.Error(setExtentErrorLogEvent, logging.Pairs{keys.Error: err})
			return
		}
	default:
		rb = &JSONRequestBody{}
		r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	}
	rb.Query = s
	rb.Dialect = JSONRequestBodyDialect{
		Annotations: DefaultAnnotations(),
	}
	b, err = json.Marshal(rb)
	if err != nil {
		logger.Error(setExtentErrorLogEvent, logging.Pairs{keys.Error: err})
		return
	}
	request.SetBody(r, b)
}

// ParseQuery tokenizes the query's range() and returns the range extent and the
// aggregateWindow step
func ParseQuery(input string) (string, timeseries.Extent, time.Duration, error) {
	s, e, w, err := parseQuery(input)
	return s, e, w.step, err
}

func parseQuery(input string) (string, timeseries.Extent, windowSpec, error) {
	var e timeseries.Extent
	var w windowSpec
	var err error
	if err = checkStages(input); err != nil {
		return "", e, w, err
	}
	// this puts all pipe operations on their own line
	input = strings.ReplaceAll(input, "|>", "\n|>")
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		ri := strings.Index(line, FuncRange)
		switch {
		case ri >= 0:
			e, err = parseRange(line)
			if err != nil {
				return "", e, w, err
			}
			lines[i] = tokenizeRangeLine(line, ri)
		case strings.Contains(line, FuncAggregateWindow):
			w, err = parseAggregateWindow(line)
			if err != nil {
				return "", e, w, err
			}
		case strings.Contains(line, FuncWindow):
			// window() output keeps each record's own _time, which is not on a step grid
			return "", e, w, ErrUnsupportedWindow
		}
	}
	return strings.Join(lines, "\n"), e, w, err
}

func checkStages(input string) error {
	if countCalls(input, funcFrom) > 1 {
		return ErrMultipleSources
	}
	// reducers are safe inside aggregateWindow's windows but collapse every bucket after it
	var windowed bool
	for stage := range strings.SplitSeq(input, pipeForward) {
		name := stageName(stage)
		switch name {
		case "":
			continue
		case funcTimeShift:
			return ErrUnsupportedWindow
		case funcFill:
			if v, ok := argValue(stage, tokenUsePrevious); ok && v == fluxTrue {
				return ErrCrossBucket
			}
		}
		if _, ok := crossBucketStages[name]; ok {
			return ErrCrossBucket
		}
		if _, ok := wholeRangeStages[name]; ok && windowed {
			return ErrCrossBucket
		}
		if name == funcAggregateWindow {
			windowed = true
		}
	}
	return nil
}

func stageName(stage string) string {
	stage = strings.TrimSpace(stage)
	if i := strings.IndexAny(stage, "( \t\r\n"); i >= 0 {
		return stage[:i]
	}
	return stage
}

func countCalls(input, name string) int {
	// counts name( calls, allowing space before the parenthesis and skipping longer identifiers
	var n int
	for i := strings.Index(input, name); i >= 0; {
		after := strings.TrimLeft(input[i+len(name):], " \t")
		if (i == 0 || !isIdentByte(input[i-1])) && strings.HasPrefix(after, "(") {
			n++
		}
		next := strings.Index(input[i+len(name):], name)
		if next < 0 {
			break
		}
		i += len(name) + next
	}
	return n
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func parseAggregateWindow(line string) (windowSpec, error) {
	step, err := parseStep(line)
	if err != nil {
		return windowSpec{}, err
	}
	w := windowSpec{step: step}
	// a period other than every overlaps windows, and a location shifts them by a zone offset
	if step <= 0 || strings.Contains(line, TokenLocation) {
		return w, ErrUnsupportedWindow
	}
	if strings.Contains(line, TokenPeriod) {
		v, ok := argValue(line, TokenPeriod)
		if !ok {
			return w, ErrUnsupportedWindow
		}
		if period, err := time.ParseDuration(v); err != nil || period != step {
			return w, ErrUnsupportedWindow
		}
	}
	if strings.Contains(line, TokenOffset) {
		v, ok := argValue(line, TokenOffset)
		if !ok {
			return w, ErrUnsupportedWindow
		}
		offset, err := time.ParseDuration(v)
		if err != nil {
			return w, err
		}
		// windows start on the Unix-epoch grid shifted by the offset
		if w.phase = offset % step; w.phase < 0 {
			w.phase += step
		}
	}
	if strings.Contains(line, TokenTimeSrc) {
		v, _ := argValue(line, TokenTimeSrc)
		switch v {
		case timeSrcStart:
			w.labelsAtStart = true
		case timeSrcStop:
		default:
			return w, ErrUnsupportedWindow
		}
	}
	return w, nil
}

func parseStep(input string) (time.Duration, error) {
	v, ok := argValue(input, TokenEvery)
	if !ok {
		return 0, ErrTimeRangeParsingFailed
	}
	return time.ParseDuration(v)
}

func argValue(input, token string) (string, bool) {
	i := strings.Index(input, token)
	if i < 0 {
		return "", false
	}
	input = input[i+len(token):]
	j := strings.IndexAny(input, ",)")
	if j < 0 {
		return "", false
	}
	return strings.TrimSpace(input[:j]), true
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
	if s == "now()" {
		return time.Now(), nil
	}
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

// tokenizeRangeLine replaces the body of `|> range(...)` with the placeholder,
// correctly matching the closing paren at the same nesting depth (so nested
// function calls like `now()` inside range() don't confuse the match).
func tokenizeRangeLine(input string, funcStart int) string {
	open := funcStart + len(FuncRange) - 1 // index of the `(` in `|> range(`
	if open >= len(input) || input[open] != '(' {
		return input
	}
	depth := 1
	close := -1
	for j := open + 1; j < len(input); j++ {
		switch input[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				close = j
			}
		}
		if close >= 0 {
			break
		}
	}
	if close < 0 {
		return input
	}
	return input[:open+1] + TokenPlaceholderTimeRange + input[close:]
}
