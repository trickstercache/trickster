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

package prometheus

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"
)

// InsertRoutes inserts the mock's routes into the provided Mux
func InsertRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/prometheus/api/v1/query_range", queryRangeHandler)
	mux.HandleFunc("/prometheus/api/v1/query", queryHandler)
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

		i, err = parseDuration(p)
		if err != nil {
			writeError(http.StatusBadRequest, []byte(fmt.Sprintf("unable to parse step parameter: %s", p)), w)
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

func parseDuration(input string) (int64, error) {

	v, err := strconv.ParseInt(input, 10, 64)
	if err == nil {
		return v, nil
	}

	for i := range input {
		if input[i] > 47 && input[i] < 58 {
			continue
		}
		if input[i] == 46 {
			break
		}
		if i > 0 {
			units, ok := UnitMap[input[i:]]
			if !ok {
				return 0, durationError(input)
			}
			v, err := strconv.ParseInt(input[0:i], 10, 64)
			if err != nil {
				return 0, durationError(input)
			}
			v = v * units
			return int64(time.Duration(v).Seconds()), nil
		}
	}
	return 0, durationError(input)
}

func durationError(input string) error {
	return fmt.Errorf("cannot parse %q to a valid duration", input)
}

// UnitMap provides a map of common time unit indicators to nanoseconds of duration per unit
var UnitMap = map[string]int64{
	"ns": int64(time.Nanosecond),
	"us": int64(time.Microsecond),
	"µs": int64(time.Microsecond), // U+00B5 = micro symbol
	"μs": int64(time.Microsecond), // U+03BC = Greek letter mu
	"ms": int64(time.Millisecond),
	"s":  int64(time.Second),
	"m":  int64(time.Minute),
	"h":  int64(time.Hour),
	"d":  int64(24 * time.Hour),
	"w":  int64(24 * 7 * time.Hour),
	"y":  int64(24 * 365 * time.Hour),
}
