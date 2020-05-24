/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package clickhouse

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

// This file handles tokenization of time parameters within ClickHouse queries
// for cache key hashing and delta proxy caching.

// Tokens for String Interpolation
const (
	tkTimestamp1 = "<$TIMESTAMP1$>"
	tkTimestamp2 = "<$TIMESTAMP2$>"
)

const quote = byte('\'')
const bs = byte('\\')
const space = byte(' ')
const open = byte('(')
const close = byte(')')

var operators = map[byte]bool{
	byte('*'): true,
	byte('/'): true,
	byte(','): true,
	byte('+'): true,
	byte('-'): true,
	byte('>'): true,
	byte('<'): true,
	byte('='): true,
}

var timeFuncMap = map[string]string{
	"Minute":         "1m",
	"FiveMinute":     "5m",
	"FifteenMinutes": "15m",
	"Hour":           "1h",
}

var reTimeFieldAndStep, reTimeFuncAndStep, reTimeClauseAlt *regexp.Regexp

func init() {

	reTimeFieldAndStep = regexp.MustCompile(`(?i)select\s+\(\s*intdiv\s*\(\s*touint32\s*\(\s*` +
		`(?P<timeField>[a-zA-Z0-9\._-]+)\s*\)\s*,\s*(?P<step>[0-9]+)\s*\)\s*\*\s*[0-9]+\s*\)`)
	reTimeClauseAlt = regexp.MustCompile(`(?i)\s+(?P<expression>(?P<operator>>=|>|=|between)\s+` +
		`(?P<modifier>toDate(Time)?)\((?P<ts1>[0-9]+)\)(?P<timeExpr2>\s+and\s+toDate(Time)?\((?P<ts2>[0-9]+)\))?)`)
	reTimeFuncAndStep = regexp.MustCompile(`(?i)select\s+touint32\s*\(toStartOf(?P<step>[^\s]+)\s+\(?P<timeField>*\)`)
}

func interpolateTimeQuery(template string, extent *timeseries.Extent) string {
	return strings.Replace(strings.Replace(template, tkTimestamp1,
		strconv.Itoa(int(extent.Start.Unix())), -1), tkTimestamp2, strconv.Itoa(int(extent.End.Unix())), -1)
}

var compiledRe = make(map[string]*regexp.Regexp)

const timeClauseRe = `(?i)(?P<conjunction>where|and)\s+#TIME_FIELD#\s+(?P<timeExpr1>(?P<operator>>=|>|=|between)\s+` +
	`(?P<modifier>toDate(Time)?)\((?P<ts1>[0-9]+)\))(?P<timeExpr2>\s+and\s+toDate(Time)?\((?P<ts2>[0-9]+)\))?`

func parseRawQuery(query string, trq *timeseries.TimeRangeQuery) error {
	parts := findParts(query)
	size := len(parts)
	if size < 4 {
		return fmt.Errorf("unrecognized query Format")
	}
	if strings.ToUpper(parts[size-2]+" "+parts[size-1]) != "FORMAT JSON" {
		return fmt.Errorf("non JSON formats not supported")
	}

	var tsColumn, tsAlias string
	var startTime, endTime int
	for i := 0; i < size; i++ {
		p := parts[i]
		if strings.ToUpper(p) == "SELECT" {
			parts[i] = "SELECT"
			i++
			tsColumn = parts[i]
			if strings.HasSuffix(tsColumn, ",") {
				tsColumn = tsColumn[:len(tsColumn)-1]
			} else if strings.ToUpper(parts[i+1]) == "AS" {
				i += 2
				tsAlias = strings.Split(parts[i], ",")[0]
			}
			continue
		}
		if tsColumn != "" && (strings.ToUpper(parts[i]) == "PREWHERE" || strings.ToUpper(parts[i]) == "WHERE") {
			startTime, endTime = findRange(parts[i+1:], tsColumn, tsAlias)
			if startTime > 0 {
				break
			}
		}
	}

	if tsColumn == "" {
		return fmt.Errorf("no matching time value column found")
	}
	if startTime == 0 {
		return fmt.Errorf("no time range found")
	}
	trq.Statement = strings.Join(parts, " ")
	trq.Extent.Start = time.Unix(int64(startTime), int64(endTime))
	return nil
}

func findRange(parts []string, col string, alias string) (int, int) {
	return 0, 0
}

