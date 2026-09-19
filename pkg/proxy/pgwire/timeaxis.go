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
	isoLayout      = "2006-01-02 15:04:05"
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

func newTimeAxisDecoder(kind TimeAxisKind, unit timeseries.FieldDataType, naiveUTC bool,
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
		if kind != TimeAxisTimestampTZ && !naiveUTC {
			// a zone-less value is compared in the session zone at the origin
			if zone, ok := settings(varTimeZone); !ok || !isUTCZone(zone) {
				return nil, errTimeAxis
			}
		}
	case TimeAxisEpochInteger, TimeAxisEpochFloat, TimeAxisEpochNumeric:
		if !isEpochUnit(unit) {
			return nil, errTimeAxis
		}
		if kind == TimeAxisEpochFloat {
			// negative extra_float_digits rounds an epoch to text like 2e+09
			if digits, ok := settings("extra_float_digits"); ok {
				if n, err := strconv.Atoi(digits); err != nil || n < 0 {
					return nil, errTimeAxis
				}
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
		return parseISOTimestamp(string(text), true)
	case TimeAxisTimestamp:
		return parseISOTimestamp(string(text), false)
	case TimeAxisDate:
		if len(text) != isoDateLen {
			return time.Time{}, errTimeAxis
		}
		return parseISOTimestamp(string(text)+" 00:00:00", false)
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

func parseISOTimestamp(text string, zoned bool) (time.Time, error) {
	// reads YYYY-MM-DD HH:MM:SS[.f] and, when zoned, +HH[:MM[:SS]].
	// Years outside four digits, BC dates and infinities do not match, so they fail closed.
	if len(text) < isoDateTimeLen || text[4] != '-' {
		return time.Time{}, errTimeAxis
	}
	rest := text[isoDateTimeLen:]
	var fraction time.Duration
	if strings.HasPrefix(rest, ".") {
		end := 1
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		digits := rest[1:end]
		if digits == "" || len(digits) > 9 {
			return time.Time{}, errTimeAxis
		}
		n, _ := strconv.Atoi(digits + strings.Repeat("0", 9-len(digits)))
		fraction, rest = time.Duration(n), rest[end:]
	}
	value, err := time.Parse(isoLayout, text[:isoDateTimeLen])
	if err != nil {
		return time.Time{}, errTimeAxis
	}
	value = value.Add(fraction)
	if !zoned {
		if rest != "" {
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

func parseZoneOffset(text string) (time.Duration, error) {
	if len(text) < 3 || text[0] != '+' && text[0] != '-' {
		return 0, errTimeAxis
	}
	parts := strings.Split(text[1:], ":")
	if len(parts) > 3 {
		return 0, errTimeAxis
	}
	seconds := 0
	for i, scale := range []int{secondsPerHour, secondsPerMin, 1}[:len(parts)] {
		n, err := strconv.Atoi(parts[i])
		if err != nil || len(parts[i]) != 2 || n < 0 || n > 59 {
			return 0, errTimeAxis
		}
		seconds += n * scale
	}
	if text[0] == '-' {
		seconds = -seconds
	}
	return time.Duration(seconds) * time.Second, nil
}
