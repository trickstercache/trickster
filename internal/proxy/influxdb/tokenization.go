/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package influxdb

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Comcast/trickster/internal/proxy/timeconv"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/regexp/matching"
)

// This file handles tokenization of time parameters within InfluxDB queries
// for cache key hashing and delta proxy caching.

// Tokens for String Interpolation
const (
	tkTime = "<$TIME_TOKEN$>"
)

var reType, reTime1, reTime2, reStep, reTime1Parse, reTime2Parse *regexp.Regexp

func init() {

	// Regexp for extracting the step from an InfluxDB Timeseries Query. searches for something like: group by time(1d)
	reStep = regexp.MustCompile(`(?i)\s+group\s+by\s+.*time\((?P<step>[0-9]+(ns|µ|u|ms|s|m|h|d|w|y))\).*;??`)

	// Regexp for extracting the time elements from an InfluxDB Timeseries Query with equality operators: >=, >, =
	// If it's a relative time range (e.g.,  where time >= now() - 24h  ), this expression is all that is required
	reTime1 = regexp.MustCompile(`(?i)(?P<preOp1>where|and)\s+(?P<timeExpr1>time\s+(?P<relationalOp1>>=|>|=)\s+(?P<value1>((?P<ts1>[0-9]+)(?P<tsUnit1>ns|µ|u|ms|s|m|h|d|w|y)|(?P<now1>now\(\))\s+(?P<operand1>[+-])\s+(?P<offset1>[0-9]+[mhsdwy]))))(\s+(?P<postOp1>and|or|group|order|limit)|$)`)

	// Regexp for extracting the time elements from an InfluxDB Timeseries Query with equality operators: <=, <
	// If it's an absolute time range (e.g.,  where time >= 150000ms and time <= 150001ms ), this expression catches the second clause
	reTime2 = regexp.MustCompile(`(?i)(?P<preOp2>where|and)\s+(?P<timeExpr2>time\s+(?P<relationalOp2><=|<)\s+(?P<value2>((?P<ts2>[0-9]+)(?P<tsUnit2>ns|µ|u|ms|s|m|h|d|w|y)|(?P<now2>now\(\))\s+(?P<operand2>[+-])\s+(?P<offset2>[0-9]+[mhsdwy]))))(\s+(?P<postOp2>and|or|group|order|limit)|$)`)
}

func interpolateTimeQuery(template string, extent *timeseries.Extent) string {
	return strings.Replace(template, tkTime, fmt.Sprintf("time >= %dms AND time <= %dms", extent.Start.Unix()*1000, extent.End.Unix()*1000), -1)
}

func getQueryParts(query string) (string, timeseries.Extent) {
	m := matching.GetNamedMatches(reTime1, query, nil)
	if _, ok := m["now1"]; !ok {
		m2 := matching.GetNamedMatches(reTime2, query, nil)
		for k, v := range m2 {
			m[k] = v
		}
	}
	return tokenizeQuery(query, m), parseQueryExtents(query, m)
}

// tokenizeQuery will take an InfluxDB query and replace all time conditionals with a single $TIME$
func tokenizeQuery(query string, timeParts map[string]string) string {

	replacement := tkTime
	// First check the existence of timeExpr1, and if exists, do the replacement
	// this catches anything with "time >" or "time >="
	if expr, ok := timeParts["timeExpr1"]; ok {
		query = strings.Replace(query, expr, replacement, -1)
		// We already inserted a $TIME$, for any more occurrences, replace with ""
		replacement = ""
	}

	// Then check the existence of timeExpr2, and if exists, do the replacement
	// including any preceding "and" or the following "and" if preceded by "where"
	// this catches anything with "time <" or "time <="
	if expr, ok := timeParts["timeExpr2"]; ok {
		if preOp, ok := timeParts["preOp2"]; ok {
			if strings.ToLower(preOp) == "where" {
				if postOp, ok := timeParts["postOp2"]; ok {
					if strings.ToLower(postOp) == "and" {
						expr += " " + postOp
					}
				}
			} else {
				expr = " " + preOp + " " + expr
			}
		}
		query = strings.Replace(query, expr, replacement, -1)
	}
	return query
}

func parseQueryExtents(query string, timeParts map[string]string) timeseries.Extent {

	var e timeseries.Extent

	t1 := timeFromParts("1", timeParts)
	e.Start = t1
	if _, ok := timeParts["now1"]; ok {
		e.End = time.Now()
		return e
	}

	t2 := timeFromParts("2", timeParts)
	e.End = t2
	return e
}

func timeFromParts(clauseNum string, timeParts map[string]string) time.Time {

	ts := int64(0)

	if _, ok := timeParts["now"+clauseNum]; ok {
		if offset, ok := timeParts["offset"+clauseNum]; ok {
			s, err := timeconv.ParseDuration(offset)
			if err == nil {
				if operand, ok := timeParts["operand"+clauseNum]; ok {
					if operand == "+" {
						ts = time.Now().Unix() + int64(s.Seconds())
					} else {
						ts = time.Now().Unix() - int64(s.Seconds())
					}
				}
			}
		}
	} else if v, ok := timeParts["value"+clauseNum]; ok {
		s, err := time.ParseDuration(v)
		if err == nil {
			ts = int64(s.Seconds())
		}
	}
	return time.Unix(ts, 0)
}
