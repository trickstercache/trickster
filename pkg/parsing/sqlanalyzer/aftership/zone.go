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

package aftership

import (
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"

	chast "github.com/AfterShip/clickhouse-sql-parser/parser"
)

// the zone a rendered DateTime64 bound's text is written in
const utcZone = "UTC"

// the ClickHouse date and time functions that read the session's time zone, by lowercase name and by
// the prefixes of their families
var (
	zoneFunctions = map[string]struct{}{
		"todate": {}, "todate32": {}, "todateornull": {}, "todateorzero": {}, "todate32ornull": {},
		"todate32orzero": {}, "toyear": {}, "toquarter": {}, "tomonth": {}, "todayofyear": {},
		"todayofmonth": {}, "todayofweek": {}, "tohour": {}, "tominute": {}, "tosecond": {}, "tomonday": {},
		"totime": {}, "toisoweek": {}, "toisoyear": {}, "toweek": {}, "toyearweek": {}, "timeslot": {},
		"timeslots": {}, "today": {}, "yesterday": {}, "date_trunc": {}, "datetrunc": {}, "datename": {},
		"age": {}, "datediff": {}, "date_diff": {}, "timestampdiff": {}, "timestamp_diff": {},
		"tostring": {}, "makedate": {}, "makedate32": {}, "makedatetime": {}, "makedatetime64": {},
	}
	zoneFunctionPrefixes = []string{
		"tostartof", "torelative", "toyyyy", "formatdatetime", "parsedatetime",
		"tolastdayof", "fromunixtimestamp",
	}
)

// zoneFunction reports whether a function reads the session's time zone
func zoneFunction(name string) bool {
	name = strings.ToLower(name)
	if _, ok := zoneFunctions[name]; ok {
		return true
	}
	for _, prefix := range zoneFunctionPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// readsSessionZone reports whether a statement reads the session's zone other than through its bucket,
// whose alignment the provider checks: a zone's function, or text read as a time
func readsSessionZone(query *chast.SelectQuery, bucket chast.Expr) bool {
	bucketKey := expressionKey(bucket)
	// text a conversion names the zone of is read in that zone; the walk reaches a call before its arguments
	named := map[*chast.StringLiteral]struct{}{}
	found := false
	chast.Walk(query, func(node chast.Expr) bool {
		switch value := node.(type) {
		case *chast.StringLiteral:
			if _, ok := named[value]; !ok {
				_, found = sqlanalyzer.ParseSQLTime(value.Literal)
			}
		case *chast.FunctionExpr:
			if text, ok := zonedConversion(value); ok {
				named[text] = struct{}{}
				return true
			}
			// the bucket's own function, in its SELECT, GROUP BY or ORDER BY, is judged by its alignment
			found = value.Name != nil && zoneFunction(value.Name.Name) && expressionKey(value) != bucketKey
		}
		return !found
	})
	return found
}

// zonedConversion returns the text a toDateTime or toDateTime64 call converts in the zone it names
func zonedConversion(f *chast.FunctionExpr) (*chast.StringLiteral, bool) {
	if f.Name == nil {
		return nil, false
	}
	args := functionArgs(f)
	switch strings.ToLower(f.Name.Name) {
	case toDateTimeFunction:
		if len(args) != 2 {
			return nil, false
		}
	case "todatetime64":
		if len(args) != 3 {
			return nil, false
		}
	default:
		return nil, false
	}
	text, ok := unwrapColumnExpr(args[0]).(*chast.StringLiteral)
	_, zoned := unwrapColumnExpr(args[len(args)-1]).(*chast.StringLiteral)
	return text, ok && zoned
}

// zonedBound reports whether a bound is text or a date, which ClickHouse reads in the session's zone
// and the analysis reads as UTC
func zonedBound(style boundStyle) bool {
	return style == boundSQLDateTime || style == boundToDate
}
