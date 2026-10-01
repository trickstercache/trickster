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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

func TestAppendFloat(t *testing.T) {
	// as ClickHouse 26.7 writes each
	for v, want := range map[float64]string{
		0: "0", math.Copysign(0, -1): "-0", 1: "1", 0.1: "0.1", 1e15: "1000000000000000",
		1.5e20: "150000000000000000000", 1e21: "1e21", 1.5e21: "1.5e21", 9.99e20: "999000000000000000000",
		123456789012345680000: "123456789012345680000", 0.0001: "0.0001", 1e-6: "0.000001",
		1.5e-6: "0.0000015", 9.99e-7: "9.99e-7", 1e-7: "1e-7", 1.234e-5: "0.00001234",
		3.141592653589793: "3.141592653589793", 1e100: "1e100", 1.7976931348623157e308: "1.7976931348623157e308",
		5e-324: "5e-324", -1.5: "-1.5", 2.5e-10: "2.5e-10", 1.23456e-8: "1.23456e-8", 1e-300: "1e-300",
		math.NaN(): "nan", math.Inf(1): "inf", math.Inf(-1): "-inf", 14.318181818181818: "14.318181818181818",
	} {
		require.Equal(t, want, string(appendFloat(nil, v)), "%g", v)
	}
	// every float reads back as itself
	rng := weaktest.NewRand(11, 11)
	for range 100000 {
		v := math.Float64frombits(rng.Uint64())
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		got, err := strconv.ParseFloat(string(appendFloat(nil, v)), 64)
		require.NoError(t, err)
		require.Equal(t, v, got)
	}
}

func TestOutFieldTimes(t *testing.T) {
	at := func(s string) epoch.Epoch {
		tm, err := time.Parse("2006-01-02 15:04:05.999999999", s)
		require.NoError(t, err)
		return epoch.Epoch(tm.UnixNano())
	}
	ny, _ := LoadZone("America/New_York")
	opts := &FormatOptions{Zone: ny}
	field := func(typ string) outField {
		return newOutField(&timeseries.FieldDefinition{SDataType: typ}, opts)
	}
	e, neg := at("2026-11-01 05:30:00.123456"), at("1960-01-01 00:00:00.5")
	// as ClickHouse 26.7 writes each with session_timezone=America/New_York
	for _, c := range []struct {
		typ    string
		e      epoch.Epoch
		format byte
		want   string
	}{
		{"DateTime('UTC')", e, DateTimeSimple, "2026-11-01 05:30:00"},
		{"DateTime", e, DateTimeSimple, "2026-11-01 01:30:00"},
		{"Nullable(DateTime64(6, 'UTC'))", e, DateTimeSimple, "2026-11-01 05:30:00.123456"},
		{"DateTime64(0, 'UTC')", e, DateTimeSimple, "2026-11-01 05:30:00"},
		{"DateTime64(1, 'UTC')", neg, DateTimeSimple, "1960-01-01 00:00:00.5"},
		{"DateTime('Asia/Tokyo')", 0, DateTimeSimple, "1970-01-01 09:00:00"},
		{"DateTime('UTC')", e, DateTimeISO, "2026-11-01T05:30:00Z"},
		{"DateTime64(6, 'UTC')", e, DateTimeISO, "2026-11-01T05:30:00.123456Z"},
		{"DateTime64(1, 'UTC')", neg, DateTimeISO, "1960-01-01T00:00:00.5Z"},
		{"DateTime('Asia/Tokyo')", 0, DateTimeISO, "1970-01-01T00:00:00Z"},
		{"DateTime", e, DateTimeUnix, "1793511000"},
		{"DateTime64(6, 'UTC')", e, DateTimeUnix, "1793511000.123456"},
		{"DateTime64(1, 'UTC')", neg, DateTimeUnix, "-315619199.5"},
		{"DateTime64(3)", epoch.Epoch(-500 * time.Millisecond), DateTimeUnix, "-0.500"},
		{"DateTime64(3)", epoch.Epoch(-1500 * time.Millisecond), DateTimeUnix, "-1.500"},
	} {
		f := field(c.typ)
		require.Equal(t, classDateTime, f.class)
		require.Equal(t, c.want, string(f.appendTime(nil, c.e, c.format)), "%s %d", c.typ, c.format)
	}
	// a stored DateTime's UTC text is written as it is when nothing changes it
	utc := newOutField(&timeseries.FieldDefinition{SDataType: "DateTime64(3)"}, &FormatOptions{})
	require.Equal(t, "2026-11-01 05:30:00.123", string(utc.appendStoredTime(nil, []byte("2026-11-01 05:30:00.123"), DateTimeSimple)))
	require.Equal(t, "x", string(utc.appendStoredTime(nil, []byte("x"), DateTimeISO)))
	local := field("DateTime64(3)")
	require.Equal(t, "2026-11-01 01:30:00.123", string(local.appendStoredTime(nil, []byte("2026-11-01 05:30:00.123"), DateTimeSimple)))
	require.Equal(t, "2026-11-01T05:30:00.123Z", string(utc.appendStoredTime(nil, []byte("2026-11-01 05:30:00.123"), DateTimeISO)))
}

