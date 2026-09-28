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

import (
	"math"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

func TestParseDecimal(t *testing.T) {
	tests := []struct {
		in   string
		unit timeseries.FieldDataType
		want Epoch
	}{
		{"1727222400", timeseries.DateTimeUnixSecs, 1727222400 * 1e9},
		{"1727222400.123", timeseries.DateTimeUnixSecs, 1727222400123000000},
		{"1727222400.123456789", timeseries.DateTimeUnixSecs, 1727222400123456789},
		{"1727222400.1230000000000000000000", timeseries.DateTimeUnixSecs, 1727222400123000000},
		{"1.727222400123e+09", timeseries.DateTimeUnixSecs, 1727222400123000000},
		{"1.727222400123E9", timeseries.DateTimeUnixSecs, 1727222400123000000},
		{"1727222400123e-3", timeseries.DateTimeUnixSecs, 1727222400123000000},
		{"1000e-12", timeseries.DateTimeUnixSecs, 1},
		{`"1727222400.5"`, timeseries.DateTimeUnixSecs, 1727222400500000000},
		{".5", timeseries.DateTimeUnixSecs, 500000000},
		{"5.", timeseries.DateTimeUnixSecs, 5000000000},
		{"+5", timeseries.DateTimeUnixSecs, 5000000000},
		{"-1.5", timeseries.DateTimeUnixSecs, -1500000000},
		{"0", timeseries.DateTimeUnixSecs, 0},
		{"0.000", timeseries.DateTimeUnixSecs, 0},
		{"0e99999", timeseries.DateTimeUnixSecs, 0},
		{"000000000000000000000001", timeseries.DateTimeUnixNano, 1},
		{"1727222400123", timeseries.DateTimeUnixMilli, 1727222400123000000},
		{"1727222400123.456", timeseries.DateTimeUnixMilli, 1727222400123456000},
		{"1727222400123456", timeseries.DateTimeUnixMicro, 1727222400123456000},
		{"1727222400123456789", timeseries.DateTimeUnixNano, 1727222400123456789},
		{strconv.FormatInt(math.MaxInt64, 10), timeseries.DateTimeUnixNano, math.MaxInt64},
	}
	for _, test := range tests {
		t.Run(test.in, func(t *testing.T) {
			got, err := ParseDecimal([]byte(test.in), test.unit)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestParseDecimalErrors(t *testing.T) {
	tests := []struct {
		in   string
		unit timeseries.FieldDataType
	}{
		{"1", timeseries.Float64},
		{"", timeseries.DateTimeUnixSecs},
		{`""`, timeseries.DateTimeUnixSecs},
		{"-", timeseries.DateTimeUnixSecs},
		{".", timeseries.DateTimeUnixSecs},
		{"e5", timeseries.DateTimeUnixSecs},
		{"1e", timeseries.DateTimeUnixSecs},
		{"1e+", timeseries.DateTimeUnixSecs},
		{"1e5x", timeseries.DateTimeUnixSecs},
		{"1x", timeseries.DateTimeUnixSecs},
		{"1.2.3", timeseries.DateTimeUnixSecs},
		{" 1", timeseries.DateTimeUnixSecs},
		{"0.0000000001", timeseries.DateTimeUnixSecs},
		{"1.5", timeseries.DateTimeUnixNano},
		{"1e-20", timeseries.DateTimeUnixNano},
		{"1e20", timeseries.DateTimeUnixNano},
		{"1e99999", timeseries.DateTimeUnixSecs},
		{"9223372036854775808", timeseries.DateTimeUnixNano},
		{"9223372036854775807", timeseries.DateTimeUnixSecs},
		{"99999999999999999999", timeseries.DateTimeUnixNano},
	}
	for _, test := range tests {
		t.Run(test.in, func(t *testing.T) {
			_, err := ParseDecimal([]byte(test.in), test.unit)
			require.ErrorIs(t, err, timeseries.ErrInvalidTimeFormat)
		})
	}
}

func BenchmarkParseDecimal(b *testing.B) {
	in := []byte("1727222400.123")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParseDecimal(in, timeseries.DateTimeUnixSecs); err != nil {
			b.Fatal(err)
		}
	}
}
