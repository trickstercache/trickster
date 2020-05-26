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
	tt "github.com/tricksterproxy/trickster/pkg/proxy/timeconv"
	"github.com/tricksterproxy/trickster/pkg/util/regexp/matching"
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

const bQuote = byte('\'')
const bBS = byte('\\')
const bSpace = byte(' ')
const bOpen = byte('(')
const bClose = byte(')')

const divOperation = "intDiv(toInt32("

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
	"toStartOfMinute":         "1m",
	"toStartOfFiveMinute":     "5m",
	"toStartOfFifteenMinutes": "15m",
	"toStartOfHour":           "1h",
	"toDate":                  "1d",
}

var reTimeFieldAndStep, reTimeFuncAndStep, reTimeClauseAlt *regexp.Regexp

var srp = func(str, old, new string) string {
	return strings.Replace(str, old, new, -1)
}
var srm = func(str, old string) string {
	return strings.Replace(str, old, "", -1)
}
var sai = strconv.Atoi
var sup = strings.ToUpper

func init() {
	reTimeFieldAndStep = regexp.MustCompile(`.*intDiv\(toUInt32\((?P<timeField>[a-zA-Z0-9._-]+)\),(?P<step>[0-9]+)\)*.+\)(?P<mult>.*)`)
	reTimeClauseAlt = regexp.MustCompile(`(?i)\s+(?P<expression>(?P<operator>>=|>|=|between)\s+` +
		`(?P<modifier>toDate(Time)?)\((?P<ts1>[0-9]+)\)(?P<timeExpr2>\s+and\s+toDate(Time)?\((?P<ts2>[0-9]+)\))?)`)
	reTimeFuncAndStep = regexp.MustCompile(`(?i)select\s+touint32\s*\(toStartOf(?P<step>[^\s]+)\s+\(?P<timeField>*\)`)
}

func interpolateTimeQuery(template string, extent *timeseries.Extent, step time.Duration) string {
	// If we end on the very beginning of a timeslice, extend the slice to capture up to the next boundary
	ds := int(step.Seconds())
	endTime := int(extent.End.Unix())
	if endTime%ds == 0 {
		endTime += ds
	}
	return strings.Replace(strings.Replace(template, tkTimestamp1,
		strconv.Itoa(int(extent.Start.Unix())), -1), tkTimestamp2, strconv.Itoa(endTime), -1)
}

var compiledRe = make(map[string]*regexp.Regexp)

const timeClauseRe = `(?i)(?P<conjunction>where|and)\s+#TIME_FIELD#\s+(?P<timeExpr1>(?P<operator>>=|>|=|between)\s+` +
	`(?P<modifier>toDate(Time)?)\((?P<ts1>[0-9]+)\))(?P<timeExpr2>\s+and\s+toDate(Time)?\((?P<ts2>[0-9]+)\))?`

func parseRawQuery(query string, trq *timeseries.TimeRangeQuery) error {
	var duration string
	var err error
	//var timeField string
	parts := findParts(query)
	size := len(parts)
	if size < 4 {
		return fmt.Errorf("unrecognized query Format")
	}
	if sup(parts[size-2]+" "+parts[size-1]) != "FORMAT JSON" {
		return fmt.Errorf("non JSON formats not supported")
	}

	var tsColumn, tsAlias string
	var startTime, endTime, qs, qe int
	for i := 0; i < size; i++ {
		p := parts[i]
		if sup(p) == "SELECT" {
			parts[i] = "SELECT"
			i++
			tsColumn = parts[i]
			if strings.HasSuffix(tsColumn, ",") {
				tsColumn = tsColumn[:len(tsColumn)-1]
			} else if strings.ToUpper(parts[i+1]) == "AS" {
				i += 2
				tsAlias = strings.Split(parts[i], ",")[0]
			}
			m := matching.GetNamedMatches(reTimeFieldAndStep, tsColumn, nil)
			if tf, ok := m["timeField"]; ok {
				tsColumn = tf
				strStep, ok := m["step"]
				if !ok {
					return fmt.Errorf("invalid step from division operation")
				}
				duration = strStep + "s"
			}
			continue
		}
		if tsColumn != "" && (sup(parts[i]) == "PREWHERE" || sup(parts[i]) == "WHERE") {
			startTime, endTime, qe, tsColumn, err = findRange(parts[i+1:], tsColumn, tsAlias)
			if err != nil {
				return err
			}
			if startTime > 0 {
				qs = i
				qe += i + 2
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

	trq.Step, err = tt.ParseDuration(duration)
	if err != nil {
		return fmt.Errorf("invalid duration parsed")
	}

	tr := " (" + tsColumn + " >= " + tkTimestamp1 + " AND " + tsColumn + " < " + tkTimestamp2 + ") "
	trq.Statement = strings.Join(parts[:qs+1], " ") + tr + strings.Join(parts[qe:], " ")
	trq.Extent.Start = time.Unix(int64(startTime), 0)
	trq.Extent.End = time.Unix(int64(endTime), 0)
	return nil
}

func findRange(parts []string, column string, alias string) (int, int, int, string, error) {
	var err error
	var st, et int
	size := len(parts)
	for i := 1; i < size; i++ {
		p := parts[i]
		if strings.ToUpper(p) == "BETWEEN" {
			f := parts[i-1]
			if f == column || f == alias {
				i++
				ts := srm(srm(srm(parts[i], "toDateTime("), "toDate("), ")")
				st, err = strconv.Atoi(ts)
				if err != nil {
					return st, et, 0, "", err
				}
				i++
				if sup(parts[i]) != "AND" {
					return st, et, 0, "", fmt.Errorf("unrecognized between clause")
				}
				i++
				ts = srm(srm(srm(parts[i], "toDateTime("), "toDate("), ")")
				et, err = strconv.Atoi(ts)
				if err != nil {
					return st, et, 0, "", err
				}
				return st, et, i, f, nil
			}
		}
	}

	/*for i := 0; i < size; i++ {
		p := ss[i]
		if strings.Index(p, column) > -1 {

		}
	}*/
	return st, et, 0, "", nil
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
			if b == bQuote && !escaped {
				inQuote = false
			}
			escaped = !escaped && b == bBS
			buf = append(buf, b)
			continue
		}
		if b == bSpace {
			if trimming {
				continue
			}
			for i++; i < size; i++ {
				b = bytes[i]
				if b == bClose || operators[b] {
					break
				}
				if b != bSpace {
					fields = append(fields, string(buf[fieldStart:]))
					buf = append(buf, bSpace)
					fieldStart = len(buf)
					break
				}
			}
		}
		if b == bQuote {
			inQuote = true
		}
		trimming = b == bOpen || operators[b]
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
