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

package model

import (
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// how a response writes a DateTime, as ClickHouse's date_time_output_format names them
const (
	DateTimeSimple byte = iota
	DateTimeISO
	DateTimeUnix
)

// the ClickHouse settings that change how a response is written
const (
	SettingSessionTimezone = "session_timezone"
	SettingDateTimeOutput  = "date_time_output_format"
	SettingQuoteInt64      = "output_format_json_quote_64bit_integers"
	SettingQuoteDecimals   = "output_format_json_quote_decimals"
	SettingQuoteDenormals  = "output_format_json_quote_denormals"
	dateTimeISO            = "iso"
	dateTimeUnix           = "unix_timestamp"
	// TimezoneHeader names the time zone a ClickHouse response's DateTimes are written in
	TimezoneHeader = "X-ClickHouse-Timezone"
	utcName        = "UTC"
)

// FormatOptions are a request's settings for how its response is written.
type FormatOptions struct {
	// Revision is the Native result framing revision the HTTP client asked for.
	Revision uint64
	// Zone is the time zone a DateTime without its own is written in; nil is UTC.
	Zone *time.Location
	// DateTimeFormat is how a DateTime is written: DateTimeSimple, DateTimeISO or DateTimeUnix.
	DateTimeFormat byte
	// QuoteInt64, QuoteDecimals and QuoteDenormals quote JSON's 64-bit and wider integers, its
	// decimals, and its NaNs and infinities, which are otherwise bare, bare and null.
	QuoteInt64, QuoteDecimals, QuoteDenormals bool
}

// NewFormatOptions returns the options a request's settings give, writing DateTimes in zone.
func NewFormatOptions(settings url.Values, zone *time.Location) FormatOptions {
	o := FormatOptions{Zone: zone}
	switch strings.ToLower(settings.Get(SettingDateTimeOutput)) {
	case dateTimeISO:
		o.DateTimeFormat = DateTimeISO
	case dateTimeUnix:
		o.DateTimeFormat = DateTimeUnix
	}
	o.QuoteInt64 = settingOn(settings.Get(SettingQuoteInt64))
	o.QuoteDecimals = settingOn(settings.Get(SettingQuoteDecimals))
	o.QuoteDenormals = settingOn(settings.Get(SettingQuoteDenormals))
	return o
}

// settingOn reports whether a boolean setting's text is true, as ClickHouse reads it
func settingOn(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true":
		return true
	}
	return false
}

// ZoneName returns the name ClickHouse gives the zone, UTC for nil.
func (o *FormatOptions) ZoneName() string {
	if o == nil || o.Zone == nil {
		return utcName
	}
	return o.Zone.String()
}

// formatOptions returns a request's FormatOptions, or the defaults when it has none
func formatOptions(rlo *timeseries.RequestOptions) *FormatOptions {
	if rlo != nil {
		if fopts, ok := rlo.ProviderRequest.(FormatOptions); ok {
			return &fopts
		}
	}
	return &FormatOptions{}
}

var zones sync.Map

// LoadZone returns the named time zone, nil for UTC, and whether the name is a zone. Zones are loaded
// once and kept.
func LoadZone(name string) (*time.Location, bool) {
	if name == "" || name == utcName {
		return nil, true
	}
	if loc, ok := zones.Load(name); ok {
		return loc.(*time.Location), true
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, false
	}
	if loc == time.UTC {
		loc = nil
	}
	zones.Store(name, loc)
	return loc, true
}

// valueClass is how a ClickHouse type's values are written in text formats
type valueClass uint8

const (
	// a quoted string: String, FixedString, UUID, Enum, IPv4, ...
	classText valueClass = iota
	// a bare integer
	classNumber
	classFloat
	classDecimal
	classBool
	classDateTime
	classDate
	// an Array, Map or Tuple, held as ClickHouse's literal
	classCompound
)

// outField is how a field's values are written: its type's class, and what the class needs
type outField struct {
	fd       *timeseries.FieldDefinition
	class    valueClass
	nullable bool
	// a FixedString's width, which its text is padded to with NULs
	fixed int
	// a 64-bit or wider integer
	wide bool
	// a DateTime64's fraction digits
	precision int
	// the zone a DateTime is written in, the column's own or the request's; nil is UTC
	zone *epoch.Zone
}

