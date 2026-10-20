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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/stretchr/testify/require"
)

const (
	limitTestBlob  = "blob"
	limitTestFirst = "a"
	limitTestNext  = "b"
	// limitTestEscapes is how many characters a label quoting to six times its length holds
	limitTestEscapes = 300
	// limitTestCeiling is the limit the growth tests encode under, far below what their results take
	limitTestCeiling = 64 << 10
	// limitTestAllocs bounds the bytes an encoding failing under limitTestCeiling may allocate, and
	// limitTestStringAllocs those of one failing on its only string, which is measured before it is encoded
	limitTestAllocs       = 1 << 20
	limitTestStringAllocs = 8 << 10
)

// limitTestHistogram returns a float histogram with buckets non-empty buckets on each side of zero
func limitTestHistogram(buckets int) *histogram.FloatHistogram {
	counts := make([]float64, buckets)
	for i := range counts {
		counts[i] = float64(i + 1)
	}
	return &histogram.FloatHistogram{
		Count: 1e6, Sum: -2.5e-9, ZeroThreshold: 0.001, ZeroCount: 3,
		PositiveSpans: []histogram.Span{{Offset: -2, Length: uint32(buckets)}}, PositiveBuckets: counts,
		NegativeSpans: []histogram.Span{{Offset: 1, Length: uint32(buckets)}}, NegativeBuckets: counts,
	}
}

// limitTestResults are results of every type and shape, with strings that need escaping
func limitTestResults() map[string]parser.Value {
	escaped := labels.FromStrings(labels.MetricName, testMetric, testPod, "a\"<b>\\\n\xff \u00fc\u2028")
	return map[string]parser.Value{
		"matrix": promql.Matrix{
			{Metric: escaped, Floats: []promql.FPoint{
				{T: -1005, F: 1e-7}, {T: 1500, F: math.NaN()}, {T: 2000, F: math.Inf(-1)}, {T: 3010, F: 1e22},
			}},
			{Metric: podLabels("h"), Histograms: []promql.HPoint{
				{T: 1000, H: limitTestHistogram(3)}, {T: 2000, H: &histogram.FloatHistogram{}},
			}},
			{
				Metric: labels.EmptyLabels(), Floats: []promql.FPoint{{T: 1000, F: 2}},
				Histograms: []promql.HPoint{{T: 2000, H: limitTestHistogram(1)}},
			},
		},
		"vector": promql.Vector{
			{Metric: escaped, T: 1000, F: -0.5},
			{Metric: podLabels("h"), T: 1000, H: limitTestHistogram(2)},
			{Metric: labels.EmptyLabels(), T: 2000, H: &histogram.FloatHistogram{Count: 1}},
		},
		// a label that can't fit is followed by one that can, and quotes to far more than its own length
		"long label first": promql.Vector{{Metric: labels.FromStrings(limitTestFirst, strings.Repeat("<", limitTestEscapes),
			limitTestNext, "y"), T: 1000, F: 1}},
		"scalar":        promql.Scalar{T: 1000, V: 1e22},
		"string":        promql.String{T: -1, V: "<\"hi\">"},
		"empty matrix":  promql.Matrix{},
		"empty vector":  promql.Vector{},
		"unknown value": limitTestValue{},
	}
}

// limitTestValue is a value of a type the API has no encoding for
type limitTestValue struct{}

func (limitTestValue) Type() parser.ValueType { return "unknown" }
func (limitTestValue) String() string         { return "" }

// encodeTestResult encodes value into a new buffer, under limit bytes when limit is positive
func encodeTestResult(value parser.Value, warnings, infos []string, limit int64) (resultEncoder, bool) {
	e := resultEncoder{limit: encodeLimit(limit)}
	return e, e.result(value, warnings, infos)
}

