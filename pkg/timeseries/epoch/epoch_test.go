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
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		input Epoch
		exp1  string
		typ   timeseries.FieldDataType
		exp3  error
	}{
		{
			input: Epoch(1577836800) * BillionNS,
			exp1:  "1577836800",
			typ:   timeseries.DateTimeUnixSecs,
		},
		{
			input: Epoch(1577836800000) * MillionNS,
			exp1:  "1577836800000",
			typ:   timeseries.DateTimeUnixMilli,
		},
		{
			input: Epoch(1577836800000) * MillionNS,
			exp1:  "1577836800000000000",
			typ:   timeseries.DateTimeUnixNano,
		},
		{
			input: Epoch(1577836800000) * MillionNS,
			exp1:  "'2020-01-01'",
			typ:   timeseries.DateSQL,
		},
		{
			input: Epoch(1577836800000) * MillionNS,
			exp1:  "'2020-01-01 00:00:00'",
			typ:   timeseries.DateTimeSQL,
		},
		{
			input: Epoch(1577836800000) * MillionNS,
			exp1:  "'00:00:00'",
			typ:   timeseries.TimeSQL,
		},
		{
			input: Epoch(0) * MillionNS,
			exp1:  "0",
			typ:   0,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			out := test.input.Format(test.typ, true)
			if out != test.exp1 {
				t.Errorf("got %s expected %s", out, test.exp1)
			}
		})
	}
}

func TestAppendFormatMatchesFormat(t *testing.T) {
	types := []timeseries.FieldDataType{
		0, timeseries.DateTimeUnixSecs, timeseries.DateTimeUnixMilli, timeseries.DateTimeUnixNano,
		timeseries.DateTimeSQL, timeseries.DateSQL, timeseries.TimeSQL, timeseries.DateTimeRFC3339,
		timeseries.DateTimeRFC3339Nano, timeseries.String,
	}
	for _, e := range []Epoch{0, 1577836800123456789, -86400 * BillionNS, 1, 9223372036 * BillionNS} {
		for _, typ := range types {
			for _, quote := range []bool{false, true} {
				want := e.Format(typ, quote)
				if got := string(e.AppendFormat([]byte("x"), typ, quote)); got != "x"+want {
					t.Errorf("%d as %v: got %s want x%s", e, typ, got, want)
				}
				tm := time.Unix(0, int64(e))
				if got := string(AppendTime(nil, tm, typ, quote)); got != FormatTime(tm, typ, quote) {
					t.Errorf("%d as %v: AppendTime %s", e, typ, got)
				}
			}
		}
	}
	// RFC 3339 is written to the second, even when nanoseconds are asked for
	if got := Epoch(1577836800123456789).Format(timeseries.DateTimeRFC3339Nano, true); got != "2020-01-01T00:00:00Z" {
		t.Errorf("got %s", got)
	}
}
