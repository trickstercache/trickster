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

package elasticsearch

import (
	"bytes"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	queryParamSource = "source"

	aggKeyAggs         = "aggs"
	aggKeyAggregations = "aggregations"

	rangeStartToken = "<$START$>"
	rangeEndToken   = "<$END$>"
)

type requestKind byte

const (
	requestKindSearch requestKind = iota
	requestKindMSearch
)

// RequestPlan is the parsed provider request used by SetExtent and response marshaling.
type RequestPlan struct {
	Kind       requestKind
	Searches   []*SearchPlan
	SourceBody bool
}

// SearchPlan is one Elasticsearch search body inside a request.
type SearchPlan struct {
	Header             map[string]any
	Body               map[string]any
	AggregationMeta    map[string]any
	DateHistogramName  string
	TimestampField     string
	TimestampValueKind timestampValueKind
	RangeEndExclusive  bool
	Statement          string
	Extent             timeseries.Extent
	Step               time.Duration
}

type timestampValueKind byte

const (
	timestampValueRFC3339 timestampValueKind = iota
	timestampValueEpochSeconds
	timestampValueEpochMillis
)

func (c *Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error,
) {
	if r == nil || r.URL == nil {
		return nil, nil, false, errors.ErrNotTimeRangeQuery
	}
	isSearch := isSearchPath(r.URL.Path)
	isMSearch := isMSearchPath(r.URL.Path)
	if !isSearch && !isMSearch {
		return nil, nil, false, errors.ErrNotTimeRangeQuery
	}

	body, sourceBody, err := readSearchBody(r)
	if err != nil {
		return nil, nil, false, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil, false, errors.ErrNotTimeRangeQuery
	}

	opts := c.elasticsearchOptions()
	plan, trq, ro, err := parseRequestPlan(body, sourceBody, isMSearch, opts.TimestampField)
	if err == nil && hasUnsafeSearchQueryParams(r, sourceBody) {
		setFallbackCacheKey(trq, body, sourceBody)
		err = errors.ErrNotTimeRangeQuery
	}
	if trq != nil {
		trq.OriginalBody = body
		trq.ParsedQuery = plan
	}
	if ro != nil {
		ro.ProviderRequest = plan
	}
	return trq, ro, true, err
}

func readSearchBody(r *http.Request) ([]byte, bool, error) {
	if methods.HasBody(r.Method) {
		body, err := request.GetBody(r)
		if err != nil && !stderrors.Is(err, io.EOF) {
			return nil, false, err
		}
		if len(bytes.TrimSpace(body)) > 0 {
			return body, false, nil
		}
	}
	if r.Method == http.MethodGet && r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, false, err
		}
		request.SetBody(r, body)
		if len(bytes.TrimSpace(body)) > 0 {
			return body, false, nil
		}
	}
	if source := r.URL.Query().Get(queryParamSource); source != "" {
		return []byte(source), true, nil
	}
	return nil, false, nil
}

func parseRequestPlan(body []byte, sourceBody, isMSearch bool, timestampField string) (*RequestPlan,
	*timeseries.TimeRangeQuery, *timeseries.RequestOptions, error,
) {
	if isMSearch {
		plan, trq, ro, err := parseMSearchPlan(body, timestampField)
		if err != nil {
			if plan == nil {
				plan = &RequestPlan{Kind: requestKindMSearch}
			}
			trq = timeRangeQueryFromSearches(plan.Searches, exactRequestStatement(body))
			ro = &timeseries.RequestOptions{ProviderRequest: plan}
		}
		return plan, trq, ro, err
	}
	sp, normalized, err := parseSearchPlan(nil, body, timestampField)
	plan := &RequestPlan{Kind: requestKindSearch, Searches: []*SearchPlan{sp}, SourceBody: sourceBody}
	statement := normalized
	if err != nil {
		statement = exactRequestStatement(body)
	}
	trq := timeRangeQueryFromSearches(plan.Searches, statement)
	setStatementCacheKey(trq, sourceBody)
	ro := &timeseries.RequestOptions{ProviderRequest: plan}
	if err != nil {
		return plan, trq, ro, err
	}
	return plan, trq, ro, nil
}

