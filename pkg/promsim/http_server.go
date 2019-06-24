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
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"time"
)

// NewTestServer launches a Test Prometheus Server (for unit testing)
func NewTestServer() *httptest.Server {
	return httptest.NewServer(MuxWithRoutes())
}

// MuxWithRoutes returns a ServeMux that includes the PromSim handlers already registered
func MuxWithRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query_range", queryRangeHandler)
	mux.HandleFunc("/api/v1/query", queryHandler)
	return mux
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

		start, err = parseTime(s)
		if err != nil {
			writeError(http.StatusBadRequest, []byte("unable to parse start time parameter"), w)
			return
		}

		end, err = parseTime(e)
		if err != nil {
			writeError(http.StatusBadRequest, []byte("unable to parse end time parameter"), w)
			return
		}

		i, err = strconv.ParseInt(p, 10, 64)
		if err != nil {
			writeError(http.StatusBadRequest, []byte("unable to parse step parameter"), w)
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
	writeError(http.StatusBadRequest, []byte("missing required parameter"), w)
}

func queryHandler(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")

	params := r.URL.Query()
	q := params.Get("query")
	t := params.Get("time")

	if q != "" {
		tm := time.Now()
		if t != "" {
			var err error
			tm, err = parseTime(t)
			if err != nil {
				writeError(http.StatusBadRequest, []byte("unable to parse time parameter"), w)
				return
			}
		}

		json, code, _ := GetInstantData(q, tm)
		w.WriteHeader(code)
		w.Write([]byte(json))
		return
	}
	writeError(http.StatusBadRequest, []byte("missing required parameter 'query'"), w)
}

func writeError(code int, body []byte, w http.ResponseWriter) {
	w.WriteHeader(code)
	w.Write(body)
}

// parseTime converts a query time URL parameter to time.Time.
// Copied from https://github.com/prometheus/prometheus/blob/master/web/api/v1/api.go
func parseTime(s string) (time.Time, error) {
	if t, err := strconv.ParseFloat(s, 64); err == nil {
		s, ns := math.Modf(t)
		ns = math.Round(ns*1000) / 1000
		return time.Unix(int64(s), int64(ns*float64(time.Second))), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cannot parse %q to a valid timestamp", s)
}