func TestOutFieldClasses(t *testing.T) {
	for typ, want := range map[string]outField{
		"Int32": {class: classNumber}, "LowCardinality(Nullable(UInt64))": {class: classNumber, wide: true, nullable: true},
		"Int256": {class: classNumber, wide: true}, "Float32": {class: classFloat}, "Decimal(18, 3)": {class: classDecimal},
		"Bool": {class: classBool}, "Date32": {class: classDate}, "Array(DateTime)": {class: classCompound},
		"Map(String, UInt8)": {class: classCompound}, "Tuple(a UInt8)": {class: classCompound},
		"FixedString(4)": {class: classText, fixed: 4}, "Enum8('a' = 1)": {class: classText}, "String": {class: classText},
		"DateTime64(3)": {class: classDateTime, precision: 3},
	} {
		got := newOutField(&timeseries.FieldDefinition{SDataType: typ}, &FormatOptions{})
		got.fd = nil
		require.Equal(t, want, got, typ)
	}
}

func TestFormatOptions(t *testing.T) {
	o := NewFormatOptions(url.Values{SettingDateTimeOutput: {"ISO"}, SettingQuoteInt64: {"1"},
		SettingQuoteDecimals: {"true"}, SettingQuoteDenormals: {"0"}}, nil)
	require.Equal(t, FormatOptions{DateTimeFormat: DateTimeISO, QuoteInt64: true, QuoteDecimals: true}, o)
	require.Equal(t, DateTimeUnix, NewFormatOptions(url.Values{SettingDateTimeOutput: {"unix_timestamp"}}, nil).DateTimeFormat)
	require.Equal(t, "UTC", o.ZoneName())
	require.Equal(t, "UTC", (*FormatOptions)(nil).ZoneName())
	ny, ok := LoadZone("America/New_York")
	require.True(t, ok)
	again, _ := LoadZone("America/New_York")
	require.Same(t, ny, again)
	require.Equal(t, "America/New_York", (&FormatOptions{Zone: ny}).ZoneName())
	_, ok = LoadZone("Nowhere/Special")
	require.False(t, ok)
	for _, name := range []string{"", "UTC", "Etc/UTC"} {
		loc, ok := LoadZone(name)
		require.True(t, ok)
		if name != "Etc/UTC" {
			require.Nil(t, loc)
		}
	}
	require.Equal(t, &FormatOptions{}, formatOptions(nil))
	require.Equal(t, &o, formatOptions(&timeseries.RequestOptions{ProviderRequest: o}))
}

func TestIsJSONNumber(t *testing.T) {
	for _, s := range []string{"0", "-0", "7", "-10", "1.5", "1e21", "1e-7", "2.5E+10", "18446744073709551615"} {
		require.True(t, isJSONNumber(s), s)
	}
	for _, s := range []string{"", "-", "01", "1.", ".5", "1e", "1e+", "nan", "inf", "+1", "1x", "0x10", "1.5.5"} {
		require.False(t, isJSONNumber(s), s)
	}
}
