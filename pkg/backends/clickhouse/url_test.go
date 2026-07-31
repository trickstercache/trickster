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

package clickhouse

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestSetExtent(t *testing.T) {
	start := time.Now().Add(time.Duration(-6) * time.Hour)
	end := time.Now()
	expected := "query=select+%28intdiv%28touint32%28myTimeField%29%2C+" +
		"60%29+%2A+60%29+%2A+where+myTimeField+BETWEEN+toDateTime%28" +
		fmt.Sprintf("%d", start.Unix()) + "%29+AND+toDateTime%28" +
		fmt.Sprintf("%d", end.Unix()) + "%29+end"

	client := &Client{}

	tu := &url.URL{}
	e := &timeseries.Extent{Start: start, End: end}

	r, _ := http.NewRequest(http.MethodGet, tu.String(), nil)
	trq := &timeseries.TimeRangeQuery{
		TemplateURL: tu,
		Statement:   `select (intdiv(touint32(myTimeField), 60) * 60) * where myTimeField BETWEEN toDateTime(<$TS1$>) AND toDateTime(<$TS2$>) end`,
	}
	tu.RawQuery = url.Values{"query": []string{trq.Statement}}.Encode()

	client.SetExtent(r, trq, e)
	if expected != r.URL.RawQuery {
		t.Errorf("\nexpected [%s]\ngot      [%s]", expected, r.URL.RawQuery)
	}

	client.SetExtent(r, trq, nil)
	if expected != r.URL.RawQuery {
		t.Errorf("\nexpected [%s]\ngot      [%s]", expected, r.URL.RawQuery)
	}

	client.SetExtent(nil, trq, e)
	client.SetExtent(r, nil, e)
}

func TestSetExtentWithBody(t *testing.T) {
	start := time.Unix(1577836800, 0).UTC()
	end := time.Unix(1577836860, 0).UTC()
	e := &timeseries.Extent{Start: start, End: end}
	trq := &timeseries.TimeRangeQuery{
		Statement: `SELECT * WHERE <$RANGE$> FORMAT <$FORMAT$>`,
		TimestampDefinition: timeseries.FieldDefinition{
			Name:     "ts",
			DataType: timeseries.DateTimeUnixSecs,
		},
	}
	r, err := http.NewRequest(http.MethodPost, "http://example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	(&Client{}).SetExtent(r, trq, e)
	body, err := request.GetBody(r)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "ts BETWEEN 1577836800 AND 1577836860") {
		t.Errorf("unexpected body: %s", got)
	}
	if !strings.Contains(got, "TSVWithNamesAndTypes") {
		t.Errorf("expected format token replacement in %s", got)
	}
	if r.URL.RawQuery != "" {
		t.Errorf("expected empty query for body request, got %q", r.URL.RawQuery)
	}
}

func TestFormatTimestampValues(t *testing.T) {
	start := time.Unix(1577836800, 500*int64(time.Millisecond)).UTC()
	end := time.Unix(1577836860, 0).UTC()
	e := &timeseries.Extent{Start: start, End: end}

	tests := []struct {
		name      string
		dataType  timeseries.FieldDataType
		wantStart string
		wantEnd   string
	}{
		{
			name:      "milli",
			dataType:  timeseries.DateTimeUnixMilli,
			wantStart: "1577836800500",
			wantEnd:   "1577836860000",
		},
		{
			name:      "nano",
			dataType:  timeseries.DateTimeUnixNano,
			wantStart: "1577836800500000000",
			wantEnd:   "1577836860000000000",
		},
		{
			name:      "sql",
			dataType:  timeseries.DateTimeSQL,
			wantStart: "'2020-01-01 00:00:00'",
			wantEnd:   "'2020-01-01 00:01:00'",
		},
		{
			name:      "default secs",
			dataType:  timeseries.DateTimeUnixSecs,
			wantStart: "1577836800",
			wantEnd:   "1577836860",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotStart, gotEnd := formatTimestampValues(tc.dataType, e)
			if gotStart != tc.wantStart || gotEnd != tc.wantEnd {
				t.Errorf("got (%q, %q) want (%q, %q)",
					gotStart, gotEnd, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

func TestInterpolateTimeQuerySecondaryType(t *testing.T) {
	start := time.Unix(1577836800, 0).UTC()
	end := time.Unix(1577836860, 0).UTC()
	e := &timeseries.Extent{Start: start, End: end}
	out := interpolateTimeQuery(
		`WHERE <$RANGE$> AND secondary BETWEEN <$TS1$> AND <$TS2$>`,
		timeseries.FieldDefinition{
			Name:          "ts",
			DataType:      timeseries.DateTimeUnixMilli,
			ProviderData1: byte(timeseries.DateTimeUnixSecs),
		},
		e,
	)
	if !strings.Contains(out, "ts BETWEEN 1577836800000 AND 1577836860000") {
		t.Errorf("unexpected primary range: %s", out)
	}
	if !strings.Contains(out, "secondary BETWEEN 1577836800 AND 1577836860") {
		t.Errorf("unexpected secondary range: %s", out)
	}
}