func findParts(query string) []string {
	bytes := []byte(strings.TrimSpace(query))
	size := len(bytes)
	buf := make([]byte, 0, size)
	inQuote := false
	escaped := false
	trimming := false
	fields := make([]string, 0, 30)
	fieldStart := 0
	for i := 0; i < size; i++ {
		b := bytes[i]
		if inQuote {
			if b == quote && !escaped {
				inQuote = false
			}
			escaped = !escaped && b == bs
			buf = append(buf, b)
			continue
		}
		if b == space {
			if trimming {
				continue
			}
			for i++; i < size; i++ {
				b = bytes[i]
				if b == close || operators[b] {
					break
				}
				if b != space {
					fields = append(fields, string(buf[fieldStart:]))
					buf = append(buf, space)
					fieldStart = len(buf)
					break
				}
			}
		}
		if b == quote {
			inQuote = true
		}
		trimming = b == open || operators[b]
		buf = append(buf, b)
	}
	return append(fields, string(buf[fieldStart:]))
}

/*func getQueryParts(query string, timeField string) (string, timeseries.Extent, bool, error) {




	tcKey := timeField + "-tc"
	trex, ok := compiledRe[tcKey]
	if !ok {
		trex = regexp.MustCompile(strings.Replace(timeClauseRe, "#TIME_FIELD#", timeField, -1))
		compiledRe[tcKey] = trex
	}

	m := matching.GetNamedMatches(trex, query, nil)
	if len(m) == 0 {
		return "", timeseries.Extent{}, false, fmt.Errorf("unable to parse time from query: %s", query)
	}

	ext, isRelativeTime, err := parseQueryExtents(query, m)
	if err != nil {
		return "", timeseries.Extent{}, false, err
	}

	tq := tokenizeQuery(query, m)
	return tq, ext, isRelativeTime, err
}

// tokenizeQuery will take a ClickHouse query and replace all time conditionals with a single <$TIME_TOKEN$>
func tokenizeQuery(query string, timeParts map[string]string) string {
	// First check the existence of timeExpr1, and if exists, tokenize
	if expr, ok := timeParts["timeExpr1"]; ok {
		if modifier, ok := timeParts["modifier"]; ok {
			query = strings.Replace(query, expr, fmt.Sprintf("BETWEEN %s(%s) AND %s(%s)",
				modifier, tkTimestamp1, modifier, tkTimestamp2), -1)
			// Then check the existence of timeExpr2, and if exists, remove from tokenized version
			if expr, ok := timeParts["timeExpr2"]; ok {
				query = strings.Replace(query, expr, "", -1)
			}
		}
	}

	if ts1, ok := timeParts["ts1"]; ok {
		if strings.Contains(query, "("+ts1+")") {
			m := matching.GetNamedMatches(reTimeClauseAlt, query, nil)
			if len(m) > 0 {
				if modifier, ok := m["modifier"]; ok {
					if expression, ok := m["expression"]; ok {
						query = strings.Replace(query, expression, fmt.Sprintf("BETWEEN %s(%s) AND %s(%s)",
							modifier, tkTimestamp1, modifier, tkTimestamp2), -1)
					}
				}
			}
		}
	}

	return query
}

func parseQueryExtents(query string, timeParts map[string]string) (timeseries.Extent, bool, error) {

	var e timeseries.Extent

	isRelativeTime := true

	op, ok := timeParts["operator"]
	if !ok {
		return e, false, fmt.Errorf("failed to parse query: %s", "could not find operator")
	}

	if t, ok := timeParts["ts1"]; ok {
		i, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return e, false, fmt.Errorf("failed to parse query: %s", "could not find start time")
		}
		e.Start = time.Unix(i, 0)
	}

	if strings.ToLower(op) == "between" {
		isRelativeTime = false
		if t, ok := timeParts["ts2"]; ok {
			i, err := strconv.ParseInt(t, 10, 64)
			if err != nil {
				return e, false, fmt.Errorf("failed to parse query: %s", "could not determine end time")
			}
			e.End = time.Unix(i, 0)
		} else {
			return e, false, fmt.Errorf("failed to parse query: %s", "could not find end time")
		}
	} else {
		e.End = time.Now()
	}

	return e, isRelativeTime, nil
}

mp := []string{"step", "timeField"}
found := matching.GetNamedMatches(reTimeFieldAndStep, trq.Statement, mp)
if len(found) == 2 {
trq.TimestampFieldName = found["timeField"]
trq.Step, _ = tt.ParseDuration(found["step"] + "s")
} else {
found = matching.GetNamedMatches(reTimeFuncAndStep, trq.Statement, mp)
if len(found) == 2 {
trq.TimestampFieldName = found["timeField"]
trq.Step, _ = tt.ParseDuration(timeFuncMap[found["step"]])
} else {
return nil, errors.ErrNotTimeRangeQuery
}
}

var err error
trq.Statement, trq.Extent, _, err = getQueryParts(trq.Statement, trq.TimestampFieldName)
if err != nil {
return nil, err
} */
