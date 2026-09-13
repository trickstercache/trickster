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
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestSetExtent(t *testing.T) {
	trq, _, _, err := parse(tq03, nil)
	if err != nil {
		t.Fatal(err)
	}
	extent := &timeseries.Extent{Start: time.Unix(1516669200, 0), End: time.Unix(1516672800, 0)}
	r, err := http.NewRequest(http.MethodGet, "http://example/?"+
		url.Values{upQuery: []string{trq.Statement}}.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Client{}).SetExtent(r, trq, extent); err != nil {
		t.Fatal(err)
	}
	rendered := r.URL.Query().Get(upQuery)
	if !strings.Contains(rendered, "time_column >= toDateTime(1516669200)") ||
		!strings.Contains(rendered, "time_column < toDateTime(1516672860)") {
		t.Errorf("primary extent was not rendered: %s", rendered)
	}
	if !strings.Contains(rendered, "date_column >= toDate(1516669200)") ||
		!strings.Contains(rendered, "date_column <= toDate(1516672800)") {
		t.Errorf("secondary extent was not rendered: %s", rendered)
	}
	if !strings.Contains(rendered, "FORMAT TSVWithNamesAndTypes") {
		t.Errorf("origin format was not forced: %s", rendered)
	}
}

func TestSetExtentWithBody(t *testing.T) {
	query := `SELECT toStartOfMinute(ts) AS t, count() FROM events ` +
		`WHERE ts >= '2020-01-01 00:00:00' AND ts < '2020-01-01 01:00:00' GROUP BY t FORMAT JSON`
	trq, _, _, err := parse(query, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.NewRequest(http.MethodPost, "http://example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Client{}).SetExtent(r, trq, &timeseries.Extent{
		Start: time.Date(2020, 1, 1, 0, 10, 0, 0, time.UTC),
		End:   time.Date(2020, 1, 1, 0, 20, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	body, err := request.GetBody(r)
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(body)
	if !strings.Contains(rendered, "ts >= '2020-01-01 00:10:00'") ||
		!strings.Contains(rendered, "ts < '2020-01-01 00:21:00'") {
		t.Errorf("SQL datetime extent was not rendered: %s", rendered)
	}
}

func TestSetExtentNativeFormatPreserved(t *testing.T) {
	client := &Client{}
	start := time.Unix(1589904000, 0)
	end := time.Unix(1589997600, 0)
	e := &timeseries.Extent{Start: start, End: end}

	trq, _, _, err := parse(`SELECT toStartOfFiveMinute(datetime) AS t, count() AS cnt FROM tbl WHERE datetime >= 1589904000 AND datetime < 1589997900 GROUP BY t ORDER BY t`, nil)
	if err != nil {
		t.Fatal(err)
	}

	// GET request with default_format=Native (official Grafana plugin style)
	tu := &url.URL{RawQuery: url.Values{
		"query":                   {trq.Statement},
		"default_format":          {"Native"},
		"client_protocol_version": {"54460"},
		"database":                {"default"},
	}.Encode()}
	r, _ := http.NewRequest(http.MethodGet, tu.String(), nil)
	r.URL = tu

	if err := client.SetExtent(r, trq, e); err != nil {
		t.Fatal(err)
	}

	q := r.URL.Query()
	if !q.Has("default_format") {
		t.Error("expected default_format to be preserved in URL params")
	}
	if !q.Has("database") {
		t.Error("expected database param to be preserved")
	}
	// Origin requests use the analyzer's TSV format regardless of the client format.
	sql := q.Get("query")
	if !strings.Contains(sql, "FORMAT TSVWithNamesAndTypes") {
		t.Errorf("expected TSV origin format with Native client output, got: %s", sql)
	}
}

func TestSetExtentTSVFormatInjected(t *testing.T) {
	client := &Client{}
	start := time.Unix(1589904000, 0)
	end := time.Unix(1589997600, 0)
	e := &timeseries.Extent{Start: start, End: end}

	trq, _, _, err := parse(`SELECT toStartOfFiveMinute(datetime) AS t, count() AS cnt FROM tbl WHERE datetime >= 1589904000 AND datetime < 1589997900 GROUP BY t ORDER BY t`, nil)
	if err != nil {
		t.Fatal(err)
	}

	// GET request WITHOUT default_format (standard TSV path)
	tu := &url.URL{RawQuery: url.Values{
		"query":    {trq.Statement},
		"database": {"default"},
	}.Encode()}
	r, _ := http.NewRequest(http.MethodGet, tu.String(), nil)
	r.URL = tu

	if err := client.SetExtent(r, trq, e); err != nil {
		t.Fatal(err)
	}

	q := r.URL.Query()
	sql := q.Get("query")
	if !strings.Contains(sql, "TSVWithNamesAndTypes") {
		t.Errorf("expected FORMAT TSVWithNamesAndTypes when no default_format, got: %s", sql)
	}
}
