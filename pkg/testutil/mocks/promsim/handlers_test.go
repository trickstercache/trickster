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

package promsim

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	expectedUpRange   = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"series_id":"0"},"values":[[0,"19"],[15,"63"],[30,"18"]]}]}}`
	expectedUpInstant = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"series_id":"0"},"value":[30,"18"]}]}}`
)

func serve(t *testing.T, r *http.Request) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	b, err := io.ReadAll(w.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	return w.Code, string(b)
}

func get(t *testing.T, path string) (int, string) {
	t.Helper()
	return serve(t, httptest.NewRequest(http.MethodGet, "http://0"+path, nil))
}

func TestQueryRangeHandler(t *testing.T) {
	for _, q := range []string{
		"query=up&start=0&end=30&step=15",
		"query=up&start=0&end=30&step=15s",
		"query=up&start=0.000&end=30.456&step=15",
	} {
		code, body := get(t, PathQueryRange+"?"+q)
		if code != http.StatusOK {
			t.Errorf("%s: expected 200 got %d", q, code)
		}
		if body != expectedUpRange {
			t.Errorf("%s: expected %s got %s", q, expectedUpRange, body)
		}
	}
}

func TestQueryRangeHandlerPostForm(t *testing.T) {
	form := url.Values{"query": {"up"}, "start": {"0"}, "end": {"30"}, "step": {"15"}}
	r := httptest.NewRequest(http.MethodPost, "http://0"+PathQueryRange, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if code, body := serve(t, r); code != http.StatusOK || body != expectedUpRange {
		t.Errorf("expected 200 %s got %d %s", expectedUpRange, code, body)
	}
}

func TestQueryRangeHandlerBadRequests(t *testing.T) {
	for _, q := range []string{
		"q=up&start=0&end=30&step=15",
		"query=up&start=foo&end=30&step=15",
		"query=up&start=0&end=foo&step=15",
		"query=up&start=0&end=30&step=foo",
		"query=up&start=0&end=30&step=0",
		"query=up{status_code=400}&start=0&end=30&step=15",
		"query=up&start=NaN&end=30&step=15",
		"query=up&start=0&end=Inf&step=15",
		"query=up&start=-1e300&end=1e300&step=15",
		"query=up&start=0&end=30&step=9223372037",
		"query=up&start=0&end=30&step=18446744074",
		"query=up&start=0&end=30&step=99999999999y",
		"query=up&start=0&end=1000000000&step=1",
		"query=up&start=30&end=0&step=15",
	} {
		if code, _ := get(t, PathQueryRange+"?"+q); code != http.StatusBadRequest {
			t.Errorf("%s: expected %d got %d", q, http.StatusBadRequest, code)
		}
	}
}

func TestQueryHandler(t *testing.T) {
	for _, q := range []string{"query=up&time=30", "query=up&time=30.456"} {
		code, body := get(t, PathQuery+"?"+q)
		if code != http.StatusOK {
			t.Errorf("%s: expected 200 got %d", q, code)
		}
		if body != expectedUpInstant {
			t.Errorf("%s: expected %s got %s", q, expectedUpInstant, body)
		}
	}
	if code, _ := get(t, PathQuery+"?query=up"); code != http.StatusOK {
		t.Errorf("expected 200 got %d", code)
	}
	if code, _ := get(t, PathQuery+"?query="+url.QueryEscape(`test{status_code="500"}`)); code != http.StatusInternalServerError {
		t.Errorf("expected %d got %d", http.StatusInternalServerError, code)
	}
}

func TestQueryHandlerBadRequests(t *testing.T) {
	for _, q := range []string{"q=up", "query=up&time=foo"} {
		if code, _ := get(t, PathQuery+"?"+q); code != http.StatusBadRequest {
			t.Errorf("%s: expected %d got %d", q, http.StatusBadRequest, code)
		}
	}
}

func TestParseTime(t *testing.T) {
	if _, err := parseTime("2006-01-02T15:04:05.999999999Z"); err != nil {
		t.Error(err)
	}
	if _, err := parseTime("foo"); err == nil {
		t.Error("expected an error")
	}
}

func TestParseDuration(t *testing.T) {
	valid := map[string]int64{"15": 15, "1h": 3600, "2m": 120, "-1": -1, "1d": 86400}
	for in, expected := range valid {
		d, err := parseDuration(in)
		if err != nil || d != expected {
			t.Errorf("%s: expected %d got %d (%v)", in, expected, d, err)
		}
	}
	for _, in := range []string{"1x", "1.3", "", "1s1t", "s", "99999999999999999999s"} {
		if d, err := parseDuration(in); err == nil {
			t.Errorf("%s: expected an error, got %d", in, d)
		}
	}
}

func TestQueryRangeHandlerBadDataBody(t *testing.T) {
	code, body := get(t, PathQueryRange+"?query=up&start=0&end=1000000000&step=1")
	if code != http.StatusBadRequest || !strings.Contains(body, "exceeded maximum resolution") {
		t.Errorf("expected a 400 resolution error, got %d %s", code, body)
	}
}

func TestParseDurationOverflow(t *testing.T) {
	if d, err := parseDuration("106751d"); err != nil || d != 106751*86400 {
		t.Errorf("expected %d got %d (%v)", 106751*86400, d, err)
	}
	for _, in := range []string{"106752d", "292471y", "9223372036854775807h"} {
		if d, err := parseDuration(in); err == nil {
			t.Errorf("%s: expected an overflow error, got %d", in, d)
		}
	}
}
