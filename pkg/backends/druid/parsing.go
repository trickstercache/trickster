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
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/druid/model"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	intervalPlaceholder = "__TRICKSTER_DRUID_INTERVAL__"
	queryTypeTimeseries = "timeseries"
	queryTypeGroupBy    = "groupby"
	queryTypeTopN       = "topn"
)

var transientContextKeys = []string{
	"priority", "queryDeadline", "queryId", "sqlQueryId", "timeout",
}

var fixedSimpleGranularities = map[string]time.Duration{
	"second":         time.Second,
	"minute":         time.Minute,
	"five_minute":    5 * time.Minute,
	"ten_minute":     10 * time.Minute,
	"fifteen_minute": 15 * time.Minute,
	"thirty_minute":  30 * time.Minute,
	"hour":           time.Hour,
	"six_hour":       6 * time.Hour,
	"eight_hour":     8 * time.Hour,
	"day":            24 * time.Hour,
}

var nonFixedSimpleGranularities = map[string]struct{}{
	"all": {}, "none": {}, "week": {}, "month": {}, "quarter": {}, "year": {},
}

// ParseTimeRangeQuery builds a canonical cache identity and immutable rewrite
// plan from a native Druid JSON query.
func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error,
) {
	if r == nil || r.Method != http.MethodPost {
		return c.reject(nil, nil, false, modeProxy, reasonUnsupportedMethod, errInvalidRequest)
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get(headers.NameContentType))
	if err != nil || !strings.EqualFold(mediaType, headers.ValueApplicationJSON) {
		return c.reject(nil, nil, false, modeProxy, reasonUnsupportedContentType, errInvalidRequest)
	}
	body, err := request.GetBody(r)
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return c.reject(nil, nil, false, modeProxy, reasonInvalidJSON, errInvalidJSON)
	}
	document, err := decodeJSONObject(body)
	if err != nil {
		return c.reject(nil, nil, false, modeProxy, reasonInvalidJSON, errInvalidJSON)
	}

	sanitized, contextOK := canonicalDocument(document)
	canonical, _, _, err := marshalJSONObject(sanitized, nil)
	if err != nil {
		return c.reject(nil, nil, false, modeProxy, reasonInvalidJSON, errInvalidJSON)
	}
	queryType, _ := document["queryType"].(string)
	queryType = strings.ToLower(queryType)
	dataSource := canonicalValue(document["dataSource"])
	originalBody := bytes.Clone(body)
	trq := &timeseries.TimeRangeQuery{
		Statement:        string(canonical),
		OriginalBody:     originalBody,
		CacheKeyElements: map[string]string{"query": string(canonical), "dataSource": dataSource},
	}
	if r.URL != nil {
		trq.TemplateURL = urls.Clone(r.URL)
	}
	ro := &timeseries.RequestOptions{FastForwardDisable: true}

	if !slices.Contains([]string{queryTypeTimeseries, queryTypeGroupBy, queryTypeTopN}, queryType) {
		return c.reject(trq, ro, true, modeObject, reasonUnsupportedQueryType, errObjectCache)
	}
	if !contextOK {
		return c.reject(trq, ro, true, modeObject, reasonInvalidContext, errObjectCache)
	}
	if !responseShapeSupported(queryType, document) {
		return c.reject(trq, ro, true, modeObject, reasonUnsupportedShape, errObjectCache)
	}

	intervals, ok := stringList(document["intervals"])
	if !ok || len(intervals) == 0 {
		return c.reject(trq, ro, true, modeObject, reasonInvalidInterval, errObjectCache)
	}
	if len(intervals) != 1 {
		return c.reject(trq, ro, true, modeObject, reasonMultipleIntervals, errObjectCache)
	}

	step, phase, reason, ok := parseGranularity(document["granularity"])
	if !ok {
		return c.reject(trq, ro, true, modeObject, reason, errObjectCache)
	}
	start, endExclusive, err := parseInterval(intervals[0])
	if err != nil {
		return c.reject(trq, ro, true, modeObject, reasonInvalidInterval, errObjectCache)
	}
	start = truncateToPhase(start, step, phase)
	end := truncateToPhase(endExclusive.Add(-time.Nanosecond), step, phase)
	if end.Before(start) {
		return c.reject(trq, ro, true, modeObject, reasonInvalidInterval, errObjectCache)
	}
	dimensions, dimensionsOK := dimensionNames(queryType, document)
	if !dimensionsOK {
		return c.reject(trq, ro, true, modeObject, reasonUnsupportedDimension, errObjectCache)
	}

	cacheBody, _, _, err := marshalJSONObject(sanitized,
		[]byte(`[`+strconv.Quote(intervalPlaceholder)+`]`))
	if err != nil {
		return c.reject(trq, ro, false, modeProxy, reasonInvalidJSON, errInvalidJSON)
	}
	renderBody, intervalStart, intervalEnd, err := marshalJSONObject(document,
		[]byte(`[`+strconv.Quote(intervalPlaceholder)+`]`))
	if err != nil || intervalStart < 0 || intervalEnd < intervalStart {
		return c.reject(trq, ro, false, modeProxy, reasonInvalidJSON, errInvalidJSON)
	}

	plan := model.NewQueryPlan(queryType, dimensions, valueFieldNames(document),
		booleanValue(document["descending"]), renderBody[:intervalStart], renderBody[intervalEnd:])
	trq.Statement = string(cacheBody)
	trq.CacheKeyElements["query"] = trq.Statement
	trq.Step = step
	trq.StepNS = step.Nanoseconds()
	trq.Phase = phase
	trq.Extent = timeseries.Extent{Start: start, End: end}
	trq.ParsedQuery = plan
	trq.BackfillTolerance = druidBackfillTolerance(r)
	ro.ProviderRequest = plan
	c.observeAnalysis(modeDelta, reasonEligible)
	return trq, ro, true, nil
}

