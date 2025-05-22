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

package influxql

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/influxdata/influxql"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const expectedTokenized = "SELECT * FROM some_column WHERE time >= '$START_TIME$' AND time < '$END_TIME$' GROUP BY time(1m)"
const untokenized = "SELECT * FROM some_column WHERE time >= now() - 6h GROUP BY time(1m)"

const testQuery = `SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time >= now() - 6h ` +
	`GROUP BY time(15s), "cluster" fill(null)`

var testVals = url.Values(map[string][]string{"q": {testQuery},
	"epoch": {"ms"}})
var testRawQuery = testVals.Encode()

func TestParseTimeRangeQuery(t *testing.T) {

	// test GET
	req := &http.Request{
		Method: http.MethodGet,
		URL: &url.URL{
			Scheme:   "https",
			Host:     "blah.com",
			Path:     "/",
			RawQuery: testRawQuery,
		}}

	trq, _, _, err := ParseTimeRangeQuery(req, iofmt.InfluxqlGet)
	if err != nil {
		t.Error(err)
	} else {
		if trq.Step.Seconds() != 15 {
			t.Errorf("expected %d got %d", 15, int(trq.Step.Seconds()))
		}
		if int(trq.Extent.End.Sub(trq.Extent.Start).Hours()) != 6 {
			t.Errorf("expected %d got %d", 6, int(trq.Extent.End.Sub(trq.Extent.Start).Hours()))
		}
	}

	req, _ = http.NewRequest(http.MethodPost, "http://blah.com/", io.NopCloser(strings.NewReader(testRawQuery)))
	req.Header.Set(headers.NameContentLength, strconv.Itoa(len(testRawQuery)))
	req.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)

	trq, _, _, err = ParseTimeRangeQuery(req, iofmt.InfluxqlPost)
	if err != nil {
		t.Error(err)
	} else {
		if trq.Step.Seconds() != 15 {
			t.Errorf("expected %d got %d", 15, int(trq.Step.Seconds()))
		}
		if int(trq.Extent.End.Sub(trq.Extent.Start).Hours()) != 6 {
			t.Errorf("expected %d got %d", 6, int(trq.Extent.End.Sub(trq.Extent.Start).Hours()))
		}
	}

}

func TestSetExtent(t *testing.T) {

	start := time.Now().UTC().Add(time.Duration(-6) * time.Hour).Truncate(time.Second)
	end := time.Now().UTC().Truncate(time.Second)

	startToken := start.Format(time.RFC3339Nano)
	endToken := end.Add(time.Second * 60).Format(time.RFC3339Nano)

	expected := strings.ReplaceAll(strings.ReplaceAll(expectedTokenized, "$START_TIME$", startToken), "$END_TIME$", endToken)

	qs := url.Values{"q": {untokenized}, "epoch": {"ms"}}.Encode()

	tu, _ := url.Parse("http://example.com?" + qs)

	r, _ := http.NewRequest(http.MethodGet, tu.String(), nil)
	trq := &timeseries.TimeRangeQuery{TemplateURL: tu, Step: time.Second * 60, Statement: untokenized}
	e := &timeseries.Extent{Start: start, End: end}
	p := influxql.NewParser(strings.NewReader(trq.Statement))
	q, err := p.ParseQuery()
	if err != nil {
		t.Error(err)
	}
	SetExtent(r, trq, e, q)
	if expected != r.URL.Query().Get("q") {
		t.Errorf("\nexpected [%s]\ngot    [%s]", expected, r.URL.Query().Get("q"))
	}

	r.Method = http.MethodPost
	r.Body = io.NopCloser(bytes.NewBufferString(qs))
	SetExtent(r, trq, e, q)
	v, _, _ := params.GetRequestValues(r)
	if expected != v.Get("q") {
		t.Errorf("\nexpected [%s]\ngot    [%s]", expected, v.Get("q"))
	}

}
