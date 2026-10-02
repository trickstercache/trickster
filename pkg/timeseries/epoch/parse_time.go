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

package epoch

import "time"

const (
	// the lengths of YYYY-MM-DD, and of a canonical time without its fraction or zone
	dateLen          = len("2006-01-02")
	canonicalTimeLen = len("2006-01-02T15:04:05")
	// the most fractional digits time.Parse reads; it drops any after them
	maxFractionDigits = 9
	secondsPerDay     = 86400
	// days from 0000-03-01 to 1970-01-01, and in a 400-year era, for civil date math
	epochDayOffset = 719468
	daysPerEra     = 146097
)

// ParseCanonicalTime parses raw when it holds a UTC time written as YYYY-MM-DDTHH:MM:SS, with an
// optional fraction of a second, followed by "Z" when zoned or by nothing when not. It returns what
// time.Parse returns for that text with time.RFC3339Nano, or for the same layout without its zone,
// and false for any other text.
func ParseCanonicalTime(raw []byte, zoned bool) (Epoch, bool) {
	e, rest, ok := parseCivil(raw, 'T')
	if !ok {
		return 0, false
	}
	if zoned {
		if len(rest) != 1 || rest[0] != 'Z' {
			return 0, false
		}
	} else if len(rest) != 0 {
		return 0, false
	}
	return e, true
}

// ParseSQLDateTime parses a UTC time written as YYYY-MM-DD HH:MM:SS[.fraction], as time.Parse would
// with "2006-01-02 15:04:05.999999999", and returns false for any other text.
func ParseSQLDateTime(raw []byte) (Epoch, bool) {
	e, rest, ok := parseCivil(raw, ' ')
	if !ok || len(rest) != 0 {
		return 0, false
	}
	return e, true
}

// ParseSQLDate parses raw when it holds a date written as YYYY-MM-DD, returning what time.Parse
// returns for it in UTC with the layout "2006-01-02", and false for any other text.
func ParseSQLDate(raw []byte) (Epoch, bool) {
	if len(raw) != dateLen {
		return 0, false
	}
	days, ok := parseDate(raw)
	return Epoch(days * secondsPerDay * int64(time.Second)), ok
}

// ParseRFC3339 parses raw as time.Parse does with layout, which must be time.RFC3339 or
// time.RFC3339Nano; a canonical UTC time is parsed without allocating.
func ParseRFC3339(raw []byte, layout string) (Epoch, error) {
	if e, ok := ParseCanonicalTime(raw, true); ok {
		return e, nil
	}
	t, err := time.Parse(layout, string(raw))
	if err != nil {
		return 0, err
	}
	return Epoch(t.UnixNano()), nil
}

// parseCivil parses the date and time that start raw, with sep between them, and any fraction of a
// second after them, returning the text that follows
func parseCivil(raw []byte, sep byte) (Epoch, []byte, bool) {
	if len(raw) < canonicalTimeLen || raw[dateLen] != sep || raw[13] != ':' || raw[16] != ':' {
		return 0, nil, false
	}
	days, ok := parseDate(raw)
	hour, ok2 := twoDigits(raw[11:13])
	minute, ok3 := twoDigits(raw[14:16])
	sec, ok4 := twoDigits(raw[17:19])
	if !ok || !ok2 || !ok3 || !ok4 || hour > 23 || minute > 59 || sec > 59 {
		return 0, nil, false
	}
	rest := raw[canonicalTimeLen:]
	var nsec int64
	if len(rest) >= 2 && rest[0] == '.' && isDigit(rest[1]) {
		n := 1
		for n < len(rest) && isDigit(rest[n]) {
			n++
		}
		scale := int64(time.Second / 10)
		for _, c := range rest[1:min(n, maxFractionDigits+1)] {
			nsec += int64(c-'0') * scale
			scale /= 10
		}
		rest = rest[n:]
	}
	secs := days*secondsPerDay + hour*3600 + minute*60 + sec
	// as time.Time.UnixNano computes it, wrapping the same way outside its range
	return Epoch(secs*int64(time.Second) + nsec), rest, true
}

// parseDate returns the days from 1970-01-01 to the YYYY-MM-DD that starts raw
func parseDate(raw []byte) (int64, bool) {
	if raw[4] != '-' || raw[7] != '-' {
		return 0, false
	}
	year, ok := twoDigits(raw[0:2])
	lo, ok2 := twoDigits(raw[2:4])
	month, ok3 := twoDigits(raw[5:7])
	day, ok4 := twoDigits(raw[8:10])
	year = year*100 + lo
	if !ok || !ok2 || !ok3 || !ok4 || month < 1 || month > 12 || day < 1 || day > daysIn(month, year) {
		return 0, false
	}
	return daysFromCivil(year, month, day), true
}