func parseMSearchPlan(body []byte, timestampField string) (*RequestPlan,
	*timeseries.TimeRangeQuery, *timeseries.RequestOptions, error,
) {
	lines := splitNDJSON(body)
	if len(lines) == 0 || len(lines)%2 != 0 {
		return nil, nil, nil, timeseries.ErrInvalidBody
	}
	plan := &RequestPlan{Kind: requestKindMSearch, Searches: make([]*SearchPlan, 0, len(lines)/2)}
	normalizedParts := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i += 2 {
		header, err := decodeObject(lines[i])
		if err != nil {
			return nil, nil, nil, err
		}
		sp, normalized, err := parseSearchPlan(header, lines[i+1], timestampField)
		if sp != nil {
			plan.Searches = append(plan.Searches, sp)
		}
		hb, _ := canonicalJSON(header)
		normalizedParts = append(normalizedParts, string(hb), normalized)
		if err != nil {
			trq := timeRangeQueryFromSearches(plan.Searches, strings.Join(normalizedParts, "\n"))
			return plan, trq, &timeseries.RequestOptions{ProviderRequest: plan}, err
		}
	}
	normalized := strings.Join(normalizedParts, "\n")
	trq := timeRangeQueryFromSearches(plan.Searches, normalized)
	ro := &timeseries.RequestOptions{ProviderRequest: plan}
	if err := validateCommonRange(plan.Searches); err != nil {
		return plan, trq, ro, err
	}
	return plan, trq, ro, nil
}

func splitNDJSON(body []byte) [][]byte {
	raw := bytes.Split(body, []byte{'\n'})
	out := make([][]byte, 0, len(raw))
	for i, line := range raw {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			if i == len(raw)-1 {
				continue
			}
			return nil
		}
		out = append(out, line)
	}
	return out
}

func parseSearchPlan(header map[string]any, body []byte, timestampField string) (*SearchPlan, string, error) {
	m, err := decodeObject(body)
	if err != nil {
		return nil, "", err
	}
	sp := &SearchPlan{Header: header, Body: m, TimestampField: timestampField}
	if !isAggregationOnlySearch(m) {
		return sp, normalizedBodyForCache(m, timestampField), errors.ErrNotTimeRangeQuery
	}
	documentExtent, kind, endExclusive, ok := extractTimeRange(m, timestampField)
	if !ok {
		normalized := normalizedBodyForCache(m, timestampField)
		return sp, normalized, errors.ErrNotTimeRangeQuery
	}
	aggName, step, histogram, ok := extractDateHistogram(m, timestampField)
	if !ok {
		normalized := normalizedBodyForCache(m, timestampField)
		return sp, normalized, errors.ErrNotTimeRangeQuery
	}
	extent, ok := completeBucketExtent(documentExtent, step, kind, endExclusive)
	if !ok || !histogramSupportsIndependentExtents(histogram, extent, step) {
		normalized := normalizedBodyForCache(m, timestampField)
		return sp, normalized, errors.ErrNotTimeRangeQuery
	}
	sp.Extent = extent
	sp.Step = step
	sp.TimestampValueKind = kind
	sp.RangeEndExclusive = endExclusive
	sp.DateHistogramName = aggName
	sp.AggregationMeta = aggregationMeta(m, aggName)
	normalized := normalizedBodyForCache(m, timestampField)
	sp.Statement = normalized
	return sp, normalized, nil
}

func decodeObject(data []byte) (map[string]any, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, timeseries.ErrInvalidBody
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, timeseries.ErrInvalidBody
	}
	return m, nil
}

func timeRangeQueryFromSearches(searches []*SearchPlan, statement string) *timeseries.TimeRangeQuery {
	trq := &timeseries.TimeRangeQuery{
		Statement: statement,
		CacheKeyElements: map[string]string{
			"query": statement,
		},
	}
	if len(searches) == 0 || searches[0] == nil {
		return trq
	}
	trq.Extent = searches[0].Extent
	trq.Step = searches[0].Step
	trq.TimestampDefinition = timeseries.FieldDefinition{
		Name:     searches[0].TimestampField,
		DataType: timeseries.DateTimeUnixMilli,
		Role:     timeseries.RoleTimestamp,
	}
	return trq
}

