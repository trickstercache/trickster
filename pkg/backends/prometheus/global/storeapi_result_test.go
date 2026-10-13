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
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	resultTestWarning     = "store x is missing blocks"
	resultTestHintsType   = "type.googleapis.com/hintspb.SeriesResponseHints" // the Any a Thanos store's hints travel as
	resultTestAnyTypeURL  = 1                                                 // the type_url field of google.protobuf.Any
	resultTestRefused     = "alternative results"                             // what refusing a message of two results says
	resultTestUnknown     = 9                                                 // a SeriesResponse field number of no result
	resultTestUnsupported = "unsupported response field"                      // what refusing a field of no known result says
	resultTestUnexpected  = "unexpected response field"                       // what refusing a field of the wrong wire type says
	seriesThenWarning     = "series then warning"
	warningThenSeries     = "warning then series"
)

type resultTestMessages struct {
	series, other, warning, emptyWarning, hints, emptyHints []byte
}

func (s *testStore) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func newResultTestMessages(t *testing.T) resultTestMessages {
	t.Helper()
	anyHints := protowire.AppendString(protowire.AppendTag(nil, resultTestAnyTypeURL, protowire.BytesType), resultTestHintsType)
	return resultTestMessages{
		series: encodeTestSeries([]string{labels.MetricName, testMetric, testPod, "a"},
			spanned(encodeTestChunk(storeChunkXOR, xorData(t, pointsAt(110, 120)...), true), 110, 120)),
		other: encodeTestSeries([]string{labels.MetricName, testMetric, testPod, "b"},
			spanned(encodeTestChunk(storeChunkXOR, xorData(t, pointsAt(130)...), true), 130, 130)),
		warning:      encodeTestResponse(seriesResponseWarning, []byte(resultTestWarning)),
		emptyWarning: encodeTestResponse(seriesResponseWarning, nil),
		hints:        encodeTestResponse(seriesResponseHints, anyHints),
		emptyHints:   encodeTestResponse(seriesResponseHints, nil),
	}
}

func TestStoreAPIResponseResults(t *testing.T) {
	m := newResultTestMessages(t)
	for _, test := range []struct {
		name            string
		message         []byte
		series, samples int64
		warning         string
		charged         int64
	}{
		{name: "series", message: m.series, series: 1, samples: 2},
		{name: "warning", message: m.warning, warning: resultTestWarning, charged: warningBytes + int64(len(resultTestWarning))},
		{name: "hints", message: m.hints},
		{name: "empty hints", message: m.emptyHints},
		{name: "empty warning", message: m.emptyWarning},
		{name: "no result", message: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget := &Budget{}
			d := newDecoder(budget, decodeTestMint, decodeTestMaxt, errStoreAPI)
			warning, err := d.seriesResponse(test.message)
			require.NoError(t, err)
			require.Equal(t, test.warning, warning)
			require.Len(t, d.out, int(test.series))
			series, samples := budget.Fetched()
			require.Equal(t, test.series, series)
			require.Equal(t, test.samples, samples)
			if test.series == 0 {
				require.Equal(t, test.charged, d.limit-d.allowance, "only a warning kept is charged")
			}
		})
	}
}

func TestStoreAPIRefusesAlternativeResultsBeforeDecoding(t *testing.T) {
	m := newResultTestMessages(t)
	// a series no decoder could read, which a refused message must leave unread
	unreadable := encodeTestResponse(seriesResponseSeries, []byte{0xff})
	for name, message := range map[string][]byte{
		seriesThenWarning:            slices.Concat(m.series, m.warning),
		warningThenSeries:            slices.Concat(m.warning, m.series),
		"series then empty warning":  slices.Concat(m.series, m.emptyWarning),
		"empty warning then series":  slices.Concat(m.emptyWarning, m.series),
		"two series":                 slices.Concat(m.series, m.other),
		"one series twice":           slices.Concat(m.series, m.series),
		"series then hints":          slices.Concat(m.series, m.hints),
		"hints then series":          slices.Concat(m.hints, m.series),
		"series then empty hints":    slices.Concat(m.series, m.emptyHints),
		"empty hints then series":    slices.Concat(m.emptyHints, m.series),
		"two warnings":               slices.Concat(m.warning, m.warning),
		"warning then hints":         slices.Concat(m.warning, m.hints),
		"warning then an unreadable": slices.Concat(m.warning, unreadable),
	} {
		t.Run(name, func(t *testing.T) { requireRefusedUnread(t, message, resultTestRefused) })
	}
}

func TestStoreAPIRefusesOtherFieldsBeforeDecoding(t *testing.T) {
	m := newResultTestMessages(t)
	unknown := encodeTestResponse(resultTestUnknown, []byte{1})
	varint := protowire.AppendVarint(protowire.AppendTag(nil, seriesResponseSeries, protowire.VarintType), 1)
	for name, test := range map[string]struct {
		message []byte
		text    string
	}{
		"series then an unknown field":  {slices.Concat(m.series, unknown), resultTestUnsupported},
		"an unknown field then series":  {slices.Concat(unknown, m.series), resultTestUnsupported},
		"warning then an unknown field": {slices.Concat(m.warning, unknown), resultTestUnsupported},
		"series then a varint":          {slices.Concat(m.series, varint), resultTestUnexpected},
		"a varint then series":          {slices.Concat(varint, m.series), resultTestUnexpected},
		"warning then a varint":         {slices.Concat(m.warning, varint), resultTestUnexpected},
	} {
		t.Run(name, func(t *testing.T) { requireRefusedUnread(t, test.message, test.text) })
	}
}

