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
	"github.com/tricksterproxy/trickster/pkg/util/regexp/matching"
)

// This file handles tokenization of time parameters within ClickHouse queries
// for cache key hashing and delta proxy caching.

// Tokens for String Interpolation
const (
	tkTimestamp1 = "<$TIMESTAMP1$>"
	tkTimestamp2 = "<$TIMESTAMP2$>"
)

var reTimeFieldAndStep, reTimeClauseAlt *regexp.Regexp

func init() {
	reTimeFieldAndStep = regexp.MustCompile(`(?i)select\s+\(\s*intdiv\s*\(\s*touint32\s*\(\s*(?P<timeField>[a-zA-Z0-9\._-]+)\s*\)\s*,\s*(?P<step>[0-9]+)\s*\)\s*\*\s*[0-9]+\s*\)`)
	reTimeClauseAlt = regexp.MustCompile(`(?i)\s+(?P<expression>(?P<operator>>=|>|=|between)\s+(?P<modifier>toDate(Time)?)\((?P<ts1>[0-9]+)\)(?P<timeExpr2>\s+and\s+toDate(Time)?\((?P<ts2>[0-9]+)\))?)`)
}

func interpolateTimeQuery(template, timeField string, extent *timeseries.Extent) string {
	return strings.Replace(strings.Replace(template, tkTimestamp1, strconv.Itoa(int(extent.Start.Unix())), -1), tkTimestamp2, strconv.Itoa(int(extent.End.Unix())), -1)
}

var compiledRe = make(map[string]*regexp.Regexp)

const timeClauseRe = `(?i)(?P<conjunction>where|and)\s+#TIME_FIELD#\s+(?P<timeExpr1>(?P<operator>>=|>|=|between)\s+(?P<modifier>toDate(Time)?)\((?P<ts1>[0-9]+)\))(?P<timeExpr2>\s+and\s+toDate(Time)?\((?P<ts2>[0-9]+)\))?`

func getQueryParts(query string, timeField string) (string, timeseries.Extent, bool, error) {

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
			query = strings.Replace(query, expr, fmt.Sprintf("BETWEEN %s(%s) AND %s(%s)", modifier, tkTimestamp1, modifier, tkTimestamp2), -1)
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
						query = strings.Replace(query, expression, fmt.Sprintf("BETWEEN %s(%s) AND %s(%s)", modifier, tkTimestamp1, modifier, tkTimestamp2), -1)
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