func newOutField(fd *timeseries.FieldDefinition, opts *FormatOptions) outField {
	f := outField{fd: fd, class: classText}
	typ := strings.TrimSpace(fd.SDataType)
	for {
		if inner, ok := cutWrapper(typ, prefixLowCardinality); ok {
			typ = inner
			continue
		}
		if inner, ok := cutWrapper(typ, prefixNullable); ok {
			typ, f.nullable = inner, true
			continue
		}
		break
	}
	name, args, _ := strings.Cut(typ, "(")
	args = strings.TrimSuffix(args, ")")
	zone := opts.Zone
	switch name {
	case TypeInt8, TypeInt16, TypeInt32, TypeUInt8, TypeUInt16, TypeUInt32:
		f.class = classNumber
	case TypeInt64, TypeUInt64, "Int128", "Int256", "UInt128", "UInt256":
		f.class, f.wide = classNumber, true
	case TypeFloat32, TypeFloat64:
		f.class = classFloat
	case "Decimal", "Decimal32", "Decimal64", "Decimal128", "Decimal256":
		f.class = classDecimal
	case TypeBool:
		f.class = classBool
	case TypeDateTime, prefixDateTime64:
		f.class = classDateTime
		list := splitTypeList(args)
		if name == prefixDateTime64 {
			f.precision, _ = strconv.Atoi(list[0])
			list = list[1:]
		}
		if len(list) > 0 && list[0] != "" {
			if loc, ok := LoadZone(strings.Trim(list[0], "'")); ok {
				zone = loc
			}
		}
		if zone != nil {
			f.zone = epoch.NewZone(zone)
		}
	case TypeDate, typeDate32:
		f.class = classDate
	case "Array", "Map", "Tuple":
		f.class = classCompound
	case "FixedString":
		f.fixed, _ = strconv.Atoi(args)
	}
	return f
}

// appendTime appends a time as the field's DateTime, in the request's format
func (f *outField) appendTime(b []byte, e epoch.Epoch, format byte) []byte {
	switch format {
	case DateTimeISO:
		return append(epoch.AppendSQLTime(b, e, 'T', f.precision), 'Z')
	case DateTimeUnix:
		return appendUnixTime(b, e, f.precision)
	}
	if f.zone != nil {
		e = f.zone.Local(e)
	}
	return epoch.AppendSQLTime(b, e, ' ', f.precision)
}

// appendStoredTime appends a DateTime value held as its UTC text, which is written as it is when the
// request writes it the same way
func (f *outField) appendStoredTime(b []byte, text []byte, format byte) []byte {
	if format == DateTimeSimple && f.zone == nil {
		return append(b, text...)
	}
	e, ok := epoch.ParseSQLDateTime(text)
	if !ok {
		return append(b, text...)
	}
	return f.appendTime(b, e, format)
}

// appendUnixTime appends a time as its seconds since the epoch, with digits fraction digits, as
// ClickHouse writes a DateTime64's ticks divided by its scale
func appendUnixTime(b []byte, e epoch.Epoch, digits int) []byte {
	unit := pow10[maxPrecision-digits]
	ticks := int64(e) / unit
	if int64(e)%unit < 0 {
		ticks--
	}
	if digits == 0 {
		return strconv.AppendInt(b, ticks, 10)
	}
	scale := pow10[digits]
	whole, frac := ticks/scale, ticks%scale
	if frac < 0 {
		frac = -frac
		if whole == 0 {
			b = append(b, '-')
		}
	}
	b = strconv.AppendInt(b, whole, 10)
	b = append(b, '.')
	n := len(b)
	b = strconv.AppendInt(b, scale+frac, 10)
	// the leading 1 of scale+frac keeps the fraction's leading zeros
	return append(b[:n], b[n+1:]...)
}

// appendFloat appends v as ClickHouse writes a float: its shortest digits, positional from 1e-7 to
// 1e21 exclusive and otherwise with an exponent, and nan, inf and -inf
func appendFloat(b []byte, v float64) []byte {
	switch {
	case math.IsNaN(v):
		return append(b, "nan"...)
	case math.IsInf(v, 1):
		return append(b, "inf"...)
	case math.IsInf(v, -1):
		return append(b, "-inf"...)
	case v == 0:
		if math.Signbit(v) {
			return append(b, "-0"...)
		}
		return append(b, '0')
	}
	var buf [32]byte
	// d.ddde±XX
	s := strconv.AppendFloat(buf[:0], v, 'e', -1, 64)
	if s[0] == '-' {
		b = append(b, '-')
		s = s[1:]
	}
	mark := 0
	for s[mark] != 'e' {
		mark++
	}
	exp, _ := strconv.Atoi(string(s[mark+1:]))
	digits := s[:mark]
	if len(digits) > 1 {
		// the digits without their point
		var whole [20]byte
		digits = append(whole[:0], digits[0])
		digits = append(digits, s[2:mark]...)
	}
	switch {
	case exp < -6 || exp > 20:
		b = append(b, digits[0])
		if len(digits) > 1 {
			b = append(append(b, '.'), digits[1:]...)
		}
		b = append(b, 'e')
		return strconv.AppendInt(b, int64(exp), 10)
	case exp < 0:
		b = append(b, '0', '.')
		for range -exp - 1 {
			b = append(b, '0')
		}
		return append(b, digits...)
	case exp+1 >= len(digits):
		b = append(b, digits...)
		for range exp + 1 - len(digits) {
			b = append(b, '0')
		}
		return b
	}
	b = append(b, digits[:exp+1]...)
	return append(append(b, '.'), digits[exp+1:]...)
}