func exactRequestStatement(body []byte) string {
	return string(bytes.TrimSpace(body))
}

func setFallbackCacheKey(trq *timeseries.TimeRangeQuery, body []byte, sourceBody bool) {
	if trq == nil {
		return
	}
	trq.Statement = exactRequestStatement(body)
	trq.CacheKeyElements = map[string]string{"query": trq.Statement}
	setStatementCacheKey(trq, sourceBody)
}

func setStatementCacheKey(trq *timeseries.TimeRangeQuery, sourceBody bool) {
	if trq == nil || !sourceBody {
		return
	}
	delete(trq.CacheKeyElements, "query")
	trq.CacheKeyElements[queryParamSource] = trq.Statement
}

func isAggregationOnlySearch(root map[string]any) bool {
	size, ok := root["size"].(json.Number)
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(size.String(), 10, 64)
	if err != nil || n != 0 {
		return false
	}
	for key := range root {
		switch key {
		case "size", "query", aggKeyAggs, aggKeyAggregations, "runtime_mappings":
		default:
			return false
		}
	}
	return true
}

func hasUnsafeSearchQueryParams(r *http.Request, sourceBody bool) bool {
	if r == nil || r.URL == nil {
		return true
	}
	for key := range r.URL.Query() {
		switch key {
		case "allow_no_indices", "allow_partial_search_results", "batched_reduce_size",
			"ccs_minimize_roundtrips", "error_trace", "expand_wildcards", "human",
			"ignore_throttled", "ignore_unavailable", "max_concurrent_shard_requests",
			"pre_filter_shard_size", "preference", "pretty", "request_cache", "routing",
			"source_content_type":
		case queryParamSource:
			if !sourceBody {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func validateCommonRange(searches []*SearchPlan) error {
	if len(searches) == 0 || searches[0] == nil {
		return errors.ErrNotTimeRangeQuery
	}
	first := searches[0]
	for _, sp := range searches[1:] {
		if sp == nil || sp.Step != first.Step ||
			!sp.Extent.Start.Equal(first.Extent.Start) ||
			!sp.Extent.End.Equal(first.Extent.End) {
			return errors.ErrNotTimeRangeQuery
		}
	}
	return nil
}

func extractTimeRange(root map[string]any, timestampField string) (timeseries.Extent,
	timestampValueKind, bool, bool,
) {
	query, ok := root["query"]
	if !ok {
		return timeseries.Extent{}, timestampValueRFC3339, false, false
	}
	ranges := requiredTimestampRanges(query, timestampField)
	if len(ranges) != 1 || countTimestampRanges(root, timestampField) != 1 {
		return timeseries.Extent{}, timestampValueRFC3339, false, false
	}
	fieldNode := ranges[0]
	if hasAnyKey(fieldNode, "gt", "from", "to", "time_zone", "relation") {
		return timeseries.Extent{}, timestampValueRFC3339, false, false
	}
	_, hasLTE := fieldNode["lte"]
	_, hasLT := fieldNode["lt"]
	if hasLTE == hasLT {
		return timeseries.Extent{}, timestampValueRFC3339, false, false
	}
	start, kind, ok := parseRangeTime(fieldNode["gte"], fieldNode)
	if !ok {
		return timeseries.Extent{}, timestampValueRFC3339, false, false
	}
	endValue := fieldNode["lte"]
	if hasLT {
		endValue = fieldNode["lt"]
	}
	end, endKind, ok := parseRangeTime(endValue, fieldNode)
	if !ok || endKind != kind || !start.Before(end) {
		return timeseries.Extent{}, timestampValueRFC3339, false, false
	}
	return timeseries.Extent{Start: start, End: end}, kind, hasLT, true
}

func requiredTimestampRanges(v any, timestampField string) []map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	if rangeNode, ok := m["range"].(map[string]any); ok {
		if fieldNode, ok := rangeNode[timestampField].(map[string]any); ok {
			return []map[string]any{fieldNode}
		}
		return nil
	}
	var out []map[string]any
	if boolNode, ok := m["bool"].(map[string]any); ok {
		for _, key := range []string{"filter", "must"} {
			out = append(out, requiredTimestampRangesFromClauses(boolNode[key], timestampField)...)
		}
	}
	if constantScore, ok := m["constant_score"].(map[string]any); ok {
		out = append(out, requiredTimestampRanges(constantScore["filter"], timestampField)...)
	}
	if functionScore, ok := m["function_score"].(map[string]any); ok {
		out = append(out, requiredTimestampRanges(functionScore["query"], timestampField)...)
	}
	return out
}

func requiredTimestampRangesFromClauses(v any, timestampField string) []map[string]any {
	switch clauses := v.(type) {
	case []any:
		var out []map[string]any
		for _, clause := range clauses {
			out = append(out, requiredTimestampRanges(clause, timestampField)...)
		}
		return out
	default:
		return requiredTimestampRanges(v, timestampField)
	}
}

func countTimestampRanges(root map[string]any, timestampField string) int {
	var count int
	walkMaps(root, func(m map[string]any) bool {
		rangeNode, ok := m["range"].(map[string]any)
		if !ok {
			return true
		}
		if _, ok := rangeNode[timestampField].(map[string]any); ok {
			count++
		}
		return true
	})
	return count
}

func completeBucketExtent(documentExtent timeseries.Extent, step time.Duration,
	kind timestampValueKind, endExclusive bool,
) (timeseries.Extent, bool) {
	if step < time.Millisecond || (24*time.Hour)%step != 0 ||
		!documentExtent.Start.Equal(documentExtent.Start.Truncate(step)) {
		return timeseries.Extent{}, false
	}
	if kind == timestampValueEpochSeconds && step%time.Second != 0 {
		return timeseries.Extent{}, false
	}
	endBoundary := documentExtent.End
	if !endExclusive {
		endBoundary = endBoundary.Add(time.Millisecond)
	}
	if !endBoundary.Equal(endBoundary.Truncate(step)) || !endBoundary.After(documentExtent.Start) {
		return timeseries.Extent{}, false
	}
	return timeseries.Extent{
		Start: documentExtent.Start,
		End:   endBoundary.Add(-step),
	}, true
}

func histogramSupportsIndependentExtents(histogram map[string]any, extent timeseries.Extent,
	step time.Duration,
) bool {
	minDocCount := int64(0)
	if raw, exists := histogram["min_doc_count"]; exists {
		n, ok := raw.(json.Number)
		if !ok {
			return false
		}
		var err error
		minDocCount, err = strconv.ParseInt(n.String(), 10, 64)
		if err != nil || minDocCount < 0 || minDocCount > 1 {
			return false
		}
	}

	for _, key := range []string{"extended_bounds", "hard_bounds"} {
		raw, exists := histogram[key]
		if !exists {
			if key == "extended_bounds" && minDocCount == 0 {
				return false
			}
			continue
		}
		bounds, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		if key == "extended_bounds" && minDocCount == 0 &&
			(!hasAnyKey(bounds, "min") || !hasAnyKey(bounds, "max")) {
			return false
		}
		for bound, expected := range map[string]time.Time{"min": extent.Start, "max": extent.End} {
			value, exists := bounds[bound]
			if !exists {
				continue
			}
			parsed, _, ok := parseRangeTime(value, histogram)
			if !ok || !parsed.Truncate(step).Equal(expected) {
				return false
			}
		}
	}
	return true
}

func hasAnyKey(m map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
}

func parseRangeTime(v any, fieldNode map[string]any) (time.Time, timestampValueKind, bool) {
	if v == nil {
		return time.Time{}, timestampValueRFC3339, false
	}
	format, _ := fieldNode["format"].(string)
	switch x := v.(type) {
	case json.Number:
		return parseNumericTime(x.String(), format)
	case float64:
		return parseNumericTime(strconv.FormatFloat(x, 'f', -1, 64), format)
	case int64:
		return parseNumericTime(strconv.FormatInt(x, 10), format)
	case string:
		if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
			return t, timestampValueRFC3339, true
		}
		return parseNumericTime(x, format)
	}
	return time.Time{}, timestampValueRFC3339, false
}

func parseNumericTime(input, format string) (time.Time, timestampValueKind, bool) {
	i, err := strconv.ParseInt(input, 10, 64)
	if err != nil {
		return time.Time{}, timestampValueRFC3339, false
	}
	secondPos := strings.Index(format, "epoch_second")
	millisPos := strings.Index(format, "epoch_millis")
	if secondPos >= 0 && (millisPos < 0 || secondPos < millisPos) {
		return time.Unix(i, 0), timestampValueEpochSeconds, true
	}
	return time.UnixMilli(i), timestampValueEpochMillis, true
}

func extractDateHistogram(root map[string]any, timestampField string) (string, time.Duration,
	map[string]any, bool,
) {
	_, hasAggs := root[aggKeyAggs]
	_, hasAggregations := root[aggKeyAggregations]
	if hasAggs && hasAggregations {
		return "", 0, nil, false
	}
	aggs, ok := aggregationMap(root)
	if !ok || len(aggs) != 1 {
		return "", 0, nil, false
	}
	keys := make([]string, 0, len(aggs))
	for key := range aggs {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		agg, ok := aggs[key].(map[string]any)
		if !ok {
			continue
		}
		if meta, exists := agg["meta"]; exists {
			if _, ok := meta.(map[string]any); !ok {
				continue
			}
		}
		dh, ok := agg["date_histogram"].(map[string]any)
		if !ok {
			continue
		}
		if field, _ := dh["field"].(string); field != timestampField {
			continue
		}
		if keyed, _ := dh["keyed"].(bool); keyed {
			continue
		}
		if _, hasOffset := dh["offset"]; hasOffset {
			continue
		}
		if _, hasScript := dh["script"]; hasScript {
			continue
		}
		if !hasAscendingKeyOrder(dh) || containsPipelineAggregation(agg) {
			continue
		}
		if tzValue, hasTimeZone := dh["time_zone"]; hasTimeZone {
			tz, ok := tzValue.(string)
			if !ok || !isUTCTimeZone(tz) {
				continue
			}
		}
		step, ok := parseHistogramInterval(dh)
		if ok {
			return key, step, dh, true
		}
	}
	return "", 0, nil, false
}

func aggregationMeta(root map[string]any, name string) map[string]any {
	aggs, ok := aggregationMap(root)
	if !ok {
		return nil
	}
	agg, ok := aggs[name].(map[string]any)
	if !ok {
		return nil
	}
	meta, _ := agg["meta"].(map[string]any)
	if meta == nil {
		return nil
	}
	return cloneMap(meta)
}

func hasAscendingKeyOrder(histogram map[string]any) bool {
	value, exists := histogram["order"]
	if !exists {
		return true
	}
	order, ok := value.(map[string]any)
	if !ok || len(order) != 1 {
		return false
	}
	direction, ok := order["_key"].(string)
	return ok && strings.EqualFold(direction, "asc")
}

func containsPipelineAggregation(aggregation map[string]any) bool {
	nested, ok := aggregationMap(aggregation)
	if !ok {
		return false
	}
	for _, raw := range nested {
		definition, ok := raw.(map[string]any)
		if !ok {
			return true
		}
		if isPipelineAggregation(definition) || containsPipelineAggregation(definition) {
			return true
		}
	}
	return false
}

func isPipelineAggregation(definition map[string]any) bool {
	for key := range definition {
		switch key {
		case "avg_bucket", "bucket_script", "bucket_selector", "bucket_sort",
			"cumulative_cardinality", "cumulative_sum", "derivative",
			"extended_stats_bucket", "inference", "max_bucket", "min_bucket",
			"moving_avg", "moving_fn", "moving_percentiles", "normalize",
			"percentiles_bucket", "serial_diff", "stats_bucket", "sum_bucket":
			return true
		}
	}
	return false
}

func aggregationMap(root map[string]any) (map[string]any, bool) {
	if aggs, ok := root[aggKeyAggs].(map[string]any); ok {
		return aggs, true
	}
	if aggs, ok := root[aggKeyAggregations].(map[string]any); ok {
		return aggs, true
	}
	return nil, false
}

func parseHistogramInterval(dh map[string]any) (time.Duration, bool) {
	value, hasFixed := dh["fixed_interval"]
	calendarValue, hasCalendar := dh["calendar_interval"]
	if hasFixed == hasCalendar || hasAnyKey(dh, "interval") {
		return 0, false
	}
	if hasCalendar {
		return parseFixedCalendarInterval(fmt.Sprint(calendarValue))
	}
	if d, ok := parseESDuration(fmt.Sprint(value)); ok {
		return d, true
	}
	return 0, false
}

func parseFixedCalendarInterval(input string) (time.Duration, bool) {
	input = strings.TrimSpace(input)
	switch input {
	case "1m":
		return time.Minute, true
	case "1h":
		return time.Hour, true
	case "1d":
		return 24 * time.Hour, true
	}
	switch strings.ToLower(input) {
	case "minute":
		return time.Minute, true
	case "hour":
		return time.Hour, true
	case "day":
		return 24 * time.Hour, true
	default:
		return 0, false
	}
}

func parseESDuration(input string) (time.Duration, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, false
	}
	if d, err := time.ParseDuration(input); err == nil {
		return d, d > 0
	}
	unit := input[len(input)-1]
	n, err := strconv.Atoi(input[:len(input)-1])
	if err != nil {
		return 0, false
	}
	switch unit {
	case 'd':
		return checkedDuration(n, 24*time.Hour)
	case 'w':
		return checkedDuration(n, 7*24*time.Hour)
	}
	return 0, false
}

func checkedDuration(n int, unit time.Duration) (time.Duration, bool) {
	if n <= 0 {
		return 0, false
	}
	d := time.Duration(n) * unit
	return d, d > 0 && d/time.Duration(n) == unit
}

func isUTCTimeZone(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "UTC", "Z", "+00:00", "-00:00":
		return true
	default:
		return false
	}
}