func requireRefusedUnread(t *testing.T, message []byte, text string) {
	t.Helper()
	// a series decoded would exceed this budget, so the read refuses the message before counting any
	budget := &Budget{MaxSamples: 1}
	d := newDecoder(budget, decodeTestMint, decodeTestMaxt, errStoreAPI)
	warning, err := d.seriesResponse(message)
	require.ErrorIs(t, err, errStoreAPI)
	require.ErrorContains(t, err, text)
	require.NotErrorIs(t, err, ErrFetchLimit)
	require.Empty(t, warning)
	require.Empty(t, d.out)
	require.Nil(t, d.last)
	series, samples := budget.Fetched()
	require.Zero(t, series)
	require.Zero(t, samples)
	require.Equal(t, d.limit, d.allowance, "nothing of the message is charged")
}

func TestStoreAPIAlternativeResultsFailTheRead(t *testing.T) {
	m := newResultTestMessages(t)
	for name, mixed := range map[string][]byte{
		seriesThenWarning: slices.Concat(m.other, m.warning),
		warningThenSeries: slices.Concat(m.warning, m.other),
	} {
		t.Run(name, func(t *testing.T) {
			budget := &Budget{}
			store := &testStore{frames: [][]byte{m.series, mixed}}
			series, warnings, err := store.serve(t).Select(testContext(t, budget), decodeTestMint, decodeTestMaxt, nil)
			require.ErrorIs(t, err, errStoreAPI)
			require.ErrorContains(t, err, resultTestRefused)
			require.Nil(t, series, "not even the series before the ambiguous message is returned")
			require.Nil(t, warnings)
			require.Equal(t, 1, store.requestCount(), "a malformed answer is not retried")
		})
	}
}

func TestRawCacheKeepsNothingOfAlternativeResults(t *testing.T) {
	points := make([]testPoint, rawTestChunk)
	for i := range points {
		points[i] = testPoint{t: rawTestHour + int64(i)*rawTestStep, f: float64(i)}
	}
	good := xorChunk(t, points...)
	chunk := spanned(encodeTestChunk(storeChunkXOR, good.Data, true), good.MinTimeMs, good.MaxTimeMs)
	podA := encodeTestSeries([]string{labels.MetricName, testMetric, testPod, "a"}, chunk)
	podB := encodeTestSeries([]string{labels.MetricName, testMetric, testPod, "b"}, chunk)
	warning := encodeTestResponse(seriesResponseWarning, []byte(resultTestWarning))
	const mint, maxt = 0, 2*rawTestHour - 1
	for name, newCache := range rawTestCaches(t) {
		for order, mixed := range map[string][]byte{
			seriesThenWarning: slices.Concat(podB, warning),
			warningThenSeries: slices.Concat(warning, podB),
		} {
			t.Run(name+"/"+order, func(t *testing.T) {
				store := &testStore{frames: [][]byte{podA, mixed}}
				source := store.serve(t)
				source.ID = rawTestIdentity
				rc := newRawTestCache(newCache(), RawCacheOptions{})
				_, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), source, mint, maxt)
				require.ErrorIs(t, err, errStoreAPI)

				var failures []error
				failed := func(member string, err error) {
					require.Equal(t, rawTestMember, member)
					failures = append(failures, err)
				}
				for _, partial := range []bool{true, false} {
					q := NewQueryable([]Member{{Name: rawTestMember, Source: source}}, nil, merge.TSMDeduplicationChain,
						partial, failed).WithRawCache(rc.RawCache)
					qr, err := q.Querier(mint, maxt)
					require.NoError(t, err)
					set := qr.Select(teamRequest(rawTestTeamA), true, &storage.SelectHints{Start: mint, End: maxt},
						testMetricMatcher)
					require.False(t, set.Next(), "no series of the member is kept")
					if partial {
						require.NoError(t, set.Err())
						require.Len(t, set.Warnings(), 1)
						require.EqualError(t, set.Warnings().AsErrors()[0], ErrMemberUnavailable.Error()+": "+rawTestMember)
					} else {
						require.ErrorIs(t, set.Err(), ErrMemberUnavailable)
					}
					rc.Wait()
				}
				require.Len(t, failures, 2)
				for _, err := range failures {
					require.ErrorIs(t, err, errStoreAPI, "the member failed, rather than a fetch limit ending the query")
					require.NotErrorIs(t, err, ErrFetchLimit)
				}

				// once the store answers unambiguously, the next read reaches it, as nothing was kept in the meantime
				store.mu.Lock()
				store.frames = [][]byte{podA, podB}
				store.mu.Unlock()
				require.Equal(t, 3, store.requestCount())
				series, warnings, err := rc.selectAt(t, teamRequest(rawTestTeamA), source, mint, maxt)
				require.NoError(t, err)
				require.Empty(t, warnings)
				require.Len(t, series, 2)
				for _, s := range series {
					require.Equal(t, points, readPoints(t, s))
				}
				require.Equal(t, 4, store.requestCount())
				// what that read kept answers the next
				series, _, err = rc.selectAt(t, teamRequest(rawTestTeamA), source, mint, maxt)
				require.NoError(t, err)
				require.Len(t, series, 2)
				require.Equal(t, 4, store.requestCount())
			})
		}
	}
}
