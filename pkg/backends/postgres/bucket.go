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

package postgres

import (
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/cockroach"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/types"
)

const (
	fnTimeBucket        = "time_bucket"
	fnTimeBucketGapfill = "time_bucket_gapfill"
	fnDateBin           = "date_bin"
	fnDateTrunc         = "date_trunc"
	fnExtract           = "extract"

	// unnamedColumn is the result column name PostgreSQL gives an unaliased expression.
	unnamedColumn = "?column?"

	day               = 24 * time.Hour
	maxIntervalDigits = 12
	maxFractionDigits = 6
)

var timeBucketOrigin = time.Date(2000, 1, 3, 0, 0, 0, 0, time.UTC)

var intervalUnits = map[string]time.Duration{
	// only fixed-length units; months and longer vary, so widths written with them never match
	"us": time.Microsecond, "usec": time.Microsecond, "usecs": time.Microsecond,
	"microsecond": time.Microsecond, "microseconds": time.Microsecond,
	"ms": time.Millisecond, "msec": time.Millisecond, "msecs": time.Millisecond,
	"millisecond": time.Millisecond, "milliseconds": time.Millisecond,
	"s": time.Second, "sec": time.Second, "secs": time.Second, "second": time.Second, "seconds": time.Second,
	"m": time.Minute, "min": time.Minute, "mins": time.Minute, "minute": time.Minute, "minutes": time.Minute,
	"h": time.Hour, "hr": time.Hour, "hrs": time.Hour, "hour": time.Hour, "hours": time.Hour,
	"d": day, "day": day, "days": day,
	"w": 7 * day, "week": 7 * day, "weeks": 7 * day,
}

var zonedLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07",
	"2006-01-02 15:04Z07:00", "2006-01-02 15:04Z07",
}

var zonelessLayouts = []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04", "2006-01-02"}

func parseInterval(text string) (time.Duration, bool) {
	// reads a width such as '300.000s', '5 minutes' or '1h30m'. Signs, clock and ISO 8601
	// forms, bare numbers and sub-microsecond widths do not match, so they fail closed.
	var total time.Duration
	terms := 0
	for i := 0; ; terms++ {
		for i < len(text) && text[i] == ' ' {
			i++
		}
		if i == len(text) {
			return total, terms > 0 && total > 0
		}
		start := i
		for i < len(text) && isDigit(text[i]) {
			i++
		}
		whole := text[start:i]
		fraction := ""
		if i < len(text) && text[i] == '.' {
			i++
			start = i
			for i < len(text) && isDigit(text[i]) {
				i++
			}
			fraction = strings.TrimRight(text[start:i], "0")
		}
		if whole == "" || len(whole) > maxIntervalDigits || len(fraction) > maxFractionDigits {
			return 0, false
		}
		for i < len(text) && text[i] == ' ' {
			i++
		}
		start = i
		for i < len(text) && isLetter(text[i]) {
			i++
		}
		unit, ok := intervalUnits[strings.ToLower(text[start:i])]
		if !ok {
			return 0, false
		}
		term, ok := intervalTerm(whole, fraction, unit)
		if !ok || total+term < total {
			return 0, false
		}
		total += term
	}
}