func walkMaps(v any, fn func(map[string]any) bool) bool {
	switch x := v.(type) {
	case map[string]any:
		if !fn(x) {
			return false
		}
		for _, child := range x {
			if !walkMaps(child, fn) {
				return false
			}
		}
	case []any:
		for _, child := range x {
			if !walkMaps(child, fn) {
				return false
			}
		}
	}
	return true
}

func normalizedBodyForCache(body map[string]any, timestampField string) string {
	clone := cloneMap(body)
	replaceRangeValues(clone, timestampField, rangeStartToken, rangeEndToken)
	replaceExtendedBounds(clone, timestampField, rangeStartToken, rangeEndToken)
	b, _ := canonicalJSON(clone)
	return string(b)
}

func cloneMap(in map[string]any) map[string]any {
	out := maps.Clone(in)
	for k, v := range out {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneMap(x)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = cloneValue(x[i])
		}
		return out
	default:
		return x
	}
}

func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func replaceRangeValues(root map[string]any, timestampField string, start, end any) bool {
	var replaced bool
	walkMaps(root, func(m map[string]any) bool {
		rangeNode, ok := m["range"].(map[string]any)
		if !ok {
			return true
		}
		fieldNode, ok := rangeNode[timestampField].(map[string]any)
		if !ok {
			return true
		}
		setRangeBound(fieldNode, start, "gte", "gt", "from")
		setRangeBound(fieldNode, end, "lte", "lt", "to")
		replaced = true
		return true
	})
	return replaced
}

