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

package stream

import (
	"math"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

func TestParseValue(t *testing.T) {
	tests := []struct {
		raw  string
		dt   timeseries.FieldDataType
		want any
	}{
		{"abc", timeseries.String, "abc"},
		{"", timeseries.String, ""},
		{"null", timeseries.String, "null"},
		{"2024-01-01", timeseries.DateSQL, "2024-01-01"},
		{"x", timeseries.Null, nil},
		{"", timeseries.Int64, nil},
		{"-42", timeseries.Int64, int64(-42)},
		{"1727222400", timeseries.DateTimeUnixSecs, int64(1727222400)},
		{"-300", timeseries.Int16, int64(-300)},
		{"-100", timeseries.Byte, int64(-100)},
		{"18446744073709551615", timeseries.Uint64, uint64(math.MaxUint64)},
		{"1.5", timeseries.Float64, 1.5},
		{"NaN", timeseries.Float64, math.NaN()},
		{"-Inf", timeseries.Float64, math.Inf(-1)},
		{"true", timeseries.Bool, true},
		{"0", timeseries.Bool, false},
		{"true", timeseries.Unknown, true},
		{"false", timeseries.Unknown, false},
		{"-12", timeseries.Unknown, int64(-12)},
		{"18446744073709551615", timeseries.Unknown, uint64(math.MaxUint64)},
		{"99999999999999999999", timeseries.Unknown, 1e20},
		{"-0.5", timeseries.Unknown, -0.5},
		{"1e3", timeseries.Unknown, 1000.0},
		{"1E+3", timeseries.Unknown, 1000.0},
		{"2.5e-1", timeseries.Unknown, 0.25},
		{"0", timeseries.Unknown, int64(0)},
		{"1e999", timeseries.Unknown, "1e999"},
		{"01", timeseries.Unknown, "01"},
		{"-", timeseries.Unknown, "-"},
		{"1.", timeseries.Unknown, "1."},
		{"1e", timeseries.Unknown, "1e"},
		{"1e+", timeseries.Unknown, "1e+"},
		{"1x", timeseries.Unknown, "1x"},
		{"NaN", timeseries.Unknown, "NaN"},
		{"abc", timeseries.Unknown, "abc"},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			got, err := ParseValue([]byte(test.raw), test.dt)
			require.NoError(t, err)
			if f, ok := test.want.(float64); ok && math.IsNaN(f) {
				require.True(t, math.IsNaN(got.(float64)))
				return
			}
			require.Equal(t, test.want, got)
		})
	}
}

func TestParseValueErrors(t *testing.T) {
	tests := []struct {
		raw string
		dt  timeseries.FieldDataType
	}{
		{"x", timeseries.Int64},
		{"1.5", timeseries.Int64},
		{"40000", timeseries.Int16},
		{"200", timeseries.Byte},
		{"-1", timeseries.Uint64},
		{"x", timeseries.Float64},
		{"yes", timeseries.Bool},
		{"1", timeseries.FieldDataType(250)},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			_, err := ParseValue([]byte(test.raw), test.dt)
			require.ErrorIs(t, err, ErrInvalidValue)
			require.ErrorIs(t, err, timeseries.ErrInvalidBody)
		})
	}
}

func TestParseJSONValue(t *testing.T) {
	tests := []struct {
		raw  string
		dt   timeseries.FieldDataType
		want any
	}{
		{"null", timeseries.String, nil},
		{"null", timeseries.Float64, nil},
		{`"null"`, timeseries.String, "null"},
		{`"abc"`, timeseries.String, "abc"},
		{`"a\"b\\c\n"`, timeseries.String, "a\"b\\c\n"},
		{`""`, timeseries.String, ""},
		{`""`, timeseries.Float64, nil},
		{`"1.5"`, timeseries.Float64, 1.5},
		{`1.5`, timeseries.Float64, 1.5},
		{`"+Inf"`, timeseries.Float64, math.Inf(1)},
		{`123`, timeseries.String, "123"},
		{`"123"`, timeseries.Unknown, "123"},
		{`123`, timeseries.Unknown, int64(123)},
		{`true`, timeseries.Unknown, true},
		{`"`, timeseries.Unknown, `"`},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			got, err := ParseJSONValue([]byte(test.raw), test.dt)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
	_, err := ParseJSONValue([]byte(`"\x"`), timeseries.String)
	require.ErrorIs(t, err, ErrInvalidValue)
	_, err = ParseJSONValue([]byte(`"x"`), timeseries.Int64)
	require.ErrorIs(t, err, ErrInvalidValue)
}

func BenchmarkParseJSONValue(b *testing.B) {
	raw := []byte("1727222400.123")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParseJSONValue(raw, timeseries.Float64); err != nil {
			b.Fatal(err)
		}
	}
}