func intervalTerm(whole, fraction string, unit time.Duration) (time.Duration, bool) {
	count := int64(0)
	for _, c := range []byte(whole) {
		count = count*10 + int64(c-'0')
	}
	if count > int64((1<<63-1)/unit) {
		return 0, false
	}
	term := time.Duration(count) * unit
	if fraction == "" {
		return term, true
	}
	numerator, scale := int64(0), int64(1)
	for _, c := range []byte(fraction) {
		numerator, scale = numerator*10+int64(c-'0'), scale*10
	}
	// six digits of an hour stay far below an int64; a fractional day or week is refused
	if unit > time.Hour {
		return 0, false
	}
	product := numerator * int64(unit)
	if product%scale != 0 || (product/scale)%int64(time.Microsecond) != 0 {
		return 0, false
	}
	return term + time.Duration(product/scale), true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func intervalArg(expr tree.Expr) (time.Duration, bool) {
	switch value := expr.(type) {
	case *tree.StrVal:
		return parseInterval(value.RawString())
	case *tree.CastExpr:
		literal, ok := value.Expr.(*tree.StrVal)
		if !ok || !castsTo(value, types.Interval) {
			return 0, false
		}
		return parseInterval(literal.RawString())
	}
	return 0, false
}

func castsTo(cast *tree.CastExpr, want *types.T) bool {
	// an INTERVAL with a field restriction or a precision is a different type, and changes the value
	castType, ok := tree.GetStaticallyKnownType(cast.Type)
	return ok && castType.Identical(want)
}

func parseInstant(text string, zoneless bool) (time.Time, bool) {
	// reads a timestamp literal. One without a zone is a UTC instant only when
	// the session zone is UTC, which zoneless says.
	for _, layout := range zonedLayouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	if !zoneless {
		return time.Time{}, false
	}
	for _, layout := range zonelessLayouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func phaseOf(origin time.Time, step time.Duration) time.Duration {
	phase := time.Duration(origin.UnixNano() % step.Nanoseconds())
	if phase < 0 {
		phase += step
	}
	return phase
}

type bucketMatchers struct {
	// utc says the session zone is UTC, so zone-less origins are UTC instants.
	utc bool
}

func (m bucketMatchers) timeBucket(name string, args []tree.Expr) (cockroach.BucketMatch, bool) {
	// matches time_bucket(width, column[, origin|offset]) and the two-argument
	// time_bucket_gapfill, whose start and finish come from the WHERE range.
	none := cockroach.BucketMatch{}
	switch {
	case name == fnTimeBucket && (len(args) == 2 || len(args) == 3):
	case name == fnTimeBucketGapfill && len(args) == 2:
	default:
		return none, false
	}
	step, ok := intervalArg(args[0])
	if !ok {
		return none, false
	}
	column, ok := cockroach.ColumnName(args[1])
	if !ok {
		return none, false
	}
	// TimescaleDB aligns fixed-width buckets to Monday 2000-01-03 unless told otherwise
	origin := timeBucketOrigin
	if len(args) == 3 {
		// an untyped third argument is the time zone overload, so only a typed one matches
		cast, ok := args[2].(*tree.CastExpr)
		if !ok {
			return none, false
		}
		literal, ok := cast.Expr.(*tree.StrVal)
		if !ok {
			return none, false
		}
		switch {
		case castsTo(cast, types.TimestampTZ):
			if origin, ok = parseInstant(literal.RawString(), m.utc); !ok {
				return none, false
			}
		case castsTo(cast, types.Interval):
			offset, ok := parseInterval(literal.RawString())
			if !ok {
				return none, false
			}
			origin = origin.Add(offset)
		default:
			return none, false
		}
	}
	return cockroach.BucketMatch{
		TimeColumn: column, Step: step, Phase: phaseOf(origin, step), OutputColumn: name,
	}, true
}

func (m bucketMatchers) dateBin(name string, args []tree.Expr) (cockroach.BucketMatch, bool) {
	// matches date_bin(width, column, origin); PostgreSQL requires the origin.
	none := cockroach.BucketMatch{}
	if name != fnDateBin || len(args) != 3 {
		return none, false
	}
	step, ok := intervalArg(args[0])
	if !ok {
		return none, false
	}
	column, ok := cockroach.ColumnName(args[1])
	if !ok {
		return none, false
	}
	literal, ok := args[2].(*tree.StrVal)
	if cast, isCast := args[2].(*tree.CastExpr); isCast {
		if !castsTo(cast, types.TimestampTZ) && (!m.utc || !castsTo(cast, types.Timestamp)) {
			return none, false
		}
		literal, ok = cast.Expr.(*tree.StrVal)
	}
	if !ok {
		return none, false
	}
	origin, ok := parseInstant(literal.RawString(), m.utc)
	if !ok {
		return none, false
	}
	return cockroach.BucketMatch{
		TimeColumn: column, Step: step, Phase: phaseOf(origin, step), OutputColumn: name,
	}, true
}

func dateTrunc(name string, args []tree.Expr) (cockroach.BucketMatch, bool) {
	// PostgreSQL truncates a timestamptz in the session zone, so this matcher
	// is registered only for sessions whose zone is UTC.
	match, ok := cockroach.DateTruncMatcher(name, args)
	match.OutputColumn = fnDateTrunc
	return match, ok
}

func epochFloor(expr tree.Expr) (cockroach.BucketMatch, bool) {
	match, ok := cockroach.EpochFloorMatcher(expr)
	if ok {
		match.OutputColumn = unnamedColumn
	}
	return match, ok
}