func setRangeBound(m map[string]any, value any, keys ...string) {
	for _, key := range keys {
		if _, ok := m[key]; ok {
			m[key] = value
			return
		}
	}
}

func replaceExtendedBounds(root map[string]any, timestampField string, start, end any) {
	aggs, ok := aggregationMap(root)
	if !ok {
		return
	}
	for _, v := range aggs {
		agg, ok := v.(map[string]any)
		if !ok {
			continue
		}
		dh, ok := agg["date_histogram"].(map[string]any)
		if !ok {
			continue
		}
		if field, _ := dh["field"].(string); field != "" && field != timestampField {
			continue
		}
		for _, key := range []string{"extended_bounds", "hard_bounds"} {
			if bounds, ok := dh[key].(map[string]any); ok {
				if _, ok := bounds["min"]; ok {
					bounds["min"] = start
				}
				if _, ok := bounds["max"]; ok {
					bounds["max"] = end
				}
			}
		}
	}
}

// SetExtent rewrites the Elasticsearch search body to request the provided extent.
func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent,
) {
	if r == nil || trq == nil || extent == nil {
		return
	}
	plan, ok := trq.ParsedQuery.(*RequestPlan)
	if !ok || plan == nil {
		return
	}
	out := plan.bodyForExtent(extent)
	if plan.SourceBody {
		q := r.URL.Query()
		q.Set(queryParamSource, string(out))
		r.URL.RawQuery = q.Encode()
		return
	}
	request.SetBody(r, out)
}

