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

// Package parsing implements graphite-web's render API grammars and the
// allowlist classifier; ambiguous input fails closed to the unaccelerated lane.
package parsing

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// DefaultFrom is graphite-web's default for an absent from parameter
const DefaultFrom = "-1d"

var (
	// ErrEmptyTimeRange mirrors graphite-web's "Invalid empty time range"
	ErrEmptyTimeRange = errors.New("invalid empty time range")
	errBadOffset      = errors.New("invalid time offset")
)

var months = [...]string{
	"jan", "feb", "mar", "apr", "may", "jun",
	"jul", "aug", "sep", "oct", "nov", "dec",
}

var weekdays = [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// ParseTimeRange resolves from/until into an Extent as graphite-web does:
// absent until is now, absent from is -1d, ordered, truncated to whole seconds.
func ParseTimeRange(from, until string, loc *time.Location,
	now time.Time,
) (timeseries.Extent, error) {
	var ext timeseries.Extent
	if loc == nil {
		loc = time.UTC
	}
	now = now.In(loc)
	u := now
	if until != "" {
		var err error
		if u, err = ParseATTime(until, loc, now); err != nil {
			return ext, fmt.Errorf("until: %w", err)
		}
	}
	if from == "" {
		from = DefaultFrom
	}
	f, err := ParseATTime(from, loc, now)
	if err != nil {
		return ext, fmt.Errorf("from: %w", err)
	}
	f, u = f.Truncate(time.Second), u.Truncate(time.Second)
	if u.Before(f) {
		f, u = u, f
	}
	if f.Equal(u) {
		return ext, ErrEmptyTimeRange
	}
	ext.Start, ext.End = f, u
	return ext, nil
}

// ParseATTime ports graphite-web's attime.parseATTime: epoch seconds, relative
// offsets, references (now/midnight/...), date forms, and reference+offset compounds.
func ParseATTime(s string, loc *time.Location, now time.Time) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	now = now.In(loc)
	s = normalizeATTime(s)
	if isDigits(s) {
		// an 8-digit value that looks like a date is YYYYMMDD, not an epoch
		if len(s) != 8 || atoi(s[:4]) <= 1900 || atoi(s[4:6]) >= 13 || atoi(s[6:]) >= 32 {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(n, 0).In(loc), nil
		}
	}
	ref, offset := s, ""
	if i := strings.IndexByte(s, '+'); i >= 0 {
		ref, offset = s[:i], s[i:]
	} else if i := strings.IndexByte(s, '-'); i >= 0 {
		ref, offset = s[:i], s[i:]
	}
	t, err := parseTimeReference(ref, loc, now)
	if err != nil {
		return time.Time{}, err
	}
	d, err := ParseTimeOffset(offset)
	if err != nil {
		return time.Time{}, err
	}
	return t.Add(d), nil
}

