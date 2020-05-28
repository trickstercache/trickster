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
	ttc "github.com/tricksterproxy/trickster/pkg/proxy/timeconv"
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

var chOperators = map[byte]bool{
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

var reTimeFieldAndStep = regexp.MustCompile(`.*intDiv\(toU?Int32\((?P<timeField>[a-zA-Z0-9._-]+)\),(?P<step>[0-9]+)\)`)

var srm = func(str, old string) string {
	return strings.Replace(str, old, "", -1)
}

var sup = strings.ToUpper

func interpolateTimeQuery(template string, extent *timeseries.Extent, step time.Duration) string {
	endTime := int(extent.End.Unix()) + int(step.Seconds()) // Add step to normalized end time
	return strings.Replace(strings.Replace(template, tkTimestamp1,
		strconv.Itoa(int(extent.Start.Unix())), -1), tkTimestamp2, strconv.Itoa(endTime), -1)
}

func parseRawQuery(query string, trq *timeseries.TimeRangeQuery) error {
	var duration string
	var err error
	parts := findParts(query)
	size := len(parts)
	if size < 4 {
		return fmt.Errorf("unrecognized query Format")
	}
	if sup(parts[size-2]+" "+parts[size-1]) != "FORMAT JSON" {
		return fmt.Errorf("non JSON formats not supported")
	}

	var tsColumn, tsAlias string
	var startTime, endTime, whereStart int
	var whereClause []string
	for i := 0; i < size; i++ {
		p := parts[i]
		if sup(p) == "SELECT" && tsColumn == "" {
			parts[i] = "SELECT"
			i++
			tsColumn = parts[i]
			if strings.HasSuffix(tsColumn, ",") {
				tsColumn = tsColumn[:len(tsColumn)-1]
			} else if strings.ToUpper(parts[i+1]) == "AS" {
				i += 2
				tsAlias = strings.Split(parts[i], ",")[0]
			}
			// First look for a Grafana/division type time series query
			m := matching.GetNamedMatches(reTimeFieldAndStep, tsColumn, nil)
			if tf, ok := m["timeField"]; ok {
				tsColumn = tf
				strStep, ok := m["step"]
				if !ok {
					return fmt.Errorf("invalid step from division operation")
				}
				duration = strStep + "s"
				// Otherwise check for the use of built-in ClickHouse time grouping functions
			} else {
				for k, v := range timeFuncMap {
					fi := strings.Index(tsColumn, k+"(")
					if fi > -1 {
						cp := strings.Index(tsColumn[fi+1:], ")")
						if cp == -1 {
							return fmt.Errorf("invalid time function syntax")
						}
						tsColumn = tsColumn[fi+len(k)+1 : fi+cp+1]
						duration = v
						break
					}
				}
				if duration == "" {
					return fmt.Errorf("unable to validate that first parameter is time grouping")
				}
			}
			continue
		}
		if tsColumn != "" && (sup(parts[i]) == "PREWHERE" || sup(parts[i]) == "WHERE") {
			startTime, endTime, whereClause, tsColumn, err = findRange(parts[i+1:], tsColumn, tsAlias)
			if err != nil {
				return err
			}
			if startTime > 0 {
				whereStart = i
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

	trq.Step, err = ttc.ParseDuration(duration)
	if err != nil {
		return fmt.Errorf("invalid duration parsed")
	}
	trq.Statement = strings.Join(parts[:whereStart+1], " ") + " " + strings.Join(whereClause, " ")
	trq.Extent.Start = time.Unix(int64(startTime), 0)
	trq.TimestampFieldName = tsColumn

	now := int(time.Now().Unix())
	if endTime == 0 {
		endTime = now
	}

	// Pad out endTime if we are in the "now" bucket so the last extent is not truncated
	step := int(trq.Step.Seconds())
	norm := now / step * step
	if endTime > norm {
		endTime = norm + step
		sbf := time.Duration(now-norm) * time.Second
		if sbf > trq.BackfillTolerance {
			trq.BackfillTolerance = sbf
		}
	}
	trq.Extent.End = time.Unix(int64(endTime), 0)
	return nil
}

func parseTime(s string) (int, error) {
	if strings.HasPrefix(s, "now()") {
		now := int(time.Now().Unix())
		if len(s) > 7 && s[5] == '-' {
			sub := 1
			for _, ms := range strings.Split(s[6:], "*") {
				m, err := strconv.Atoi(ms)
				if err != nil {
					return 0, err
				}
				sub *= m
			}
			return now - sub, nil
		}
		return now, nil
	}
	ts := srm(srm(srm(s, "toDateTime("), "toDate("), ")")
	t, err := strconv.Atoi(ts)
	if err == nil {
		return t, nil
	}
	td, err := fromDateString(srm(ts, "'"))
	if err == nil {
		return int(td.Unix()), nil
	}
	return 0, err
}

func findRange(parts []string, column string, alias string) (int, int, []string, string, error) {
	var err error
	var st, et int
	var actColumn string
	size := len(parts)
	var wc = make([]string, 0, size)
	for i := 0; i < size; i++ {
		p := parts[i]
		if strings.ToUpper(p) == "BETWEEN" {
			f := parts[i-1]
			if f == column || f == alias {
				actColumn = f
				i++
				st, err = parseTime(parts[i])
				if err != nil {
					return st, et, nil, "", err
				}
				i++
				if sup(parts[i]) != "AND" {
					return st, et, nil, "", fmt.Errorf("unrecognized between clause")
				}
				i++
				et, err = parseTime(parts[i])
				if err != nil {
					return st, et, nil, "", err
				}
				wc = append(wc, "("+actColumn+" >= "+tkTimestamp1+" AND "+actColumn+" < "+tkTimestamp2+") ")
				wc = append(wc, parts[i+1:]...)
				return st, et, wc, actColumn, nil
			}
		}
		pSize := len(p)
		tl := strings.Index(p, column)
		if tl == -1 {
			tl = strings.Index(p, alias)
			if tl == -1 {
				wc = append(wc, p)
				continue
			} else {
				tl += len(alias)
				actColumn = alias
			}
		} else {
			tl += len(column)
			actColumn = column
		}

		if tl < pSize && p[tl] == '>' {
			if tl < pSize+1 && p[tl+1] == '=' {
				tl++
			}
			st, err = parseTime(p[tl+1:])
			if err != nil {
				return st, et, nil, "", err
			}
			wc = append(wc, actColumn+" >= "+tkTimestamp1)
		} else if tl < pSize && p[tl] == '<' {
			if tl < pSize+1 && p[tl+1] == '=' {
				tl++
			}
			et, err = parseTime(p[tl+1:])
			if err != nil {
				return st, et, nil, "", err
			}
			wc = append(wc, actColumn+" < "+tkTimestamp2)
		} else {
			wc = append(wc, p)
		}
	}
	return st, et, wc, actColumn, nil
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
				if b == bClose || chOperators[b] {
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
		trimming = b == bOpen || chOperators[b]
		buf = append(buf, b)
	}
	return append(fields, string(buf[fieldStart:]))
}
