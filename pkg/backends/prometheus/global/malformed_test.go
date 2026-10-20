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
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

const (
	healthyMember   = "healthy"
	malformedMember = "malformed"
)

// shortChunks are chunk data shorter than their encoding's header, which the library would panic reading
var shortChunks = []struct {
	name string
	enc  chunkenc.Encoding
	data []byte
}{
	{name: "empty xor", enc: chunkenc.EncXOR, data: []byte{}},
	{name: "one byte xor", enc: chunkenc.EncXOR, data: []byte{0}},
	{name: "truncated xor2", enc: chunkenc.EncXOR2, data: []byte{0, 1}},
	{name: "empty histogram", enc: chunkenc.EncHistogram, data: nil},
	{name: "one byte histogram", enc: chunkenc.EncHistogram, data: []byte{0}},
	{name: "truncated histogram", enc: chunkenc.EncHistogram, data: []byte{0, 1}},
	{name: "truncated float histogram", enc: chunkenc.EncFloatHistogram, data: []byte{0, 1}},
}

var promChunkTypes = map[chunkenc.Encoding]prompb.Chunk_Encoding{
	chunkenc.EncXOR: prompb.Chunk_XOR, chunkenc.EncXOR2: prompb.Chunk_XOR2,
	chunkenc.EncHistogram: prompb.Chunk_HISTOGRAM, chunkenc.EncFloatHistogram: prompb.Chunk_FLOAT_HISTOGRAM,
}

var storeChunkTypes = map[chunkenc.Encoding]uint64{
	chunkenc.EncXOR: storeChunkXOR, chunkenc.EncHistogram: storeChunkHistogram,
	chunkenc.EncFloatHistogram: storeChunkFloatHistogram,
}

func TestNewChunkRejectsShortHeaders(t *testing.T) {
	for _, test := range shortChunks {
		t.Run(test.name, func(t *testing.T) {
			_, err := newChunk(test.enc, test.data)
			require.ErrorIs(t, err, errMalformedChunk)
		})
	}
	for _, enc := range []chunkenc.Encoding{
		chunkenc.EncXOR, chunkenc.EncXOR2, chunkenc.EncHistogram,
		chunkenc.EncFloatHistogram,
	} {
		size, ok := chunkHeaderSize(enc)
		require.True(t, ok)
		chk, err := newChunk(enc, make([]byte, size))
		require.NoError(t, err, enc.String())
		require.Zero(t, chk.NumSamples(), "a whole header of no samples is an empty chunk")
		require.Equal(t, chunkenc.ValNone, chk.Iterator(nil).Next())
	}
	_, err := newChunk(chunkenc.EncHistogramST, make([]byte, 8))
	require.ErrorIs(t, err, errMalformedChunk, "encodings no member sends are refused")
}

// malformedRemoteRead serves a CRC-correct frame holding a series whose one chunk is data of enc
func malformedRemoteRead(t *testing.T, enc chunkenc.Encoding, data []byte) *RemoteRead {
	t.Helper()
	return newTestRemoteRead(t, func(w http.ResponseWriter, _ *http.Request) {
		var body bytes.Buffer
		writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
			{Type: promChunkTypes[enc], Data: data},
		}})
		w.Header().Set(headers.NameContentType, contentTypeStreamed)
		_, _ = w.Write(body.Bytes())
	})
}

func TestRemoteReadMalformedChunks(t *testing.T) {
	for _, test := range shortChunks {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := malformedRemoteRead(t, test.enc, test.data).Select(testContext(t, nil), 0, 10, nil)
			require.ErrorIs(t, err, errRemoteRead)
			require.ErrorIs(t, err, errMalformedChunk)
		})
	}
}

func TestStoreAPIMalformedChunks(t *testing.T) {
	for _, test := range shortChunks {
		enc, ok := storeChunkTypes[test.enc]
		if !ok {
			continue
		}
		t.Run(test.name, func(t *testing.T) {
			store := &testStore{frames: [][]byte{encodeTestSeries([]string{labels.MetricName, testMetric},
				encodeTestChunk(enc, test.data, true))}}
			_, _, err := store.serve(t).Select(testContext(t, nil), 0, 10, nil)
			require.ErrorIs(t, err, errStoreAPI)
			require.ErrorIs(t, err, errMalformedChunk)
		})
	}
}

// panicSource panics on every read, as a decoding bug would
type panicSource struct{ MemorySource }

func (*panicSource) Select(context.Context, int64, int64, *storage.SelectHints,
	...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	panic("decoding bug")
}