func TestWriteResultLimitIsExact(t *testing.T) {
	// a result fits a limit exactly when its encoding is no longer than the limit, and its buffer never grows
	// further past the limit than the slack
	warnings, infos := []string{"w<1>", "w2"}, []string{"\x00i"}
	for name, value := range limitTestResults() {
		for _, notes := range [][2][]string{{nil, nil}, {warnings, infos}} {
			t.Run(name+"/notes "+strconv.Itoa(len(notes[0])), func(t *testing.T) {
				full, ok := encodeTestResult(value, notes[0], notes[1], 0)
				require.True(t, ok)
				require.True(t, json.Valid(full.b), string(full.b))
				size := len(full.b)
				for limit := 1; limit <= size+2; limit++ {
					e, ok := encodeTestResult(value, notes[0], notes[1], int64(limit))
					require.Equal(t, limit >= size, ok, "limit %d of %d", limit, size)
					require.LessOrEqual(t, cap(e.b), limit+encodeSlack, "limit %d", limit)
					if ok {
						require.Equal(t, string(full.b), string(e.b))
					}
				}
			})
		}
	}
}

func TestWriteResultLimit(t *testing.T) {
	value := limitTestResults()["matrix"]
	full := writeTestResult(t, value, []string{"w"}, nil)

	w := httptest.NewRecorder()
	require.NoError(t, WriteResult(w, value, []string{"w"}, nil, int64(len(full))))
	require.Equal(t, full, w.Body.String(), "a result as large as its limit is written whole")

	w = httptest.NewRecorder()
	err := WriteResult(w, value, []string{"w"}, nil, int64(len(full)-1))
	require.ErrorIs(t, err, ErrResultTooLarge)
	require.Contains(t, err.Error(), strconv.Itoa(len(full)-1))
	require.Zero(t, w.Body.Len(), "nothing of a result too large is written")
	require.Empty(t, w.Header(), "nor any of its headers")
}

// growthTestCase is a result too large for its limit, with the bytes encoding it may allocate before failing
type growthTestCase struct {
	value    parser.Value
	warnings []string
	allocs   int
}

func TestWriteResultLimitBoundsGrowth(t *testing.T) {
	// a result failing its limit allocates nothing near its own size, however large its labels or samples, or
	// however much longer escaping makes them
	many := func(n int) []promql.FPoint {
		points := make([]promql.FPoint, n)
		for i := range points {
			points[i] = promql.FPoint{T: int64(i) * 15000, F: float64(i) / 3}
		}
		return points
	}
	histograms := make([]promql.HPoint, 2000)
	for i := range histograms {
		histograms[i] = promql.HPoint{T: int64(i) * 15000, H: limitTestHistogram(32)}
	}
	label := func(value string) growthTestCase {
		return growthTestCase{
			value:  promql.Vector{{Metric: labels.FromStrings(limitTestBlob, value), F: 1}},
			allocs: limitTestStringAllocs,
		}
	}
	warning := func(value string) growthTestCase {
		return growthTestCase{value: promql.Vector{}, warnings: []string{value}, allocs: limitTestStringAllocs}
	}
	cases := map[string]growthTestCase{
		"long label":                         label(strings.Repeat("x", 4<<20)),
		"long escaped label":                 label(strings.Repeat("<", 4<<20)),
		"escaped label within raw bound":     label(strings.Repeat("<", limitTestCeiling/4)),
		"escaped label just under the limit": label(strings.Repeat("<", limitTestCeiling-100)),
		"control label just under the limit": label(strings.Repeat("\x01", limitTestCeiling-100)),
		"invalid label just under the limit": label(strings.Repeat("\xff", limitTestCeiling-100)),
		"long warning":                       warning(strings.Repeat("\x01", 4<<20)),
		"warning just under the limit":       warning(strings.Repeat("&", limitTestCeiling-100)),
		"separator just under the limit":     warning(strings.Repeat("\u2028", (limitTestCeiling-100)/len("\u2028"))),
		"long label after samples": {value: promql.Matrix{
			{Metric: podLabels("a"), Floats: many(1000)},
			{Metric: labels.FromStrings(limitTestBlob, strings.Repeat("&", limitTestCeiling)), Floats: many(1)},
		}, allocs: limitTestAllocs},
		"many samples":    {value: promql.Matrix{{Metric: podLabels("a"), Floats: many(400_000)}}, allocs: limitTestAllocs},
		"many histograms": {value: promql.Matrix{{Metric: podLabels("a"), Histograms: histograms}}, allocs: limitTestAllocs},
		"many series": {value: promql.Vector(func() promql.Vector {
			v := make(promql.Vector, 100_000)
			for i := range v {
				v[i] = promql.Sample{Metric: podLabels(strconv.Itoa(i)), F: 1}
			}
			return v
		}()), allocs: limitTestAllocs},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			e, ok := encodeTestResult(tc.value, tc.warnings, nil, limitTestCeiling)
			runtime.ReadMemStats(&after)
			require.False(t, ok)
			require.LessOrEqual(t, cap(e.b), limitTestCeiling+encodeSlack)
			require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(tc.allocs))
		})
	}
}

