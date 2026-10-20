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
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/klauspost/compress/snappy"
	"github.com/prometheus/prometheus/prompb"
	"github.com/stretchr/testify/require"
)

const (
	limitTestBody     = "abc"
	limitTestOver     = "d"
	limitTestMint     = 4 * rawTestHour
	limitTestMaxt     = 6 * rawTestHour
	limitTestStreamed = contentTypeStreamed + "; proto=prometheus.ChunkedReadResponse"
)

var errLimitTestReset = errors.New("connection reset by peer")

// readStep is one result a scriptedReader returns
type readStep struct {
	data string
	err  error
}

// scriptedReader returns its steps in order, each step's data over as many reads as it takes, then EOF
type scriptedReader struct{ steps []readStep }

func (r *scriptedReader) Read(p []byte) (int, error) {
	if len(r.steps) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.steps[0].data)
	if n < len(r.steps[0].data) {
		r.steps[0].data = r.steps[0].data[n:]
		return n, nil
	}
	err := r.steps[0].err
	r.steps = r.steps[1:]
	return n, err
}

func TestLimitedReaderAtItsLimit(t *testing.T) {
	tests := []struct {
		name  string
		steps []readStep
		// want is nil for a response read completely
		want error
	}{
		{name: "EOF after the limit", steps: []readStep{{limitTestBody, nil}, {"", io.EOF}}},
		{name: "EOF with the last byte", steps: []readStep{{limitTestBody, io.EOF}}},
		{name: "truncated with the last byte", want: io.ErrUnexpectedEOF, steps: []readStep{{limitTestBody, io.ErrUnexpectedEOF}}},
		{name: "transport error with the last byte", want: errLimitTestReset, steps: []readStep{{limitTestBody, errLimitTestReset}}},
		{name: "one byte over", steps: []readStep{{limitTestBody + limitTestOver, nil}}, want: ErrFetchLimit},
		{name: "one byte over with EOF", steps: []readStep{{limitTestBody, nil}, {limitTestOver, io.EOF}}, want: ErrFetchLimit},
		{
			name: "one byte over with a transport error", want: ErrFetchLimit,
			steps: []readStep{{limitTestBody, nil}, {limitTestOver, errLimitTestReset}},
		},
		{
			name: "truncated at the limit", want: io.ErrUnexpectedEOF,
			steps: []readStep{{limitTestBody, nil}, {"", io.ErrUnexpectedEOF}},
		},
		{
			name: "transport error at the limit", want: errLimitTestReset,
			steps: []readStep{{limitTestBody, nil}, {"", errLimitTestReset}},
		},
		{
			name: "nothing read, then a byte", want: ErrFetchLimit,
			steps: []readStep{{limitTestBody, nil}, {"", nil}, {limitTestOver, nil}},
		},
		{name: "nothing read, then EOF", steps: []readStep{{limitTestBody, nil}, {"", nil}, {"", io.EOF}}},
		{
			name: "nothing read, then a transport error", want: errLimitTestReset,
			steps: []readStep{{limitTestBody, nil}, {"", nil}, {"", errLimitTestReset}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := &limitedReader{r: &scriptedReader{steps: slices.Clone(test.steps)}, remaining: int64(len(limitTestBody))}
			data, err := io.ReadAll(r)
			require.Equal(t, limitTestBody, string(data), "what's within the limit is read either way")
			if test.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, test.want)
			if !errors.Is(test.want, ErrFetchLimit) {
				require.NotErrorIs(t, err, ErrFetchLimit, "a transport failure is not a response over its limit")
			}
		})
	}
	t.Run("nothing read is not the end", func(t *testing.T) {
		r := &limitedReader{
			r:         &scriptedReader{steps: []readStep{{limitTestBody, nil}, {"", nil}, {limitTestOver, nil}}},
			remaining: int64(len(limitTestBody)),
		}
		p := make([]byte, 8)
		n, err := r.Read(p)
		require.NoError(t, err)
		require.Equal(t, len(limitTestBody), n)
		n, err = r.Read(p)
		require.NoError(t, err)
		require.Zero(t, n)
		_, err = r.Read(p)
		require.ErrorIs(t, err, ErrFetchLimit)
	})
}

// limitTestFrames returns a streamed answer of two frames, one series each, and the length of the first
func limitTestFrames(t *testing.T) ([]byte, int) {
	t.Helper()
	var body bytes.Buffer
	writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
		xorChunk(t, testPoint{t: limitTestMint + rawTestStep, f: 1}, testPoint{t: limitTestMint + 2*rawTestStep, f: 2}),
	}})
	first := body.Len()
	writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("b"), Chunks: []prompb.Chunk{
		xorChunk(t, testPoint{t: limitTestMint + rawTestStep, f: 3}),
	}})
	return body.Bytes(), first
}

