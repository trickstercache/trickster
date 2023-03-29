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
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/readers"
)

func testRawQuery() string {
	return url.Values(map[string][]string{"query": {
		`SELECT (intDiv(toUInt32(time_column), 60) * 60) * 1000 AS t, countMerge(some_count) AS cnt, field1, field2 ` +
			`FROM testdb.test_table WHERE time_column BETWEEN toDateTime(1516665600) AND toDateTime(1516687200) ` +
			`AND date_column >= toDate(1516665600) AND toDate(1516687200) ` +
			`AND field1 > 0 AND field2 = 'some_value' GROUP BY t, field1, field2 ORDER BY t, field1 FORMAT JSON`}}).
		Encode()
}

func testNonSelectQuery() string {
	return url.Values(map[string][]string{"enable_http_compression": {"1"}}).Encode()
	// not a real query, just something to trigger a non-select proxy-only request
}

func TestQueryHandler(t *testing.T) {

	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs,
		200, "{}", nil, "clickhouse", "/?"+testRawQuery(), "debug")
	ctx := r.Context()
	if err != nil {
		t.Error(err)
	} else {
		defer ts.Close()
	}
	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	rsc.BackendClient = client
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()

	_, ok := client.Configuration().Paths["/"]
	if !ok {
		t.Errorf("could not find path config named %s", "/")
	}

	client.QueryHandler(w, r)

	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
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

	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "{}" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}

}

func TestQueryHandlerBody(t *testing.T) {
	t.Run("body_and_query", func(t *testing.T) {
		backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
		if err != nil {
			t.Error(err)
		}
		ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs,
			200, "{}", nil, "clickhouse", "/?"+testRawQuery(), "debug")
		if err != nil {
			t.Error(err)
		} else {
			defer ts.Close()
		}
		r.Method = http.MethodPost
		r.Body = io.NopCloser(bytes.NewReader([]byte(testRawQuery())))

		rsc := request.GetResources(r)
		backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
		if err != nil {
			t.Error(err)
		}
		client := backendClient.(*Client)
		rsc.BackendClient = client
		rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()

		_, ok := client.Configuration().Paths["/"]
		if !ok {
			t.Errorf("could not find path config named %s", "/")
		}

		client.QueryHandler(w, r)

		resp := w.Result()

		// it should return 200 OK
		if resp.StatusCode != 200 {
			t.Errorf("expected 200 got %d.", resp.StatusCode)
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Error(err)
		}

		if string(bodyBytes) != "{}" {
			t.Errorf("expected '{}' got %s.", bodyBytes)
		}
	})
	t.Run("body_no_query", func(t *testing.T) {
		backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
		if err != nil {
			t.Error(err)
		}
		ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs,
			200, "{}", nil, "clickhouse", "/", "debug")
		if err != nil {
			t.Error(err)
		} else {
			defer ts.Close()
		}
		r.Method = http.MethodPost
		r.Body = io.NopCloser(bytes.NewReader([]byte(testRawQuery())))

		rsc := request.GetResources(r)
		backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
		if err != nil {
			t.Error(err)
		}
		client := backendClient.(*Client)
		rsc.BackendClient = client
		rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()

		_, ok := client.Configuration().Paths["/"]
		if !ok {
			t.Errorf("could not find path config named %s", "/")
		}

		client.QueryHandler(w, r)

		resp := w.Result()

		// it should return 200 OK
		if resp.StatusCode != 200 {
			t.Errorf("expected 200 got %d.", resp.StatusCode)
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Error(err)
		}

		if string(bodyBytes) != "{}" {
			t.Errorf("expected '{}' got %s.", bodyBytes)
		}
	})
	t.Run("bad_read", func(t *testing.T) {
		backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
		if err != nil {
			t.Error(err)
		}
		ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs,
			200, "{}", nil, "clickhouse", "/?"+testRawQuery(), "debug")
		if err != nil {
			t.Error(err)
		} else {
			defer ts.Close()
		}
		r.Method = http.MethodPost
		r.Body = io.NopCloser(&readers.BadReader{})

		rsc := request.GetResources(r)
		backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
		if err != nil {
			t.Error(err)
		}
		client := backendClient.(*Client)
		rsc.BackendClient = client
		rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()

		_, ok := client.Configuration().Paths["/"]
		if !ok {
			t.Errorf("could not find path config named %s", "/")
		}

		client.QueryHandler(w, r)

		resp := w.Result()

		// it should return 400 Bad Request
		if resp.StatusCode != 400 {
			t.Errorf("expected 400 got %d.", resp.StatusCode)
		}
	})
}