func TestEncodeLimit(t *testing.T) {
	for _, none := range []int64{0, -1, math.MaxInt64} {
		require.Equal(t, math.MaxInt-encodeSlack, encodeLimit(none), none)
	}
	require.Equal(t, 100, encodeLimit(100))
}

// keepingWriter keeps a result's body as a BodyKeeper does, and fails the test if anything is written
type keepingWriter struct {
	*httptest.ResponseRecorder
	t    *testing.T
	body io.ReadCloser
	size int
}

func (k *keepingWriter) Write([]byte) (int, error) {
	k.t.Error("a kept body is never written")
	return 0, nil
}

func (k *keepingWriter) KeepBody(body io.ReadCloser, size int) {
	k.body, k.size = body, size
}

func TestWriteResultKeepsBody(t *testing.T) {
	value := limitTestResults()["vector"]
	full := writeTestResult(t, value, nil, []string{"i"})
	k := &keepingWriter{ResponseRecorder: httptest.NewRecorder(), t: t}
	require.NoError(t, WriteResult(k, value, nil, []string{"i"}, 0))
	require.Equal(t, http.StatusOK, k.Code)
	require.Equal(t, contentTypeJSON, k.Header().Get(headers.NameContentType))
	require.Equal(t, len(full), k.size)
	// read in small pieces, as a transfer does
	var got bytes.Buffer
	p := make([]byte, 7)
	for {
		n, err := k.body.Read(p)
		got.Write(p[:n])
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}
	require.Equal(t, full, got.String())
	require.Nil(t, k.body.(*resultBody).buf, "a body read to its end returns its buffer without being closed")
	n, err := k.body.Read(p)
	require.Zero(t, n)
	require.ErrorIs(t, err, io.EOF, "a body read to its end stays at its end")
	require.NoError(t, k.body.Close())
	require.NoError(t, k.body.Close(), "closing again is harmless")
	_, err = k.body.Read(p)
	require.ErrorIs(t, err, io.EOF)

	// a body closed before its end is never read again, as its buffer is reused
	k = &keepingWriter{ResponseRecorder: httptest.NewRecorder(), t: t}
	require.NoError(t, WriteResult(k, value, nil, []string{"i"}, 0))
	n, err = k.body.Read(p)
	require.Equal(t, len(p), n)
	require.NoError(t, err)
	require.NoError(t, k.body.Close())
	_, err = k.body.Read(p)
	require.ErrorIs(t, err, errBodyClosed)

	// a result too large is neither kept nor written
	k = &keepingWriter{ResponseRecorder: httptest.NewRecorder(), t: t}
	require.ErrorIs(t, WriteResult(k, value, nil, []string{"i"}, int64(len(full)-1)), ErrResultTooLarge)
	require.Nil(t, k.body)
}

func TestResultBodyEmpty(t *testing.T) {
	body := &resultBody{buf: responseBuffers.Get().(*[]byte)}
	*body.buf = (*body.buf)[:0]
	n, err := body.Read(make([]byte, 1))
	require.Zero(t, n)
	require.ErrorIs(t, err, io.EOF)
	require.Nil(t, body.buf, "an empty body is at its end at once")
	require.NoError(t, body.Close())
}