func (c *Client) reject(trq *timeseries.TimeRangeQuery, ro *timeseries.RequestOptions,
	canOPC bool, mode analysisMode, reason analysisReason, base error,
) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	c.observeAnalysis(mode, reason)
	return trq, ro, canOPC, newClassifiedError(base, reason)
}

func decodeJSONObject(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil || out == nil {
		return nil, errInvalidJSON
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errInvalidJSON
	}
	return out, nil
}

func canonicalDocument(document map[string]any) (map[string]any, bool) {
	out := cloneObject(document)
	context, exists := out["context"]
	if !exists {
		return out, true
	}
	contextMap, ok := context.(map[string]any)
	if !ok {
		return out, false
	}
	for _, key := range transientContextKeys {
		delete(contextMap, key)
	}
	if len(contextMap) == 0 {
		delete(out, "context")
	}
	return out, true
}

func cloneObject(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneJSONValue(value)
	}
	return out
}

func cloneJSONValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneObject(v)
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = cloneJSONValue(v[i])
		}
		return out
	default:
		return value
	}
}

// marshalJSONObject uses encoding/json for every value (which recursively
// sorts map keys) while permitting the top-level intervals value to be
// replaced without mutating the parsed document. The returned offsets delimit
// that replacement in the output, or are -1 when no replacement was requested.
func marshalJSONObject(document map[string]any, intervals []byte) ([]byte, int, int, error) {
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	start, end := -1, -1
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, _ := json.Marshal(key)
		buf.Write(keyJSON)
		buf.WriteByte(':')
		if key == "intervals" && intervals != nil {
			start = buf.Len()
			buf.Write(intervals)
			end = buf.Len()
			continue
		}
		valueJSON, err := json.Marshal(document[key])
		if err != nil {
			return nil, -1, -1, err
		}
		buf.Write(valueJSON)
	}
	buf.WriteByte('}')
	return buf.Bytes(), start, end, nil
}

func canonicalValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	b, _ := json.Marshal(value)
	return string(b)
}

func stringList(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i], ok = value.(string)
		if !ok {
			return nil, false
		}
	}
	return out, true
}

func parseInterval(value string) (time.Time, time.Time, error) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 {
		return time.Time{}, time.Time{}, errInvalidJSON
	}
	start, err := parseDruidTime(parts[0])
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := parseDruidTime(parts[1])
	if err != nil || !end.After(start) || !unixNanoRepresentable(start) ||
		!unixNanoRepresentable(end) {
		return time.Time{}, time.Time{}, errInvalidJSON
	}
	return start, end, nil
}

func unixNanoRepresentable(value time.Time) bool {
	return time.Unix(0, value.UnixNano()).UTC().Equal(value.UTC())
}

func parseDruidTime(value string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04",
		"2006-01-02",
	} {
		if strings.Contains(layout, "Z07:00") {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed, nil
			}
		} else if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, errors.New("invalid Druid timestamp")
}