func TestQueryableMemberFailuresFollowPolicy(t *testing.T) {
	healthy := &MemorySource{Series: []storage.Series{listSeries(podLabels("a"), testPoint{t: 1, f: 1})}}
	shortHistogram := shortChunks[len(shortChunks)-2]
	tests := []struct {
		name   string
		source func(t *testing.T) Source
		want   error
	}{
		{name: "malformed chunk", source: func(t *testing.T) Source {
			return malformedRemoteRead(t, shortHistogram.enc, shortHistogram.data)
		}, want: errMalformedChunk},
		{name: "panicking read", source: func(t *testing.T) Source {
			// the recovered panic's stack is logged, which the test needn't print
			prev := logger.Logger()
			logger.SetLogger(logging.NoopLogger())
			t.Cleanup(func() { logger.SetLogger(prev) })
			return &panicSource{}
		}, want: errSourcePanic},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			members := []Member{{Name: healthyMember, Source: healthy}, {Name: malformedMember, Source: test.source(t)}}
			var failures []error
			failed := func(member string, err error) {
				require.Equal(t, malformedMember, member)
				failures = append(failures, err)
			}
			partial := selectAll(t, NewQueryable(members, nil, merge.TSMDeduplicationPenalty, true, failed),
				testContext(t, nil), nil)
			require.True(t, partial.Next(), "the healthy member's series is kept")
			require.False(t, partial.Next())
			require.NoError(t, partial.Err())
			require.Len(t, partial.Warnings(), 1)
			require.EqualError(t, partial.Warnings().AsErrors()[0], ErrMemberUnavailable.Error()+": "+malformedMember)

			strict := selectAll(t, NewQueryable(members, nil, merge.TSMDeduplicationPenalty, false, failed),
				testContext(t, nil), nil)
			require.False(t, strict.Next())
			require.ErrorIs(t, strict.Err(), ErrMemberUnavailable)
			require.NotErrorIs(t, strict.Err(), test.want, "only the logs see why the member failed")

			require.Len(t, failures, 2)
			for _, err := range failures {
				require.ErrorIs(t, err, test.want)
			}
		})
	}
}

func TestStoreAPIReceiveLimitIsAFetchLimit(t *testing.T) {
	const receiveLimit = 32
	big := encodeTestSeries([]string{labels.MetricName, testMetric}, encodeTestChunk(storeChunkXOR,
		xorData(t, testPoint{t: 1, f: 1}, testPoint{t: 2, f: 2}, testPoint{t: 3, f: 3}, testPoint{t: 4, f: 4},
			testPoint{t: 5, f: 5}, testPoint{t: 6, f: 6}, testPoint{t: 7, f: 7}, testPoint{t: 8, f: 8}), true))
	require.Greater(t, len(big), receiveLimit)
	store := &testStore{frames: [][]byte{big}}
	budget := &Budget{MaxResponseBytes: receiveLimit}
	_, _, err := store.serve(t).Select(testContext(t, budget), 0, 10, nil)
	require.ErrorIs(t, err, ErrFetchLimit)
	require.NotErrorIs(t, err, errStoreAPI)

	require.ErrorIs(t, receiveError(errors.New("eof"), receiveLimit), errStoreAPI,
		"other receive failures are the member's")

	members := []Member{
		{Name: healthyMember, Source: &MemorySource{Series: []storage.Series{listSeries(podLabels("a"),
			testPoint{t: 1, f: 1})}}},
		{Name: malformedMember, Source: store.serve(t)},
	}
	set := selectAll(t, NewQueryable(members, nil, merge.TSMDeduplicationPenalty, true, nil),
		WithBudget(testContext(t, nil), budget), nil)
	require.False(t, set.Next())
	require.ErrorIs(t, set.Err(), ErrFetchLimit, "a limit fails even a partial response")
}

func TestRemoteReadFrameLimits(t *testing.T) {
	frames := func(t *testing.T) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			var body bytes.Buffer
			writeFrame(t, &body, &prompb.ChunkedSeries{Labels: testLabels("a"), Chunks: []prompb.Chunk{
				xorChunk(t, testPoint{t: 1, f: 1}, testPoint{t: 2, f: 2}),
			}})
			w.Header().Set(headers.NameContentType, contentTypeStreamed)
			_, _ = w.Write(body.Bytes())
		}
	}
	_, _, err := newTestRemoteRead(t, frames(t)).Select(testContext(t, &Budget{MaxResponseBytes: 16}), 0, 10, nil)
	require.ErrorIs(t, err, ErrFetchLimit)
	require.ErrorContains(t, err, "frame")
}
