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

// Package bucketsim simulates a bucketed origin with a row per second; each bucket counts its rows
// in the requested range, so a partly covered bucket counts fewer
package bucketsim

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PathQueryRange is the route that Register mounts. It takes Prometheus range parameters in whole
// seconds: start is inclusive, end exclusive, and a missing end runs to now.
const PathQueryRange = "/bucketsim/api/v1/query_range"

// ModFailUnaligned in a query refuses a range whose start or end is off the step grid, which fails a
// partial bucket's fetch while the whole buckets around it succeed
const ModFailUnaligned = "fail_unaligned"

const (
	paramQuery = "query"
	paramStart = "start"
	paramEnd   = "end"
	paramStep  = "step"

	hnContentType = "Content-Type"
	hvJSON        = "application/json"

	matrixPrefix = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":`
	matrixSuffix = `]}]}}`
)

// Bucket is one bucket of a response: the Unix second of its start, and the rows it counted
type Bucket struct {
	Label int64
	Rows  int64
}

// Register mounts the simulator on the mux.
func Register(mux *http.ServeMux) {
	mux.HandleFunc(PathQueryRange, queryRangeHandler)
}

// Buckets returns the buckets answering [start, end) at step, in whole seconds, holding a row at or
// before now, each with its rows in the range; nil for a step under a second
func Buckets(start, end time.Time, step time.Duration, now time.Time) []Bucket {
	s := int64(step / time.Second)
	if s <= 0 {
		return nil
	}
	var out []Bucket
	for rowSec, lastSec := start.Unix(), min(end.Unix()-1, now.Unix()); rowSec <= lastSec; {
		labelSec := floorDiv(rowSec, s) * s
		upperSec := min(labelSec+s-1, lastSec)
		out = append(out, Bucket{Label: labelSec, Rows: upperSec - rowSec + 1})
		rowSec = upperSec + 1
	}
	return out
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func queryRangeHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	q := r.Form.Get(paramQuery)
	start, errStart := strconv.ParseInt(r.Form.Get(paramStart), 10, 64)
	step, errStep := strconv.ParseInt(r.Form.Get(paramStep), 10, 64)
	if q == "" || errStart != nil || errStep != nil || step <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	now := time.Now()
	// an open end takes every row up to now
	end := now.Unix() + 1
	if v := r.Form.Get(paramEnd); v != "" {
		var err error
		if end, err = strconv.ParseInt(v, 10, 64); err != nil || end < start {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if strings.Contains(q, ModFailUnaligned) && end%step != 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	if strings.Contains(q, ModFailUnaligned) && start%step != 0 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	b.WriteString(matrixPrefix)
	b.WriteString(strconv.Quote(name(q)))
	b.WriteString(`},"values":[`)
	for i, bk := range Buckets(time.Unix(start, 0), time.Unix(end, 0), time.Duration(step)*time.Second, now) {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('[')
		b.WriteString(strconv.FormatInt(bk.Label, 10))
		b.WriteString(`,"`)
		b.WriteString(strconv.FormatInt(bk.Rows, 10))
		b.WriteString(`"]`)
	}
	b.WriteString(matrixSuffix)
	w.Header().Set(hnContentType, hvJSON)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(b.String()))
}

func name(q string) string {
	// the metric is the query's leading name, before any selector
	if i := strings.IndexAny(q, "{( "); i > 0 {
		return q[:i]
	}
	return q
}
