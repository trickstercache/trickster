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
package pgwire

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	isoDateLen     = len("2006-01-02")
	isoDateTimeLen = len("2006-01-02 15:04:05")
	settingISO     = "ISO"
	secondsPerHour = 3600
	secondsPerMin  = 60
)

var (
	errTimeAxis = errors.New("time axis value cannot be decoded")
	// utcZoneNames are the session TimeZone values under which a zone-less
	// timestamp is a UTC instant.
	utcZoneNames = map[string]struct{}{"utc": {}, "etc/utc": {}, "gmt": {}, "etc/gmt": {}, "z": {}, "uct": {}, "etc/uct": {}}
)

func isUTCZone(zone string) bool {
	_, utc := utcZoneNames[strings.ToLower(zone)]
	return utc
}

type timeAxisDecoder struct {
	kind TimeAxisKind
	unit timeseries.FieldDataType
}

func newTimeAxisDecoder(kind TimeAxisKind, unit timeseries.FieldDataType, semantics TimeSemantics,
	settings func(string) (string, bool),
) (*timeAxisDecoder, error) {
	// checks that a column of this kind can be decoded under
	// the session's settings, and fails closed when it cannot.
	switch kind {
	case TimeAxisTimestampTZ, TimeAxisTimestamp, TimeAxisDate:
		// every non-ISO DateStyle renders zone abbreviations or local formats
		if style, ok := settings("datestyle"); !ok || !strings.HasPrefix(strings.ToUpper(style), settingISO) {
			return nil, errTimeAxis
		}
		if kind != TimeAxisTimestampTZ && !semantics.NaiveTimestampsAreUTC {
			// a zone-less value is compared in the session zone at the origin
			if zone, ok := settings(varTimeZone); !ok || !isUTCZone(zone) {
				return nil, errTimeAxis
			}
		}
	case TimeAxisEpochInteger, TimeAxisEpochFloat, TimeAxisEpochNumeric:
		if !isEpochUnit(unit) {
			return nil, errTimeAxis
		}
		if kind == TimeAxisEpochFloat && !semantics.LosslessFloatText {
			// negative extra_float_digits rounds an epoch to text like 2e+09, which can still land
			// on the grid. The origin never announces the setting, so an unknown value fails closed.
			digits, ok := settings(varExtraFloatDigits)
			if n, err := strconv.Atoi(digits); !ok || err != nil || n < 0 {
				return nil, errTimeAxis
			}
		}
	default:
		return nil, errTimeAxis
	}
	return &timeAxisDecoder{kind: kind, unit: unit}, nil
}

func isEpochUnit(unit timeseries.FieldDataType) bool {
	switch unit {
	case timeseries.DateTimeUnixSecs, timeseries.DateTimeUnixMilli,
		timeseries.DateTimeUnixMicro, timeseries.DateTimeUnixNano:
		return true
	}
	return false
}

func (d *timeAxisDecoder) decode(text []byte) (time.Time, error) {
	switch d.kind {
	case TimeAxisTimestampTZ:
		return parseISOTimestamp(text, true)
	case TimeAxisTimestamp:
		return parseISOTimestamp(text, false)
	case TimeAxisDate:
		if len(text) != isoDateLen {
			return time.Time{}, errTimeAxis
		}
		if value, ok := parseISODate(text, 0, 0, 0); ok {
			return value, nil
		}
		return time.Time{}, errTimeAxis
	case TimeAxisEpochInteger:
		value, err := strconv.ParseInt(string(text), 10, 64)
		if err != nil {
			return time.Time{}, errTimeAxis
		}
		return d.fromEpoch(value)
	}
	value, err := strconv.ParseFloat(string(text), 64)
	if err != nil || value != math.Trunc(value) || math.Abs(value) > 1<<53 {
		return time.Time{}, errTimeAxis
	}
	return d.fromEpoch(int64(value))
}

func (d *timeAxisDecoder) fromEpoch(value int64) (time.Time, error) {
	if d.unit == timeseries.DateTimeUnixSecs && !sqlanalyzer.SafeUnixSeconds(value) {
		return time.Time{}, errTimeAxis
	}
	return sqlanalyzer.UnixTime(value, d.unit), nil
}

func parseISOTimestamp(text []byte, zoned bool) (time.Time, error) {
	// reads YYYY-MM-DD HH:MM:SS[.f] and, when zoned, +HH[:MM[:SS]], from bytes without allocating.
	// Other years, BC dates and infinities fail closed.
	if len(text) < isoDateTimeLen || text[10] != ' ' || text[13] != ':' || text[16] != ':' {
		return time.Time{}, errTimeAxis
	}
	hour, okHour := twoDigits(text[11:13])
	minute, okMinute := twoDigits(text[14:16])
	second, okSecond := twoDigits(text[17:19])
	if !okHour || !okMinute || !okSecond || hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, errTimeAxis
	}
	value, ok := parseISODate(text[:isoDateLen], hour, minute, second)
	if !ok {
		return time.Time{}, errTimeAxis
	}
	rest := text[isoDateTimeLen:]
	if len(rest) > 0 && rest[0] == '.' {
		end := 1
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		digits := end - 1
		if digits == 0 || digits > 9 {
			return time.Time{}, errTimeAxis
		}
		fraction := 0
		for _, d := range rest[1:end] {
			fraction = fraction*10 + int(d-'0')
		}
		for range 9 - digits {
			fraction *= 10
		}
		value, rest = value.Add(time.Duration(fraction)), rest[end:]
	}
	if !zoned {
		if len(rest) != 0 {
			return time.Time{}, errTimeAxis
		}
		return value, nil
	}
	offset, err := parseZoneOffset(rest)
	if err != nil {
		return time.Time{}, err
	}
	return value.Add(-offset), nil
}

func parseISODate(text []byte, hour, minute, second int) (time.Time, bool) {
	// YYYY-MM-DD at the given time of day, in UTC; a day the month doesn't have is refused
	if len(text) != isoDateLen || text[4] != '-' || text[7] != '-' {
		return time.Time{}, false
	}
	century, okCentury := twoDigits(text[0:2])
	years, okYears := twoDigits(text[2:4])
	month, okMonth := twoDigits(text[5:7])
	day, okDay := twoDigits(text[8:10])
	if !okCentury || !okYears || !okMonth || !okDay || month < 1 || month > 12 || day < 1 {
		return time.Time{}, false
	}
	year := century*100 + years
	value := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC)
	return value, value.Day() == day
}

func twoDigits(text []byte) (int, bool) {
	if len(text) != 2 || text[0] < '0' || text[0] > '9' || text[1] < '0' || text[1] > '9' {
		return 0, false
	}
	return int(text[0]-'0')*10 + int(text[1]-'0'), true
}

func parseZoneOffset(text []byte) (time.Duration, error) {
	// +HH, +HH:MM or +HH:MM:SS, each part two digits no larger than 59
	if len(text) < 3 || text[0] != '+' && text[0] != '-' {
		return 0, errTimeAxis
	}
	seconds, rest := 0, text[1:]
	for i, scale := range [...]int{secondsPerHour, secondsPerMin, 1} {
		if len(rest) < 2 {
			return 0, errTimeAxis
		}
		n, ok := twoDigits(rest[:2])
		if !ok || n > 59 {
			return 0, errTimeAxis
		}
		seconds, rest = seconds+n*scale, rest[2:]
		if len(rest) == 0 {
			break
		}
		if rest[0] != ':' || i == 2 {
			return 0, errTimeAxis
		}
		rest = rest[1:]
	}
	if text[0] == '-' {
		seconds = -seconds
	}
	return time.Duration(seconds) * time.Second, nil
}
