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

package bucketsim

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestBuckets(t *testing.T) {
	const step = time.Minute
	u := func(sec int64) time.Time { return time.Unix(sec, 0) }
	far := u(1 << 40)
	tests := []struct {
		name            string
		start, end, now time.Time
		step            time.Duration
		want            []Bucket
	}{
		{"whole buckets", u(60), u(180), far, step, []Bucket{{60, 60}, {120, 60}}},
		{"partial edges", u(67), u(193), far, step, []Bucket{{60, 53}, {120, 60}, {180, 13}}},
		{"rows end at now", u(60), u(300), u(150), step, []Bucket{{60, 60}, {120, 31}}},
		{"an empty range", u(60), u(60), far, step, nil},
		{"before the epoch", u(-90), u(-30), far, step, []Bucket{{-120, 30}, {-60, 30}}},
		{"a step under a second", u(0), u(10), far, time.Millisecond, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Buckets(test.start, test.end, test.step, test.now); !reflect.DeepEqual(got, test.want) {
				t.Errorf("got %v want %v", got, test.want)
			}
		})
	}
}

func TestQueryRangeHandler(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)
	serve := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, PathQueryRange+"?"+query, nil))
		return w
	}
	w := serve("query=rows{x}&start=67&end=193&step=60")
	const want = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":` +
		`"rows"},"values":[[60,"53"],[120,"60"],[180,"13"]]}]}}`
	if w.Code != http.StatusOK || w.Body.String() != want {
		t.Errorf("got %d %s", w.Code, w.Body.String())
	}
	// an open end runs to now, so the latest bucket counts the rows it has so far
	now := time.Now().Unix()
	start := now - now%60
	w = serve("query=rows&step=60&start=" + strconv.FormatInt(start, 10))
	if w.Code != http.StatusOK {
		t.Errorf("open end: got %d", w.Code)
	}
	for _, query := range []string{
		"start=0&end=60&step=60", "query=rows&start=x&end=60&step=60", "query=rows&start=0&end=60&step=0",
		"query=rows&start=60&end=0&step=60", "query=rows&start=0&end=x&step=60",
	} {
		if w := serve(query); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", query, w.Code)
		}
	}
	for _, query := range []string{"start=7&end=60", "start=0&end=67", "start=7"} {
		if w := serve("query=rows{" + ModFailUnaligned + "}&step=60&" + query); w.Code != http.StatusInternalServerError {
			t.Errorf("%s: got %d", query, w.Code)
		}
	}
	if w := serve("query=rows{" + ModFailUnaligned + "}&step=60&start=0&end=120"); w.Code != http.StatusOK {
		t.Errorf("aligned: got %d", w.Code)
	}
}
