/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package promsim

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"time"

	"github.com/Comcast/trickster/internal/routing" // need to eliminate this dependency
)

// NewTestServer launches a Test Prometheus Server (for unit testing)
func NewTestServer() *httptest.Server {
	routing.Router.HandleFunc("/api/v1/query_range", queryRangeHandler).Methods("GET", "POST")
	routing.Router.HandleFunc("/api/v1/query", queryHandler).Methods("GET", "POST")
	s := httptest.NewServer(routing.Router)
	return s
}

func queryRangeHandler(w http.ResponseWriter, r *http.Request) {

	params := r.URL.Query()
	q := params.Get("query")
	s := params.Get("start")
	e := params.Get("end")
	p := params.Get("step")

	var err error
	if q != "" && s != "" && e != "" && p != "" {
		var i int64
		var start, end time.Time
		var step time.Duration

		i, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			writeError(http.StatusBadRequest, w)
			return
		}
		start = time.Unix(i, 0)

		i, err = strconv.ParseInt(e, 10, 64)
		if err != nil {
			writeError(http.StatusBadRequest, w)
			return
		}
		end = time.Unix(i, 0)

		i, err = strconv.ParseInt(p, 10, 64)
		if err != nil {
			writeError(http.StatusBadRequest, w)
			return
		}
		step = time.Duration(i) * time.Second

		json, code, _ := GetTimeSeriesData(q, start, end, step)

		if code == http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
		}

		w.WriteHeader(code)

		if code == http.StatusOK {
			w.Write([]byte(json))
		} else {
			w.Write([]byte{})
		}

		return
	}
	writeError(http.StatusBadRequest, w)
}

func queryHandler(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")

	params := r.URL.Query()
	q := params.Get("query")
	t := params.Get("time")

	var err error
	if q != "" {

		var i int64

		tm := time.Time{}
		if t != "" {
			i, err = strconv.ParseInt(t, 10, 64)
			if err != nil {
				writeError(http.StatusBadRequest, w)
				return
			}
			tm = time.Unix(i, 0)
		}

		json, code, _ := GetInstantData(q, tm)
		w.WriteHeader(code)
		w.Write([]byte(json))
		return
	}
	writeError(http.StatusBadRequest, w)
}

func writeError(code int, w http.ResponseWriter) {
	w.WriteHeader(code)
	w.Write([]byte{})
}
