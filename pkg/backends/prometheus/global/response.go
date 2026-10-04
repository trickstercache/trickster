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

package global

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
)

// Prometheus HTTP API error types
const (
	ErrorTypeBadData     = "bad_data"
	ErrorTypeExecution   = "execution"
	ErrorTypeTimeout     = "timeout"
	ErrorTypeCanceled    = "canceled"
	ErrorTypeInternal    = "internal"
	ErrorTypeUnavailable = "unavailable"
)

const (
	contentTypeJSON = "application/json"
	// maxPooledResponse keeps the response buffers of unusually large answers from staying in the pool
	maxPooledResponse = 4 << 20
)

var responseBuffers = sync.Pool{New: func() any { return new([]byte) }}

// WriteResult writes an engine result as the Prometheus HTTP API returns it.
func WriteResult(w http.ResponseWriter, value parser.Value, warnings, infos []string) {
	buf := responseBuffers.Get().(*[]byte)
	b := (*buf)[:0]
	defer func() {
		// a buffer grown by an unusually large answer is left to the collector
		if cap(b) <= maxPooledResponse {
			*buf = b
			responseBuffers.Put(buf)
		}
	}()
	b = append(b, `{"status":"success","data":{"resultType":"`...)
	b = append(b, value.Type()...)
	b = append(b, `","result":`...)
	b = appendValue(b, value)
	b = append(b, '}')
	b = appendStrings(b, "warnings", warnings)
	b = appendStrings(b, "infos", infos)
	b = append(b, '}')
	w.Header().Set(headers.NameContentType, contentTypeJSON)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// WriteError writes a Prometheus HTTP API error response.
func WriteError(w http.ResponseWriter, status int, errorType string, err error, warnings []string) {
	b := append([]byte(`{"status":"error","errorType":`), strconv.Quote(errorType)...)
	b = append(b, `,"error":`...)
	b = appendJSONString(b, err.Error())
	b = appendStrings(b, "warnings", warnings)
	b = append(b, '}')
	w.Header().Set(headers.NameContentType, contentTypeJSON)
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func appendStrings(b []byte, field string, values []string) []byte {
	if len(values) == 0 {
		return b
	}
	b = append(b, `,"`...)
	b = append(b, field...)
	b = append(b, `":[`...)
	for i, v := range values {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendJSONString(b, v)
	}
	return append(b, ']')
}

func appendValue(b []byte, value parser.Value) []byte {
	switch v := value.(type) {
	case promql.Matrix:
		b = append(b, '[')
		for i, series := range v {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendMetric(b, series.Metric)
			if len(series.Floats) > 0 {
				b = append(b, `,"values":[`...)
				for j, p := range series.Floats {
					if j > 0 {
						b = append(b, ',')
					}
					b = appendFloatPoint(b, p.T, p.F)
				}
				b = append(b, ']')
			}
			if len(series.Histograms) > 0 {
				b = append(b, `,"histograms":[`...)
				for j, p := range series.Histograms {
					if j > 0 {
						b = append(b, ',')
					}
					b = appendHistogramPoint(b, p.T, p.H)
				}
				b = append(b, ']')
			}
			b = append(b, '}')
		}
		return append(b, ']')
	case promql.Vector:
		b = append(b, '[')
		for i, s := range v {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendMetric(b, s.Metric)
			if s.H != nil {
				b = append(b, `,"histogram":`...)
				b = appendHistogramPoint(b, s.T, s.H)
			} else {
				b = append(b, `,"value":`...)
				b = appendFloatPoint(b, s.T, s.F)
			}
			b = append(b, '}')
		}
		return append(b, ']')
	case promql.Scalar:
		return appendFloatPoint(b, v.T, v.V)
	case promql.String:
		b = append(b, '[')
		b = appendTimestamp(b, v.T)
		b = append(b, ',')
		b = appendJSONString(b, v.V)
		return append(b, ']')
	}
	return append(b, "null"...)
}

func appendMetric(b []byte, lset labels.Labels) []byte {
	b = append(b, `{"metric":{`...)
	first := true
	lset.Range(func(l labels.Label) {
		if !first {
			b = append(b, ',')
		}
		first = false
		b = appendJSONString(b, l.Name)
		b = append(b, ':')
		b = appendJSONString(b, l.Value)
	})
	return append(b, '}')
}

func appendFloatPoint(b []byte, t int64, f float64) []byte {
	b = append(b, '[')
	b = appendTimestamp(b, t)
	b = append(b, ',')
	b = appendFloat(b, f)
	return append(b, ']')
}

// appendTimestamp writes milliseconds as fractional Unix seconds, as Prometheus does
func appendTimestamp(b []byte, t int64) []byte {
	if t < 0 {
		b = append(b, '-')
		t = -t
	}
	b = strconv.AppendInt(b, t/1000, 10)
	if fraction := t % 1000; fraction != 0 {
		b = append(b, '.')
		if fraction < 100 {
			b = append(b, '0')
		}
		if fraction < 10 {
			b = append(b, '0')
		}
		b = strconv.AppendInt(b, fraction, 10)
	}
	return b
}

// appendFloat writes a quoted sample value as Prometheus does, exponent notation for very small or large values
func appendFloat(b []byte, f float64) []byte {
	b = append(b, '"')
	format := byte('f')
	if abs := math.Abs(f); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	b = strconv.AppendFloat(b, f, format, -1, 64)
	return append(b, '"')
}

// appendHistogramPoint writes a native histogram sample's non-empty buckets as boundary rule (0 lower
// exclusive, 1 upper exclusive, 2 both, 3 neither), boundaries and count
func appendHistogramPoint(b []byte, t int64, h *histogram.FloatHistogram) []byte {
	b = append(b, '[')
	b = appendTimestamp(b, t)
	b = append(b, `,{"count":`...)
	b = appendFloat(b, h.Count)
	b = append(b, `,"sum":`...)
	b = appendFloat(b, h.Sum)
	found := false
	for it := h.AllBucketIterator(); it.Next(); {
		bucket := it.At()
		if bucket.Count == 0 {
			continue
		}
		if !found {
			b = append(b, `,"buckets":[`...)
			found = true
		} else {
			b = append(b, ',')
		}
		rule := 2
		switch {
		case bucket.LowerInclusive && bucket.UpperInclusive:
			rule = 3
		case bucket.LowerInclusive:
			rule = 1
		case bucket.UpperInclusive:
			rule = 0
		}
		b = append(b, '[')
		b = strconv.AppendInt(b, int64(rule), 10)
		b = append(b, ',')
		b = appendFloat(b, bucket.Lower)
		b = append(b, ',')
		b = appendFloat(b, bucket.Upper)
		b = append(b, ',')
		b = appendFloat(b, bucket.Count)
		b = append(b, ']')
	}
	if found {
		b = append(b, ']')
	}
	return append(b, "}]"...)
}

// appendJSONString quotes s as encoding/json does, copying it directly when nothing in it needs escaping
func appendJSONString(b []byte, s string) []byte {
	for i := range len(s) {
		if c := s[i]; c < 0x20 || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' || c >= utf8.RuneSelf {
			// a string always marshals
			quoted, _ := json.Marshal(s)
			return append(b, quoted...)
		}
	}
	b = append(b, '"')
	b = append(b, s...)
	return append(b, '"')
}