func twoDigits(b []byte) (int64, bool) {
	if !isDigit(b[0]) || !isDigit(b[1]) {
		return 0, false
	}
	return int64(b[0]-'0')*10 + int64(b[1]-'0'), true
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func daysIn(month, year int64) int64 {
	switch month {
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// daysFromCivil returns the days from 1970-01-01 to a proleptic Gregorian date, by Howard Hinnant's
// algorithm, which counts years from March so a leap day ends its year
func daysFromCivil(year, month, day int64) int64 {
	if month <= 2 {
		year--
	}
	era := year / 400
	if year < 0 {
		era = (year - 399) / 400
	}
	yearOfEra := year - era*400
	monthFromMarch := (month + 9) % 12
	dayOfYear := (153*monthFromMarch+2)/5 + day - 1
	dayOfEra := yearOfEra*365 + yearOfEra/4 - yearOfEra/100 + dayOfYear
	return era*daysPerEra + dayOfEra - epochDayOffset
}

// the digits AppendCanonicalTime writes
const decimalDigits = "0123456789"

// AppendCanonicalTime appends e in UTC as time.Time.AppendFormat does with time.RFC3339 (fraction
// false, zoned true), time.RFC3339Nano (both true), or time.RFC3339Nano without its zone. An Epoch's
// years, 1677 to 2262, always take four digits.
func AppendCanonicalTime(dst []byte, e Epoch, fraction, zoned bool) []byte {
	days, sod, nsec := splitEpoch(e)
	dst = appendDate(dst, days)
	dst = append(dst, 'T')
	dst = appendClock(dst, sod)
	if fraction && nsec != 0 {
		// the nine digits of the nanoseconds, less the zeros that trail them
		var digits [maxFractionDigits]byte
		for i := maxFractionDigits - 1; i >= 0; i-- {
			digits[i] = decimalDigits[nsec%10]
			nsec /= 10
		}
		n := maxFractionDigits
		for digits[n-1] == '0' {
			n--
		}
		dst = append(dst, '.')
		dst = append(dst, digits[:n]...)
	}
	if zoned {
		dst = append(dst, 'Z')
	}
	return dst
}

// AppendSQLTime appends e in UTC as YYYY-MM-DD, sep and HH:MM:SS, then digits of its fraction when
// digits is positive, as ClickHouse writes a DateTime64(digits).
func AppendSQLTime(dst []byte, e Epoch, sep byte, digits int) []byte {
	days, sod, nsec := splitEpoch(e)
	dst = appendDate(dst, days)
	dst = append(dst, sep)
	dst = appendClock(dst, sod)
	if digits <= 0 {
		return dst
	}
	digits = min(digits, maxFractionDigits)
	var frac [maxFractionDigits]byte
	for i := maxFractionDigits - 1; i >= 0; i-- {
		frac[i] = decimalDigits[nsec%10]
		nsec /= 10
	}
	dst = append(dst, '.')
	return append(dst, frac[:digits]...)
}

// splitEpoch returns the days from 1970-01-01 to e, and e's second of that day and nanosecond of
// that second
func splitEpoch(e Epoch) (days, sod, nsec int64) {
	secs, nsec := int64(e)/int64(time.Second), int64(e)%int64(time.Second)
	if nsec < 0 {
		secs, nsec = secs-1, nsec+int64(time.Second)
	}
	days, sod = secs/secondsPerDay, secs%secondsPerDay
	if sod < 0 {
		days, sod = days-1, sod+secondsPerDay
	}
	return days, sod, nsec
}

// appendDate appends the date days after 1970-01-01 as YYYY-MM-DD
func appendDate(dst []byte, days int64) []byte {
	year, month, day := civilFromDays(days)
	dst = appendDigits(dst, year/100)
	dst = appendDigits(dst, year%100)
	dst = append(dst, '-')
	dst = appendDigits(dst, month)
	dst = append(dst, '-')
	return appendDigits(dst, day)
}

// appendClock appends a second of the day as HH:MM:SS
func appendClock(dst []byte, sod int64) []byte {
	dst = appendDigits(dst, sod/3600)
	dst = append(dst, ':')
	dst = appendDigits(dst, sod/60%60)
	dst = append(dst, ':')
	return appendDigits(dst, sod%60)
}

// appendDigits appends v, which is below 100, as two digits
func appendDigits(dst []byte, v int64) []byte {
	return append(dst, decimalDigits[v/10], decimalDigits[v%10])
}

// civilFromDays returns the proleptic Gregorian date days after 1970-01-01, the inverse of daysFromCivil
func civilFromDays(days int64) (year, month, day int64) {
	// an Epoch's days are all after 0000-03-01, so the era's division needs no floor
	days += epochDayOffset
	era := days / daysPerEra
	dayOfEra := days - era*daysPerEra
	yearOfEra := (dayOfEra - dayOfEra/1460 + dayOfEra/36524 - dayOfEra/146096) / 365
	dayOfYear := dayOfEra - (365*yearOfEra + yearOfEra/4 - yearOfEra/100)
	monthFromMarch := (5*dayOfYear + 2) / 153
	day = dayOfYear - (153*monthFromMarch+2)/5 + 1
	month = monthFromMarch + 3
	if month > 12 {
		month -= 12
	}
	year = yearOfEra + era*400
	if month <= 2 {
		year++
	}
	return year, month, day
}