func (p *RequestPlan) bodyForExtent(extent *timeseries.Extent) []byte {
	if p == nil {
		return nil
	}
	switch p.Kind {
	case requestKindMSearch:
		var buf bytes.Buffer
		for _, sp := range p.Searches {
			hb, _ := json.Marshal(sp.Header)
			bb, _ := json.Marshal(sp.bodyForExtent(extent))
			buf.Write(hb)
			_ = buf.WriteByte('\n')
			buf.Write(bb)
			_ = buf.WriteByte('\n')
		}
		return buf.Bytes()
	default:
		if len(p.Searches) == 0 {
			return nil
		}
		b, _ := json.Marshal(p.Searches[0].bodyForExtent(extent))
		return b
	}
}

func (sp *SearchPlan) bodyForExtent(extent *timeseries.Extent) map[string]any {
	body := cloneMap(sp.Body)
	rangeStart, rangeEnd := sp.formatRangeExtent(extent)
	replaceRangeValues(body, sp.TimestampField, rangeStart, rangeEnd)
	rewriteHistogramBounds(body, sp.TimestampField, extent.Start, extent.End)
	return body
}

func (sp *SearchPlan) formatRangeExtent(extent *timeseries.Extent) (any, any) {
	end := extent.End.Add(sp.Step)
	if !sp.RangeEndExclusive {
		end = end.Add(-time.Millisecond)
	}
	return sp.formatTimestamp(extent.Start), sp.formatTimestamp(end)
}