// limitTestSamples returns a samples answer, which decodes to no more bytes than it's sent in
func limitTestSamples(t *testing.T) []byte {
	t.Helper()
	data, err := (&prompb.ReadResponse{Results: []*prompb.QueryResult{{Timeseries: []*prompb.TimeSeries{{
		Labels: testLabels("a"), Samples: []prompb.Sample{{Value: 1, Timestamp: limitTestMint + rawTestStep}},
	}}}}}).Marshal()
	require.NoError(t, err)
	body := snappy.Encode(nil, data)
	size, err := snappy.DecodedLen(body)
	require.NoError(t, err)
	require.LessOrEqual(t, size, len(body), "the byte limit is reached reading the body, not decoding it")
	return body
}

// answer writes body as a response of the content type, declaring its length as declared bytes; a
// connection closed short of that length is truncated as the client reads it
func answer(w http.ResponseWriter, contentType string, body []byte, declared int) {
	w.Header().Set(headers.NameContentType, contentType)
	w.Header().Set(headers.NameContentLength, strconv.Itoa(declared))
	_, _ = w.Write(body)
}

func TestRemoteReadTruncatedAtItsLimit(t *testing.T) {
	frames, first := limitTestFrames(t)
	samples := limitTestSamples(t)
	tests := []struct {
		name, contentType string
		body              []byte
		declared          int
		limit             int64
		// want is nil for a complete answer of one series
		want error
	}{
		{
			name: "frames complete at the limit", contentType: limitTestStreamed,
			body: frames[:first], declared: first, limit: int64(first),
		},
		{
			name: "frames one byte over the limit", contentType: limitTestStreamed,
			body: frames[:first], declared: first, limit: int64(first) - 1, want: ErrFetchLimit,
		},
		{
			name: "frames truncated at the limit", contentType: limitTestStreamed,
			body: frames[:first], declared: len(frames), limit: int64(first), want: io.ErrUnexpectedEOF,
		},
		{
			name: "samples complete at the limit", contentType: contentTypeProtobuf,
			body: samples, declared: len(samples), limit: int64(len(samples)),
		},
		{
			name: "samples truncated at the limit", contentType: contentTypeProtobuf,
			body: samples, declared: len(samples) + 1, limit: int64(len(samples)), want: io.ErrUnexpectedEOF,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := newTestRemoteRead(t, func(w http.ResponseWriter, _ *http.Request) {
				answer(w, test.contentType, test.body, test.declared)
			})
			series, _, err := source.Select(testContext(t, &Budget{MaxResponseBytes: test.limit}),
				limitTestMint, limitTestMaxt, nil)
			if test.want == nil {
				require.NoError(t, err)
				require.Len(t, series, 1)
				return
			}
			require.ErrorIs(t, err, test.want)
			if !errors.Is(test.want, ErrFetchLimit) {
				require.ErrorIs(t, err, errRemoteRead, "a truncated answer is the member's failure")
				require.NotErrorIs(t, err, ErrFetchLimit)
			}
			require.Nil(t, series)
		})
	}
}

func TestRawCacheDoesNotKeepTruncatedReads(t *testing.T) {
	frames, first := limitTestFrames(t)
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			// the member's first answer is cut off after its first frame; later ones end there completely
			var requests atomic.Int32
			source := newTestRemoteRead(t, func(w http.ResponseWriter, _ *http.Request) {
				declared := first
				if requests.Add(1) == 1 {
					declared = len(frames)
				}
				answer(w, limitTestStreamed, frames[:first], declared)
			})
			source.ID = rawTestIdentity
			rc := newRawTestCache(newCache(), RawCacheOptions{MaxResponseBytes: int64(first)})
			ctx := teamRequest(rawTestTeamA)

			_, _, err := rc.selectAt(t, ctx, source, limitTestMint, limitTestMaxt)
			require.ErrorIs(t, err, errRemoteRead)
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			require.Equal(t, int32(1), requests.Load())

			series, _, err := rc.selectAt(t, ctx, source, limitTestMint, limitTestMaxt)
			require.NoError(t, err)
			require.Len(t, series, 1)
			require.Equal(t, int32(2), requests.Load(), "nothing of the truncated answer was kept")

			cached, _, err := rc.selectAt(t, ctx, source, limitTestMint, limitTestMaxt)
			require.NoError(t, err)
			require.Equal(t, int32(2), requests.Load(), "a complete answer is kept")
			require.Equal(t, pointsOf(t, series), pointsOf(t, cached))
		})
	}
}
