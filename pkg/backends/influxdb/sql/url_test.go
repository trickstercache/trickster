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
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestSetExtent_AliasedTimeColumn(t *testing.T) {
	query := "SELECT date_bin(INTERVAL '1 hour', t) AS bucket, avg(temperature) AS temperature FROM weather WHERE t >= 1704067200 AND t < 1704153600 GROUP BY 1"
	u := &url.URL{Path: "/api/v3/query_sql", RawQuery: url.Values{ParamQuery: {query}}.Encode()}
	r := &http.Request{Method: http.MethodGet, URL: u}
	f := iofmt.Detect(r)
	trq, _, _, err := ParseTimeRangeQuery(r, f)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	ext := &timeseries.Extent{
		Start: time.Unix(1704067200, 0),
		End:   time.Unix(1704070800, 0),
	}
	q, ok := trq.ParsedQuery.(*Query)
	if !ok {
		t.Fatalf("expected *Query, got %T", trq.ParsedQuery)
	}
	SetExtent(r, trq, ext, q)
	got := r.URL.Query().Get(ParamQuery)
	if strings.Contains(got, "bucket >=") || strings.Contains(got, "bucket <") {
		t.Errorf("WHERE must use base column `t`, not alias `bucket`; got:\n%s", got)
	}
	if !strings.Contains(got, "t >=") || !strings.Contains(got, "t <") {
		t.Errorf("WHERE must reference base column `t`; got:\n%s", got)
	}
}