func parseGranularity(value any) (time.Duration, time.Duration, analysisReason, bool) {
	if simple, ok := value.(string); ok {
		simple = strings.ToLower(simple)
		if step, found := fixedSimpleGranularities[simple]; found {
			return step, 0, reasonEligible, true
		}
		if _, found := nonFixedSimpleGranularities[simple]; found {
			return 0, 0, reasonNonFixedGranularity, false
		}
		return 0, 0, reasonUnsupportedGranularity, false
	}
	granularity, ok := value.(map[string]any)
	if !ok {
		return 0, 0, reasonUnsupportedGranularity, false
	}
	typeName, _ := granularity["type"].(string)
	switch strings.ToLower(typeName) {
	case "duration":
		millis, ok := integerValue(granularity["duration"])
		if !ok || millis <= 0 || millis > math.MaxInt64/int64(time.Millisecond) {
			return 0, 0, reasonUnsupportedGranularity, false
		}
		step := time.Duration(millis) * time.Millisecond
		phase, ok := granularityPhase(granularity["origin"], step, time.Time{})
		if !ok {
			return 0, 0, reasonUnsupportedGranularity, false
		}
		return step, phase, reasonEligible, true
	case "period":
		zone, zoneOK := granularity["timeZone"].(string)
		if _, exists := granularity["timeZone"]; exists && !zoneOK {
			return 0, 0, reasonUnsupportedGranularity, false
		}
		if !isUTCZone(zone) {
			return 0, 0, reasonNonFixedGranularity, false
		}
		period, _ := granularity["period"].(string)
		step, defaultOrigin, calendar, ok := fixedPeriod(period)
		if !ok {
			if calendar {
				return 0, 0, reasonNonFixedGranularity, false
			}
			return 0, 0, reasonUnsupportedGranularity, false
		}
		phase, ok := granularityPhase(granularity["origin"], step, defaultOrigin)
		if !ok {
			return 0, 0, reasonUnsupportedGranularity, false
		}
		return step, phase, reasonEligible, true
	default:
		return 0, 0, reasonUnsupportedGranularity, false
	}
}

func responseShapeSupported(queryType string, document map[string]any) bool {
	context, _ := document["context"].(map[string]any)
	if booleanValue(context["bySegment"]) || booleanValue(context["serializeDateTimeAsLong"]) {
		return false
	}
	switch queryType {
	case queryTypeTimeseries:
		return !booleanValue(context["grandTotal"])
	case queryTypeGroupBy:
		return !booleanValue(context["resultAsArray"])
	default:
		return true
	}
}

