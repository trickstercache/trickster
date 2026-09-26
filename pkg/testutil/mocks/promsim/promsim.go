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

// Package promsim simulates the Prometheus HTTP API v1 query endpoints for tests
// and the dev environment. Values are repeatable functions of query and time.
package promsim

import (
	"encoding/json"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Query selector labels that change the simulator's behavior; for example,
// up{status_code="500"} answers 500, and up{invalid_response_body=1} answers "foo".
const (
	ModStatusCode          = "status_code"
	ModInvalidResponseBody = "invalid_response_body"
)

// MaxResolution caps (end-start)/step for range queries, as Prometheus does.
const MaxResolution = 11000

const (
	labelSeriesID = "series_id"
	maxValue      = 100
	invalidBody   = "foo"

	msgBadStep        = "zero or negative query resolution step widths are not accepted. Try a positive integer"
	msgEndBeforeStart = "end timestamp must not be before start time"
	msgTooManyPoints  = "exceeded maximum resolution of 11,000 points per timeseries. Try decreasing the query resolution (?step=XX)"

	matrixPrefix = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{`
	vectorPrefix = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{`
)

type modifiers struct {
	statusCode  int
	invalidBody bool
	labels      string
	seed        uint64
}

// GetInstantData returns a simulated vector response for the query at t, or at
// the current time when t is zero, along with the HTTP status to respond with.
func GetInstantData(query string, t time.Time) (string, int) {
	if t.IsZero() {
		t = time.Now()
	}
	m := parseModifiers(query)
	if m.invalidBody {
		return invalidBody, m.statusCode
	}
	return vectorPrefix + m.labels + `},"value":[` + strconv.FormatInt(t.Unix(), 10) +
		`,"` + strconv.Itoa(m.value(t)) + `"]}]}}`, m.statusCode
}

// GetTimeSeriesData returns a simulated matrix response for the query, with
// points every step, aligned to end, and the HTTP status to respond with.
func GetTimeSeriesData(query string, start, end time.Time, step time.Duration) (string, int) {
	if msg := validateRange(start, end, step); msg != "" {
		return badData(msg), http.StatusBadRequest
	}
	m := parseModifiers(query)
	if m.invalidBody {
		return invalidBody, m.statusCode
	}
	n := int(end.Sub(start) / step) // bounded by validateRange
	start = end.Add(time.Duration(-n) * step)
	var b strings.Builder
	b.Grow(len(matrixPrefix) + len(m.labels) + 24 + (n+1)*18)
	b.WriteString(matrixPrefix)
	b.WriteString(m.labels)
	b.WriteString(`},"values":[`)
	for j := range n + 1 {
		if j > 0 {
			b.WriteByte(',')
		}
		t := start.Add(time.Duration(j) * step)
		b.WriteByte('[')
		b.WriteString(strconv.FormatInt(t.Unix(), 10))
		b.WriteString(`,"`)
		b.WriteString(strconv.Itoa(m.value(t)))
		b.WriteString(`"]`)
	}
	b.WriteString("]}]}}")
	return b.String(), m.statusCode
}

func validateRange(start, end time.Time, step time.Duration) string {
	switch {
	case step <= 0:
		return msgBadStep
	case end.Before(start):
		return msgEndBeforeStart
	case end.Sub(start)/step > MaxResolution: // Sub saturates rather than overflowing
		return msgTooManyPoints
	}
	return ""
}

func badData(msg string) string {
	return `{"status":"error","errorType":"bad_data","error":` + jsonString(msg) + `}`
}

func parseModifiers(query string) modifiers {
	m := modifiers{statusCode: http.StatusOK, seed: hashString(query)}
	var labels []string
	if start := strings.Index(query, "{") + 1; start > 0 {
		if end := strings.LastIndex(query, "}"); end > start {
			for mod := range strings.SplitSeq(query[start:end], ",") {
				k, v, ok := strings.Cut(mod, "=")
				if !ok {
					labels = append(labels, jsonString(mod)+`:""`)
					continue
				}
				v = strings.ReplaceAll(v, `"`, "")
				if i, err := strconv.Atoi(v); err == nil {
					switch k {
					case ModStatusCode:
						// codes outside this range would make WriteHeader panic
						if i >= 200 && i <= 599 {
							m.statusCode = i
						}
					case ModInvalidResponseBody:
						m.invalidBody = i > 0
					}
				}
				labels = append(labels, jsonString(k)+":"+jsonString(v))
			}
		}
	}
	m.labels = strings.Join(append(labels, `"`+labelSeriesID+`":"0"`), ",")
	return m
}

func (m modifiers) value(t time.Time) int {
	ts := uint64(t.Unix()) //nolint:gosec // wraparound is harmless when hashing
	return int(splitmix64(m.seed^(ts*0x9e3779b97f4a7c15)) % maxValue)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s) // a string always marshals
	return string(b)
}

func hashString(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}
