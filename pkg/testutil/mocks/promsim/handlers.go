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
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Routes that Register mounts.
const (
	PathQueryRange = "/prometheus/api/v1/query_range"
	PathQuery      = "/prometheus/api/v1/query"
)

// Register mounts the simulator's query and query_range handlers on the mux.
func Register(mux *http.ServeMux) {
	mux.HandleFunc(PathQueryRange, queryRangeHandler)
	mux.HandleFunc(PathQuery, queryHandler)
}

const (
	hnContentType = "Content-Type"
	hvJSON        = "application/json"
)

var (
	errInvalidDuration = errors.New("invalid duration")
	errInvalidTime     = errors.New("invalid time")
)

const (
	// the steps that can be converted to a time.Duration without overflowing
	maxStepSeconds = math.MaxInt64 / int64(time.Second)
	// the Unix times for 0001-01-01 and 9999-12-31T23:59:59Z bound accepted times
	minUnixSeconds = -62135596800
	maxUnixSeconds = 253402300799
)

var unitMap = map[string]int64{
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

func queryRangeHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	q, s, e, p := r.Form.Get("query"), r.Form.Get("start"), r.Form.Get("end"), r.Form.Get("step")
	if q == "" || s == "" || e == "" || p == "" {
		writeError(http.StatusBadRequest, "missing required parameter", w)
		return
	}
	start, err := parseTime(s)
	if err != nil {
		writeError(http.StatusBadRequest, "unable to parse start time parameter", w)
		return
	}
	end, err := parseTime(e)
	if err != nil {
		writeError(http.StatusBadRequest, "unable to parse end time parameter", w)
		return
	}
	i, err := parseDuration(p)
	if err != nil || i <= 0 || i > maxStepSeconds {
		writeError(http.StatusBadRequest, "unable to parse step parameter: "+p, w)
		return
	}
	step := time.Duration(i) * time.Second
	if msg := validateRange(start, end, step); msg != "" {
		w.Header().Set(hnContentType, hvJSON)
		writeError(http.StatusBadRequest, badData(msg), w)
		return
	}
	body, code := GetTimeSeriesData(q, start, end, step)
	if code != http.StatusOK {
		w.WriteHeader(code)
		return
	}
	w.Header().Set(hnContentType, hvJSON)
	w.WriteHeader(code)
	w.Write([]byte(body))
}

func queryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(hnContentType, hvJSON)
	r.ParseForm()
	q := r.Form.Get("query")
	if q == "" {
		writeError(http.StatusBadRequest, "missing required parameter 'query'", w)
		return
	}
	tm := time.Now()
	if t := r.Form.Get("time"); t != "" {
		var err error
		if tm, err = parseTime(t); err != nil {
			writeError(http.StatusBadRequest, "unable to parse time parameter", w)
			return
		}
	}
	body, code := GetInstantData(q, tm)
	w.WriteHeader(code)
	w.Write([]byte(body))
}

func writeError(code int, body string, w http.ResponseWriter) {
	w.WriteHeader(code)
	w.Write([]byte(body))
}

func parseTime(s string) (time.Time, error) {
	// same forms as the Prometheus API: float epoch seconds or RFC3339Nano
	if t, err := strconv.ParseFloat(s, 64); err == nil {
		// NaN fails both comparisons, and bounded times keep the int64 conversions exact
		if !(t >= minUnixSeconds && t <= maxUnixSeconds) {
			return time.Time{}, errInvalidTime
		}
		s, ns := math.Modf(t)
		ns = math.Round(ns*1000) / 1000
		return time.Unix(int64(s), int64(ns*float64(time.Second))), nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func parseDuration(input string) (int64, error) {
	// whole seconds from an integer, or from an integer with a unit suffix
	if v, err := strconv.ParseInt(input, 10, 64); err == nil {
		return v, nil
	}
	i := strings.IndexFunc(input, func(r rune) bool { return r < '0' || r > '9' })
	if i <= 0 {
		return 0, errInvalidDuration
	}
	units, ok := unitMap[input[i:]]
	if !ok {
		return 0, errInvalidDuration
	}
	v, err := strconv.ParseInt(input[:i], 10, 64)
	if err != nil || v > math.MaxInt64/units {
		return 0, errInvalidDuration
	}
	return int64(time.Duration(v*units) / time.Second), nil
}