func integerValue(value any) (int64, bool) {
	switch v := value.(type) {
	case json.Number:
		n, err := strconv.ParseInt(string(v), 10, 64)
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func granularityPhase(value any, step time.Duration, defaultOrigin time.Time) (time.Duration, bool) {
	if step <= 0 {
		return 0, false
	}
	origin := defaultOrigin
	if value != nil {
		originText, ok := value.(string)
		if !ok {
			return 0, false
		}
		var err error
		origin, err = parseDruidTime(originText)
		if err != nil || !unixNanoRepresentable(origin) {
			return 0, false
		}
	}
	if origin.IsZero() {
		return 0, true
	}
	phase := time.Duration(origin.UnixNano() % step.Nanoseconds())
	if phase < 0 {
		phase += step
	}
	return phase, true
}

// fixedPeriod accepts ISO-8601 periods whose length is invariant in UTC. Year
// and month components are calendar-width and deliberately rejected.
func fixedPeriod(value string) (time.Duration, time.Time, bool, bool) {
	if value == "" || !strings.HasPrefix(value, "P") {
		return 0, time.Time{}, false, false
	}
	datePart, timePart := strings.TrimPrefix(value, "P"), ""
	if index := strings.IndexByte(datePart, 'T'); index >= 0 {
		timePart = datePart[index+1:]
		datePart = datePart[:index]
	}
	years, months, weeks, days, ok := parseDatePeriod(datePart)
	if !ok {
		return 0, time.Time{}, false, false
	}
	if years != 0 || months != 0 {
		return 0, time.Time{}, true, false
	}
	hours, minutes, seconds, ok := parseTimePeriod(timePart)
	if !ok {
		return 0, time.Time{}, false, false
	}
	step, ok := composePeriodDuration(weeks, days, hours, minutes, seconds)
	if !ok || step <= 0 {
		return 0, time.Time{}, false, false
	}
	var defaultOrigin time.Time
	if weeks > 0 {
		// Druid's default week boundary is Monday in the selected time zone.
		defaultOrigin = time.Date(1970, 1, 5, 0, 0, 0, 0, time.UTC)
	}
	return step, defaultOrigin, false, true
}

func parseDatePeriod(value string) (years, months, weeks, days int64, ok bool) {
	fields := []struct {
		suffix byte
		value  *int64
	}{{'Y', &years}, {'M', &months}, {'W', &weeks}, {'D', &days}}
	for _, field := range fields {
		index := strings.IndexByte(value, field.suffix)
		if index < 0 {
			continue
		}
		if index == 0 {
			return 0, 0, 0, 0, false
		}
		n, err := strconv.ParseInt(value[:index], 10, 64)
		if err != nil || n < 0 {
			return 0, 0, 0, 0, false
		}
		*field.value = n
		value = value[index+1:]
	}
	return years, months, weeks, days, value == ""
}

func parseTimePeriod(value string) (hours, minutes int64, seconds time.Duration, ok bool) {
	if value == "" {
		return 0, 0, 0, true
	}
	for _, field := range []struct {
		suffix byte
		value  *int64
	}{{'H', &hours}, {'M', &minutes}} {
		index := strings.IndexByte(value, field.suffix)
		if index < 0 {
			continue
		}
		if index == 0 {
			return 0, 0, 0, false
		}
		n, err := strconv.ParseInt(value[:index], 10, 64)
		if err != nil || n < 0 {
			return 0, 0, 0, false
		}
		*field.value = n
		value = value[index+1:]
	}
	if value == "" {
		return hours, minutes, 0, true
	}
	if !strings.HasSuffix(value, "S") {
		return 0, 0, 0, false
	}
	seconds, err := time.ParseDuration(strings.TrimSuffix(value, "S") + "s")
	return hours, minutes, seconds, err == nil && seconds >= 0
}

func composePeriodDuration(weeks, days, hours, minutes int64,
	seconds time.Duration,
) (time.Duration, bool) {
	total := int64(seconds)
	parts := []struct {
		value int64
		unit  time.Duration
	}{
		{weeks, 7 * 24 * time.Hour},
		{days, 24 * time.Hour},
		{hours, time.Hour},
		{minutes, time.Minute},
	}
	for _, partValue := range parts {
		value, unit := partValue.value, partValue.unit
		if value > math.MaxInt64/int64(unit) {
			return 0, false
		}
		part := value * int64(unit)
		if total > math.MaxInt64-part {
			return 0, false
		}
		total += part
	}
	return time.Duration(total), true
}

func isUTCZone(zone string) bool {
	switch strings.ToUpper(zone) {
	case "", "UTC", "ETC/UTC", "GMT", "ETC/GMT":
		return true
	default:
		return false
	}
}

func truncateToPhase(value time.Time, step, phase time.Duration) time.Time {
	stepNS := step.Nanoseconds()
	shifted := value.UnixNano() - phase.Nanoseconds()
	quotient := shifted / stepNS
	if shifted < 0 && shifted%stepNS != 0 {
		quotient--
	}
	return time.Unix(0, quotient*stepNS+phase.Nanoseconds()).In(value.Location())
}

func dimensionNames(queryType string, document map[string]any) ([]string, bool) {
	var values []any
	switch queryType {
	case queryTypeGroupBy:
		value, exists := document["dimensions"]
		if !exists {
			return nil, true
		}
		var ok bool
		values, ok = value.([]any)
		if !ok {
			return nil, false
		}
	case queryTypeTopN:
		if _, exists := document["dimension"]; !exists {
			return nil, false
		}
		values = []any{document["dimension"]}
	default:
		return nil, true
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		name, ok := dimensionName(value)
		if !ok || slices.Contains(out, name) {
			return nil, false
		}
		out = append(out, name)
	}
	return out, true
}

func dimensionName(value any) (string, bool) {
	if name, ok := value.(string); ok {
		return name, name != ""
	}
	dimension, ok := value.(map[string]any)
	if !ok {
		return "", false
	}
	if output, exists := dimension["outputName"]; exists {
		name, ok := output.(string)
		return name, ok && name != ""
	}
	if name, ok := dimension["dimension"].(string); ok && name != "" {
		return name, true
	}
	if delegate, exists := dimension["delegate"]; exists {
		return dimensionName(delegate)
	}
	return "", false
}

func valueFieldNames(document map[string]any) []string {
	var out []string
	for _, key := range []string{"aggregations", "postAggregations"} {
		values, _ := document[key].([]any)
		for _, value := range values {
			definition, _ := value.(map[string]any)
			name, _ := definition["name"].(string)
			if name != "" && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

func booleanValue(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		parsed, _ := strconv.ParseBool(v)
		return parsed
	default:
		return false
	}
}

func druidBackfillTolerance(r *http.Request) time.Duration {
	const defaultTolerance = time.Minute
	resources := request.GetResources(r)
	if resources == nil || resources.BackendOptions == nil {
		return defaultTolerance
	}
	configured := time.Duration(resources.BackendOptions.BackfillTolerance)
	if configured == 0 {
		return defaultTolerance
	}
	return configured
}
