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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestFromConverters(t *testing.T) {
	t.Parallel()
	if got := FromSecs(1577836800); got != Epoch(1577836800)*BillionNS {
		t.Errorf("FromSecs: got %d", got)
	}
	if got := FromMilliSecs(1577836800000); got != Epoch(1577836800000)*MillionNS {
		t.Errorf("FromMilliSecs: got %d", got)
	}
	if got := FromNanoSecs(1577836800000000000); got != Epoch(1577836800000000000) {
		t.Errorf("FromNanoSecs: got %d", got)
	}
}

func TestFormatRFC3339(t *testing.T) {
	t.Parallel()
	e := FromSecs(1577836800)
	got := e.Format(timeseries.DateTimeRFC3339, false)
	want := "2020-01-01T00:00:00Z"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	got = e.Format(timeseries.DateTimeRFC3339Nano, false)
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFormatTimeDirect(t *testing.T) {
	t.Parallel()
	ts := time.Unix(1577836800, 0).UTC()
	tests := []struct {
		name  string
		typ   timeseries.FieldDataType
		quote bool
		want  string
	}{
		{name: "unix secs", typ: timeseries.DateTimeUnixSecs, want: "1577836800"},
		{name: "unix milli", typ: timeseries.DateTimeUnixMilli, want: "1577836800000"},
		{name: "unix nano", typ: timeseries.DateTimeUnixNano, want: "1577836800000000000"},
		{name: "sql datetime quoted", typ: timeseries.DateTimeSQL, quote: true, want: "'2020-01-01 00:00:00'"},
		{name: "sql date", typ: timeseries.DateSQL, quote: true, want: "'2020-01-01'"},
		{name: "sql time", typ: timeseries.TimeSQL, quote: true, want: "'00:00:00'"},
		{name: "rfc3339", typ: timeseries.DateTimeRFC3339, want: "2020-01-01T00:00:00Z"},
		{name: "unknown", typ: timeseries.Unknown, want: "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatTime(ts, tc.typ, tc.quote)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}