// Applies graphite-web's pre-parse cleanup: trim, lowercase, and drop
// underscores, commas and spaces
func normalizeATTime(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if !strings.ContainsAny(s, "_, ") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		switch s[i] {
		case '_', ',', ' ':
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Parses a short all-digit string; callers guarantee the input
func atoi(s string) int {
	n := 0
	for i := range len(s) {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// Returns the index just past the run of ASCII digits at the start of s
func leadingDigits(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}

func leadingAlphas(s string) int {
	i := 0
	for i < len(s) && ((s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z')) {
		i++
	}
	return i
}

// Ports graphite-web's attime.parseTimeReference
func parseTimeReference(ref string, loc *time.Location, now time.Time) (time.Time, error) {
	if ref == "" || ref == "now" {
		return now, nil
	}
	raw := ref
	hour, minute := 0, 0
	if i := strings.IndexByte(ref, ':'); i > 0 && i < 3 {
		if !isDigits(ref[:i]) {
			return time.Time{}, fmt.Errorf("unknown day reference: %s", raw)
		}
		hour = atoi(ref[:i])
		end := min(i+3, len(ref))
		if !isDigits(ref[i+1 : end]) {
			return time.Time{}, fmt.Errorf("unknown day reference: %s", raw)
		}
		minute = atoi(ref[i+1 : end])
		ref = ref[end:]
		if strings.HasPrefix(ref, "am") {
			ref = ref[2:]
		} else if strings.HasPrefix(ref, "pm") {
			hour = (hour + 12) % 24
			ref = ref[2:]
		}
	}
	if i := strings.Index(ref, "am"); i > 0 && i < 3 {
		if !isDigits(ref[:i]) {
			return time.Time{}, fmt.Errorf("unknown day reference: %s", raw)
		}
		hour = atoi(ref[:i])
		ref = ref[i+2:]
	}
	if i := strings.Index(ref, "pm"); i > 0 && i < 3 {
		if !isDigits(ref[:i]) {
			return time.Time{}, fmt.Errorf("unknown day reference: %s", raw)
		}
		hour = (atoi(ref[:i]) + 12) % 24
		ref = ref[i+2:]
	}
	switch {
	case strings.HasPrefix(ref, "noon"):
		hour, minute = 12, 0
		ref = ref[4:]
	case strings.HasPrefix(ref, "midnight"):
		hour, minute = 0, 0
		ref = ref[8:]
	case strings.HasPrefix(ref, "teatime"):
		hour, minute = 16, 0
		ref = ref[7:]
	}
	if hour > 23 || minute > 59 {
		return time.Time{}, fmt.Errorf("unknown day reference: %s", raw)
	}
	y, m, d := now.Date()
	refDate := time.Date(y, m, d, hour, minute, 0, 0, loc)
	switch {
	case ref == "", ref == "today":
	case ref == "yesterday":
		refDate = refDate.AddDate(0, 0, -1)
	case ref == "tomorrow":
		refDate = refDate.AddDate(0, 0, 1)
	case strings.Count(ref, "/") == 2: // MM/DD/YY[YY]
		parts := strings.Split(ref, "/")
		if !isDigits(parts[0]) || !isDigits(parts[1]) || !isDigits(parts[2]) ||
			len(parts[2]) > 4 {
			return time.Time{}, fmt.Errorf("unknown day reference: %s", raw)
		}
		mm, dd, yy := atoi(parts[0]), atoi(parts[1]), atoi(parts[2])
		if yy < 1900 {
			yy += 1900
		}
		if yy < 1970 {
			yy += 100
		}
		var err error
		if refDate, err = civilDate(yy, mm, dd, hour, minute, loc); err != nil {
			return time.Time{}, err
		}
	case len(ref) == 8 && isDigits(ref): // YYYYMMDD
		var err error
		if refDate, err = civilDate(atoi(ref[:4]), atoi(ref[4:6]), atoi(ref[6:8]),
			hour, minute, loc); err != nil {
			return time.Time{}, err
		}
	case len(ref) >= 3 && monthIndex(ref[:3]) > 0: // MonthName DayOfMonth
		var dd int
		switch {
		case len(ref) >= 2 && isDigits(ref[len(ref)-2:]):
			dd = atoi(ref[len(ref)-2:])
		case isDigits(ref[len(ref)-1:]):
			dd = atoi(ref[len(ref)-1:])
		default:
			return time.Time{}, errors.New("day of month required after month name")
		}
		var err error
		if refDate, err = civilDate(y, monthIndex(ref[:3]), dd, hour, minute, loc); err != nil {
			return time.Time{}, err
		}
	case len(ref) >= 3 && weekdayIndex(ref[:3]) >= 0: // DayOfWeek
		dayOffset := int(refDate.Weekday()) - weekdayIndex(ref[:3])
		if dayOffset < 0 {
			dayOffset += 7
		}
		refDate = refDate.AddDate(0, 0, -dayOffset)
	default:
		return time.Time{}, fmt.Errorf("unknown day reference: %s", raw)
	}
	return refDate, nil
}

// Builds a date, rejecting out-of-range fields the way Python's datetime
// constructor does rather than normalizing them like time.Date
func civilDate(y, m, d, hour, minute int, loc *time.Location) (time.Time, error) {
	if y < 1 || y > 9999 || m < 1 || m > 12 || d < 1 {
		return time.Time{}, fmt.Errorf("invalid date %04d-%02d-%02d", y, m, d)
	}
	t := time.Date(y, time.Month(m), d, hour, minute, 0, 0, loc)
	if t.Day() != d || t.Month() != time.Month(m) || t.Year() != y {
		return time.Time{}, fmt.Errorf("invalid date %04d-%02d-%02d", y, m, d)
	}
	return t, nil
}

func monthIndex(s string) int {
	for i, m := range months {
		if m == s {
			return i + 1
		}
	}
	return 0
}

func weekdayIndex(s string) int {
	for i, w := range weekdays {
		if w == s {
			return i
		}
	}
	return -1
}

// ParseTimeOffset ports attime.parseTimeOffset: units match by prefix (s, min,
// h, d, w, mon=30d, y=365d); a bare "m" is invalid, exactly as in graphite-web.
func ParseTimeOffset(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	sign := int64(1)
	switch {
	case s[0] >= '0' && s[0] <= '9':
	case s[0] == '+':
		s = s[1:]
	case s[0] == '-':
		sign = -1
		s = s[1:]
	default:
		return 0, fmt.Errorf("%w: %s", errBadOffset, s)
	}
	var total int64 // seconds
	for s != "" {
		i := leadingDigits(s)
		if i == 0 || i > 18 {
			return 0, fmt.Errorf("%w: %s", errBadOffset, s)
		}
		num, _ := strconv.ParseInt(s[:i], 10, 64)
		s = s[i:]
		j := leadingAlphas(s)
		mult, ok := unitSeconds(s[:j])
		if !ok {
			return 0, fmt.Errorf("%w: invalid offset unit '%s'", errBadOffset, s[:j])
		}
		s = s[j:]
		if num > math.MaxInt64/mult || total > math.MaxInt64-num*mult ||
			total+num*mult > math.MaxInt64/int64(time.Second) {
			return 0, fmt.Errorf("%w: out of range", errBadOffset)
		}
		total += num * mult
	}
	return time.Duration(sign*total) * time.Second, nil
}

// Ports attime.getUnitString's prefix matching
func unitSeconds(u string) (int64, bool) {
	switch {
	case strings.HasPrefix(u, "s"):
		return 1, true
	case strings.HasPrefix(u, "min"):
		return 60, true
	case strings.HasPrefix(u, "h"):
		return 3600, true
	case strings.HasPrefix(u, "d"):
		return 86400, true
	case strings.HasPrefix(u, "w"):
		return 7 * 86400, true
	case strings.HasPrefix(u, "mon"):
		return 30 * 86400, true
	case strings.HasPrefix(u, "y"):
		return 365 * 86400, true
	}
	return 0, false
}