func (sp *SearchPlan) formatTimestamp(t time.Time) any {
	return formatTimestamp(t, sp.TimestampValueKind)
}

func formatTimestamp(t time.Time, kind timestampValueKind) any {
	switch kind {
	case timestampValueEpochSeconds:
		return t.Unix()
	case timestampValueEpochMillis:
		return t.UnixMilli()
	default:
		return t.UTC().Format(time.RFC3339Nano)
	}
}

func rewriteHistogramBounds(root map[string]any, timestampField string, start, end time.Time) {
	aggs, ok := aggregationMap(root)
	if !ok {
		return
	}
	for _, value := range aggs {
		agg, ok := value.(map[string]any)
		if !ok {
			continue
		}
		histogram, ok := agg["date_histogram"].(map[string]any)
		if !ok {
			continue
		}
		if field, _ := histogram["field"].(string); field != timestampField {
			continue
		}
		for _, key := range []string{"extended_bounds", "hard_bounds"} {
			bounds, ok := histogram[key].(map[string]any)
			if !ok {
				continue
			}
			for name, timestamp := range map[string]time.Time{"min": start, "max": end} {
				original, exists := bounds[name]
				if !exists {
					continue
				}
				_, kind, ok := parseRangeTime(original, histogram)
				if ok {
					bounds[name] = formatTimestamp(timestamp, kind)
				}
			}
		}
	}
}

// FastForwardRequest is not supported for Elasticsearch.
func (c *Client) FastForwardRequest(_ *http.Request) (*http.Request, error) {
	return nil, nil
}
