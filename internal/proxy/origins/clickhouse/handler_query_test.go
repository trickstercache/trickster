/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Comcast/trickster/internal/proxy/request"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func testRawQuery() string {
	return url.Values(map[string][]string{"query": {
		`SELECT (intDiv(toUInt32(time_column), 60) * 60) * 1000 AS t, countMerge(some_count) AS cnt, field1, field2 ` +
			`FROM testdb.test_table WHERE time_column BETWEEN toDateTime(1516665600) AND toDateTime(1516687200) ` +
			`AND date_column >= toDate(1516665600) AND toDate(1516687200) ` +
			`AND field1 > 0 AND field2 = 'some_value' GROUP BY t, field1, field2 ORDER BY t, field1 FORMAT JSON`}}).Encode()
}

func testNonSelectQuery() string {
	return url.Values(map[string][]string{"query": {
		`UPDATE (intDiv(toUInt32(time_column), 60) * 60) * 1000 AS t`}}).Encode()
	// not a real query, just something to trigger a non-select proxy-only request
}

func TestQueryHandler(t *testing.T) {

	client := &Client{name: "test"}
	ts, w, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "clickhouse", "/?"+testRawQuery(), "debug")
	ctx := r.Context()
	rsc := request.GetResources(r)
	rsc.OriginClient = client
	client.config = rsc.OriginConfig
	client.webClient = hc
	client.config.HTTPClient = hc
	client.baseUpstreamURL, _ = url.Parse(ts.URL)
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

	_, ok := client.config.Paths["/"]
	if !ok {
		t.Errorf("could not find path config named %s", "/")
	}

	client.QueryHandler(w, r)

	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "{}" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}

	r, _ = http.NewRequest(http.MethodGet, ts.URL+"/?"+testNonSelectQuery(), nil)
	w = httptest.NewRecorder()

	r = r.WithContext(ctx)

	client.QueryHandler(w, r)

	resp = w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "{}" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}

}
