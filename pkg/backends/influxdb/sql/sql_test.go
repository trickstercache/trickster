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

package sql

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
)

func TestParseTimeRangeQuery_SQL(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		method    string
		path      string
		wantErr   bool
		wantStep  time.Duration
	}{
		{
			name:     "basic date_bin query with epoch",
			query:    "SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(temperature) AS temperature FROM weather WHERE time >= 1704067200 AND time < 1704153600 GROUP BY 1",
			method:   http.MethodGet,
			path:     "/api/v3/query_sql",
			wantStep: time.Hour,
		},
		{
			name:     "date_bin with 5 minute interval",
			query:    "SELECT date_bin(INTERVAL '5 minutes', time) AS time, mean(cpu) AS cpu FROM metrics WHERE time >= 1704067200 AND time < 1704070800 GROUP BY 1",
			method:   http.MethodGet,
			path:     "/api/v3/query_sql",
			wantStep: 5 * time.Minute,
		},
		{
			name:     "date_bin with SQL datetime",
			query:    "SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(temp) FROM weather WHERE time >= '2024-01-01 00:00:00' AND time < '2024-01-02 00:00:00' GROUP BY 1",
			method:   http.MethodGet,
			path:     "/api/v3/query_sql",
			wantStep: time.Hour,
		},
		{
			name:    "non-select should fail",
			query:   "CREATE TABLE test (id INT)",
			method:  http.MethodGet,
			path:    "/api/v3/query_sql",
			wantErr: true,
		},
		{
			name:     "date_trunc hour",
			query:    "SELECT date_trunc('hour', time) AS time, avg(temperature) AS temperature FROM weather WHERE time >= 1704067200 AND time < 1704153600 GROUP BY 1",
			method:   http.MethodGet,
			path:     "/api/v3/query_sql",
			wantStep: time.Hour,
		},
		{
			name:     "date_trunc minute",
			query:    "SELECT date_trunc('minute', time) AS time, avg(cpu) AS cpu FROM metrics WHERE time >= 1704067200 AND time < 1704070800 GROUP BY 1",
			method:   http.MethodGet,
			path:     "/api/v3/query_sql",
			wantStep: time.Minute,
		},
		{
			name:     "date_trunc day",
			query:    "SELECT date_trunc('day', time) AS time, avg(val) FROM stats WHERE time >= 1704067200 AND time < 1704153600 GROUP BY 1",
			method:   http.MethodGet,
			path:     "/api/v3/query_sql",
			wantStep: 24 * time.Hour,
		},
		{
			name:    "wrong format flag",
			query:   "SELECT * FROM test",
			method:  http.MethodGet,
			path:    "/query",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := &url.URL{Path: tt.path, RawQuery: url.Values{ParamQuery: {tt.query}}.Encode()}
			r := &http.Request{Method: tt.method, URL: u}
			f := iofmt.Detect(r)
			trq, _, _, err := ParseTimeRangeQuery(r, f)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if trq.Step != tt.wantStep {
				t.Errorf("step: got %v, want %v", trq.Step, tt.wantStep)
			}
		})
	}
}

func TestParseTimeRangeQuery_POST(t *testing.T) {
	u := &url.URL{Path: "/api/v3/query_sql"}
	r := &http.Request{
		Method: http.MethodPost,
		URL:    u,
		Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
	}
	f := iofmt.Detect(r)
	if !f.IsV3SQL() {
		t.Fatal("expected V3SQL format")
	}
}

func TestParse_WhereTimeRange(t *testing.T) {
	query := "SELECT date_bin(INTERVAL '1 hour', time) AS time, avg(temp) AS temp FROM weather WHERE time >= 1704067200 AND time < 1704153600 GROUP BY 1"
	trq, _, _, err := parse(query)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if trq.Extent.Start.IsZero() {
		t.Error("expected non-zero start time")
	}
	if trq.Extent.End.IsZero() {
		t.Error("expected non-zero end time")
	}
	if trq.Step != time.Hour {
		t.Errorf("expected 1h step, got %v", trq.Step)
	}
}