func TestResultBodyConcurrentClose(t *testing.T) {
	content := bytes.Repeat([]byte("x"), 1<<16)
	for range 50 {
		buf := responseBuffers.Get().(*[]byte)
		*buf = append((*buf)[:0], content...)
		body := &resultBody{buf: buf}
		var wg sync.WaitGroup
		wg.Go(func() {
			p := make([]byte, 512)
			for {
				if _, err := body.Read(p); err != nil {
					return
				}
			}
		})
		require.NoError(t, body.Close())
		wg.Wait()
		// the reader may have reached the end before the close, and the buffer is released either way
		_, err := body.Read(make([]byte, 1))
		if !errors.Is(err, io.EOF) {
			require.ErrorIs(t, err, errBodyClosed)
		}
		require.Nil(t, body.buf)
	}
}

// errorTestResponse is a decoded Prometheus API error response
type errorTestResponse struct {
	Status    string   `json:"status"`
	ErrorType string   `json:"errorType"`
	Error     string   `json:"error"`
	Warnings  []string `json:"warnings"`
}

// decodeTestError decodes an error response, requiring it to be complete JSON
func decodeTestError(t *testing.T, body []byte) errorTestResponse {
	t.Helper()
	require.True(t, json.Valid(body), string(body))
	var resp errorTestResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Equal(t, "error", resp.Status)
	return resp
}

func TestWriteErrorEscapes(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, http.StatusUnprocessableEntity, ErrorTypeExecution, ResultLimitError(10), []string{"<w>", "\xff"}, 0)
	resp := decodeTestError(t, w.Body.Bytes())
	require.Equal(t, ErrorTypeExecution, resp.ErrorType)
	require.Equal(t, ErrResultTooLarge.Error()+": more than 10 bytes", resp.Error)
	require.Equal(t, []string{"<w>", "\ufffd"}, resp.Warnings)
}

func TestWriteErrorFitsLimit(t *testing.T) {
	// an error keeps within its limit, or minErrorBytes when that is more: warnings that don't fit are left out, in
	// order, and a message that doesn't is cut short between characters
	msg := "a <message> \xff \u00fc \u2028 " + strings.Repeat("&", minErrorBytes/len(`\u0026`))
	warnings := []string{strings.Repeat("<", limitTestEscapes), "w1", strings.Repeat("\x01", 40), "w<2>"}
	write := func(limit int64) []byte {
		w := httptest.NewRecorder()
		WriteError(w, http.StatusServiceUnavailable, ErrorTypeUnavailable, errors.New(msg), warnings, limit)
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		require.Equal(t, contentTypeJSON, w.Header().Get(headers.NameContentType))
		return w.Body.Bytes()
	}
	full := write(0)
	want := decodeTestError(t, full)
	require.Len(t, want.Warnings, len(warnings))
	require.Greater(t, len(full), minErrorBytes+len(`\u0026`), "the whole error is past the floor")
	// a limit below the floor writes what the floor does, its message cut short rather than left empty
	floored := write(minErrorBytes)
	require.Equal(t, string(floored), string(write(1)))
	resp := decodeTestError(t, floored)
	require.NotEmpty(t, resp.Error)
	require.True(t, strings.HasPrefix(want.Error, resp.Error), resp.Error)
	var last errorTestResponse
	for limit := minErrorBytes - 2; limit <= len(full)+2; limit++ {
		body := write(int64(limit))
		resp := decodeTestError(t, body)
		require.Equal(t, ErrorTypeUnavailable, resp.ErrorType)
		require.LessOrEqual(t, len(body), max(limit, minErrorBytes), "limit %d", limit)
		require.True(t, strings.HasPrefix(want.Error, resp.Error), "limit %d: %q", limit, resp.Error)
		require.GreaterOrEqual(t, len(resp.Error), len(last.Error), "a higher limit never shortens the message")
		require.True(t, isSubsequence(resp.Warnings, want.Warnings), "limit %d: %q", limit, resp.Warnings)
		if len(resp.Warnings) > 0 {
			require.Equal(t, want.Error, resp.Error, "warnings are kept only once the message is whole")
		}
		if limit >= len(full) {
			require.Equal(t, string(full), string(body), "an error within its limit is written whole")
		}
		last = resp
	}
}

// isSubsequence reports whether sub holds values of all, in their order
func isSubsequence(sub, all []string) bool {
	for _, v := range all {
		if len(sub) > 0 && sub[0] == v {
			sub = sub[1:]
		}
	}
	return len(sub) == 0
}
