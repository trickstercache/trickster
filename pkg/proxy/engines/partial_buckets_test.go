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

package engines

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/mocks/bucketsim"
	"github.com/trickstercache/trickster/v2/pkg/testutil/stepwindow"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const (
	pbStep  = time.Minute
	pbQuery = "bucket_rows"
	// 7s and 13s past a minute sit inside a bucket, so both edges of a range at them are partial
	pbStartSkew = 7 * time.Second
	pbEndSkew   = 13 * time.Second
	pbBuckets   = 30
	pbWhole     = "60"
	pbKeyParam  = "instantKey"
)

type upstreamRanges struct {
	mu     sync.Mutex
	ranges []string
}

func (u *upstreamRanges) record(r *http.Request) {
	// each request's range as start-end in Unix seconds, with no end for an open range
	v := r.URL.Query()
	u.mu.Lock()
	u.ranges = append(u.ranges, v.Get(upStart)+"-"+v.Get(upEnd))
	u.mu.Unlock()
}

func (u *upstreamRanges) take() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := u.ranges
	u.ranges = nil
	return out
}

type bucketHarness struct {
	client    *TestClient
	r         *http.Request
	up        *upstreamRanges
	transport *gatedTransport
	key       string
	query     string
	step      time.Duration
}

func newBucketHarness(t *testing.T, mode timeseries.StepAlignment) *bucketHarness {
	t.Helper()
	// a delta proxy cache over bucketsim, whose buckets count their rows inside the requested range
	ts, _, r, rsc, err := setupTestHarnessDPC()
	require.NoError(t, err)
	t.Cleanup(func() { closeTestHarness(ts, r) })
	client := rsc.BackendClient.(*TestClient)
	client.bucketRanges, client.sampleModel = true, timeseries.SampleModelBucket
	client.stepAlignments, client.stepAlignment = timeseries.StepAlignmentAll, timeseries.StepAlignmentDrop
	o := rsc.BackendOptions
	o.StepAlignment = mode
	o.VolatileWindow, o.VolatileWindowPoints = 0, 0
	h := &bucketHarness{
		client: client, r: r, up: &upstreamRanges{}, query: pbQuery, step: pbStep,
		key: strings.ReplaceAll(t.Name(), "/", "-"),
	}
	inner := o.HTTPClient.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	open := make(chan struct{})
	close(open)
	h.transport = &gatedTransport{inner: inner, gate: open, hits: &atomic.Int64{}, seen: h.up.record}
	o.HTTPClient.Transport = h.transport
	return h
}

func (h *bucketHarness) request(start, end time.Time, override timeseries.StepAlignment) *http.Request {
	q := url.Values{
		pbKeyParam: {h.key}, upQuery: {h.query},
		upStep: {strconv.Itoa(int(h.step.Seconds()))}, upStart: {unixString(start)},
	}
	if !end.IsZero() {
		q.Set(upEnd, unixString(end))
	}
	r, _ := request.Clone(h.r)
	r.URL.Path, r.URL.RawQuery = bucketsim.PathQueryRange, q.Encode()
	if override != 0 {
		r = r.WithContext(tctx.WithStepAlignment(r.Context(), override))
	}
	return r
}

func (h *bucketHarness) serve(start, end time.Time, override timeseries.StepAlignment) dpcResponse {
	return serveDPC(h.client, h.request(start, end, override))
}

func unixString(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}

func upstreamRange(start, end time.Time) string {
	if end.IsZero() {
		return unixString(start) + "-"
	}
	return unixString(start) + "-" + unixString(end)
}

func bucketValues(t *testing.T, body string) map[int64]string {
	t.Helper()
	// the labels, which must come in order, and their row counts
	var doc struct {
		Data struct {
			Result []struct {
				Values [][2]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &doc), body)
	out := map[int64]string{}
	for _, res := range doc.Data.Result {
		prev := int64(-1 << 63)
		for _, v := range res.Values {
			f, _ := v[0].(float64)
			label := int64(f)
			require.Greater(t, label, prev, "labels out of order: %s", body)
			prev = label
			out[label], _ = v[1].(string)
		}
	}
	return out
}

func datasetValues(t *testing.T, ts timeseries.Timeseries) map[int64]string {
	t.Helper()
	ds, ok := ts.(*dataset.DataSet)
	require.True(t, ok, "%T", ts)
	out := map[int64]string{}
	for _, res := range ds.Results {
		for _, s := range res.SeriesList {
			// the points are read across the series' parts, which a merge may have added
			pts := s.FlatPoints()
			for i, p := range pts {
				// merged points are served in order, whatever the marshaler does with them
				require.True(t, i == 0 || pts[i-1].Epoch < p.Epoch, "points out of order")
				out[time.Unix(0, int64(p.Epoch)).Unix()], _ = p.Values[0].(string)
			}
		}
	}
	return out
}

func wholeBuckets(from, to time.Time, step time.Duration) map[int64]string {
	out := map[int64]string{}
	for t := from; !t.After(to); t = t.Add(step) {
		out[t.Unix()] = strconv.Itoa(int(step.Seconds()))
	}
	return out
}

func requirePartialBuckets(t *testing.T, resp dpcResponse, want ...headers.PartialBucketResult) {
	t.Helper()
	if len(want) == 0 {
		require.NotContains(t, resp.header.Get(headers.NameTricksterResult), keys.PartialBuckets)
		return
	}
	require.NoError(t, testResultHeaderPartMatch(resp.header, map[string]string{
		keys.PartialBuckets: "[" + headers.PartialBucketsString(want) + "]",
	}))
}

func pbResult(start, end time.Time, edge timeseries.BucketEdge, st string) headers.PartialBucketResult {
	return headers.PartialBucketResult{Extent: timeseries.Extent{Start: start, End: end}, Edge: edge, Status: st}
}

func TestPartialBucketsByMode(t *testing.T) {
	base := time.Now().Add(-6 * time.Hour).Truncate(pbStep)
	start, end := base.Add(pbStartSkew), base.Add(pbBuckets*pbStep+pbEndSkew)
	floorS, ceilS, floorE := base, base.Add(pbStep), base.Add(pbBuckets*pbStep)
	last := floorE.Add(-pbStep)
	tests := []struct {
		mode             timeseries.StepAlignment
		first            time.Time
		startPB, endPB   bool
		interiorFromEdge bool
	}{
		{timeseries.StepAlignmentTruncate, floorS, false, false, true},
		{timeseries.StepAlignmentDrop, ceilS, false, false, false},
		{timeseries.StepAlignmentPartial, ceilS, true, true, false},
		{timeseries.StepAlignmentPartialStart, ceilS, true, false, false},
		{timeseries.StepAlignmentPartialEnd, floorS, false, true, true},
	}
	for _, test := range tests {
		t.Run(test.mode.String(), func(t *testing.T) {
			h := newBucketHarness(t, test.mode)
			// the partial buckets of the client's raw edges, as served and as fetched
			expect := func(shift time.Duration, st string) (map[int64]string, []string,
				[]headers.PartialBucketResult,
			) {
				values := wholeBuckets(test.first, last, pbStep)
				var ranges []string
				var pbs []headers.PartialBucketResult
				s, e := start.Add(shift), end.Add(shift)
				if test.startPB {
					values[floorS.Unix()] = strconv.Itoa(int(ceilS.Sub(s).Seconds()))
					ranges = append(ranges, upstreamRange(s, ceilS))
					pbs = append(pbs, pbResult(s, ceilS, timeseries.BucketEdgeStart, st))
				}
				if test.endPB {
					values[floorE.Unix()] = strconv.Itoa(int(e.Sub(floorE).Seconds()))
					ranges = append(ranges, upstreamRange(floorE, e))
					pbs = append(pbs, pbResult(floorE, e, timeseries.BucketEdgeEnd, st))
				}
				return values, ranges, pbs
			}

			values, ranges, pbs := expect(0, status.StatusKeyMiss)
			resp := h.serve(start, end, 0)
			require.Zero(t, h.client.liveFetches.Load(), "no bucket of a past range is live")
			requireResult(t, resp, engineDPC, status.StatusKeyMiss)
			require.Equal(t, values, bucketValues(t, resp.body))
			require.ElementsMatch(t, append(ranges, upstreamRange(test.first, floorE)), h.up.take())
			requirePartialBuckets(t, resp, pbs...)

			t.Run("an identical request is answered from both caches", func(t *testing.T) {
				values, _, pbs := expect(0, status.StatusHit)
				resp := h.serve(start, end, 0)
				requireResult(t, resp, engineDPC, status.StatusHit)
				require.Equal(t, values, bucketValues(t, resp.body))
				require.Empty(t, h.up.take())
				requirePartialBuckets(t, resp, pbs...)
			})
			t.Run("edges a second later fetch only their own partial buckets", func(t *testing.T) {
				values, ranges, pbs := expect(time.Second, status.StatusKeyMiss)
				resp := h.serve(start.Add(time.Second), end.Add(time.Second), 0)
				requireResult(t, resp, engineDPC, status.StatusHit)
				require.Equal(t, values, bucketValues(t, resp.body))
				require.ElementsMatch(t, ranges, h.up.take())
				requirePartialBuckets(t, resp, pbs...)
			})
			t.Run("the cache holds whole buckets only", func(t *testing.T) {
				// truncate over one more bucket reads both edge buckets whole, so a partial bucket in
				// the cache would show its partial count here
				resp := h.serve(start, end.Add(pbStep), timeseries.StepAlignmentTruncate)
				got := bucketValues(t, resp.body)
				require.Equal(t, pbWhole, got[floorS.Unix()])
				require.Equal(t, pbWhole, got[floorE.Unix()])
				fetched := []string{upstreamRange(floorE, floorE.Add(pbStep))}
				if !test.interiorFromEdge {
					fetched = append(fetched, upstreamRange(floorS, ceilS))
				}
				require.ElementsMatch(t, fetched, h.up.take())
			})
		})
	}
}

func TestLivePartialBucket(t *testing.T) {
	const step = time.Hour
	type live struct {
		closed, again, open, truncated dpcResponse
		start, end, bucket             time.Time
		ranges                         [][]string
	}
	h := newBucketHarness(t, timeseries.StepAlignmentPartialEnd)
	h.step = step
	res, ok := stepwindow.Retry(step, 3, func(attempt int, now time.Time) live {
		h.key = "live-" + strconv.Itoa(attempt)
		h.client.liveFetches.Store(0)
		l := live{end: now.Truncate(time.Second), bucket: now.Truncate(step)}
		l.start = l.bucket.Add(-5 * step).Add(pbStartSkew)
		l.closed = h.serve(l.start, l.end, 0)
		l.ranges = append(l.ranges, h.up.take())
		l.again = h.serve(l.start, l.end, 0)
		l.ranges = append(l.ranges, h.up.take())
		l.open = h.serve(l.start, time.Time{}, 0)
		l.ranges = append(l.ranges, h.up.take())
		l.truncated = h.serve(l.start, l.end, timeseries.StepAlignmentTruncate)
		return l
	})
	if !ok {
		t.Fatal("every attempt straddled a bucket boundary")
	}
	// the closed, repeated and open requests each fetch their end bucket as the live one
	require.Equal(t, int64(3), h.client.liveFetches.Load())
	liveRows := strconv.Itoa(int(res.end.Sub(res.bucket).Seconds()))
	// the live bucket runs to the client's end, and never past it
	require.Equal(t, liveRows, bucketValues(t, res.closed.body)[res.bucket.Unix()])
	require.Contains(t, res.ranges[0], upstreamRange(res.bucket, res.end))
	requirePartialBuckets(t, res.closed, pbResult(res.bucket, res.end, timeseries.BucketEdgeEnd,
		status.StatusKeyMiss))
	// a second request in the same bucket, within partial_bucket_ttl, is answered by the object cache
	require.Empty(t, res.ranges[1])
	require.Equal(t, liveRows, bucketValues(t, res.again.body)[res.bucket.Unix()])
	requirePartialBuckets(t, res.again, pbResult(res.bucket, res.end, timeseries.BucketEdgeEnd,
		status.StatusHit))
	// an open end is sent open, so the bucket's viewers share its entry
	require.Equal(t, []string{upstreamRange(res.bucket, time.Time{})}, res.ranges[2])
	// truncate never shows the bucket that is still filling
	require.NotContains(t, bucketValues(t, res.truncated.body), res.bucket.Unix())
	requirePartialBuckets(t, res.truncated)
}

func TestPartialBucketTTL(t *testing.T) {
	h := newBucketHarness(t, timeseries.StepAlignmentPartialStart)
	h.client.Configuration().PartialBucketTTL = 1_000_000_000
	base := time.Now().Add(-6 * time.Hour).Truncate(pbStep)
	start, end := base.Add(pbStartSkew), base.Add(pbBuckets*pbStep)
	edge := pbResult(start, base.Add(pbStep), timeseries.BucketEdgeStart, status.StatusKeyMiss)
	requirePartialBuckets(t, h.serve(start, end, 0), edge)
	edge.Status = status.StatusHit
	requirePartialBuckets(t, h.serve(start, end, 0), edge)
	time.Sleep(1100 * time.Millisecond)
	edge.Status = status.StatusKeyMiss
	requirePartialBuckets(t, h.serve(start, end, 0), edge)
}

func TestConcurrentCallersShareTheInteriorButNotTheirPartialBuckets(t *testing.T) {
	h := newBucketHarness(t, timeseries.StepAlignmentPartialStart)
	base := time.Now().Add(-6 * time.Hour).Truncate(pbStep)
	end := base.Add(pbBuckets * pbStep)
	skews := []time.Duration{pbStartSkew, 23 * time.Second}
	gate := make(chan struct{})
	h.transport.gate = gate
	var wg sync.WaitGroup
	resps := make([]dpcResponse, len(skews))
	for i, skew := range skews {
		req := h.request(base.Add(skew), end, 0)
		wg.Go(func() { resps[i] = serveDPC(h.client, req) })
	}
	// both callers reach the interior's singleflight while the origin is held
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	interior := upstreamRange(base.Add(pbStep), end)
	var interiors int
	for _, rng := range h.up.take() {
		if rng == interior {
			interiors++
		}
	}
	require.Equal(t, 1, interiors)
	statuses := map[string]int{}
	for i, resp := range resps {
		statuses[parseStatus(resp.header)]++
		want := strconv.Itoa(int((pbStep - skews[i]).Seconds()))
		require.Equal(t, want, bucketValues(t, resp.body)[base.Unix()], "caller %d", i)
	}
	require.Equal(t, map[string]int{status.StatusKeyMiss: 1, status.StatusProxyHit: 1}, statuses)
}

func parseStatus(h http.Header) string {
	for part := range strings.SplitSeq(h.Get(headers.NameTricksterResult), "; ") {
		if v, ok := strings.CutPrefix(part, keys.Status+"="); ok {
			return v
		}
	}
	return ""
}

func TestPartialBucketsReachMergeMembersAndTransformers(t *testing.T) {
	h := newBucketHarness(t, timeseries.StepAlignmentPartial)
	base := time.Now().Add(-6 * time.Hour).Truncate(pbStep)
	start, end := base.Add(pbStartSkew), base.Add(pbBuckets*pbStep+pbEndSkew)
	for _, member := range []bool{true, false} {
		req := h.request(start, end, 0)
		rsc := request.GetResources(req)
		var transformed timeseries.Timeseries
		if member {
			rsc.IsMergeMember = true
		} else {
			rsc.TSTransformer = func(ts timeseries.Timeseries) { transformed = ts }
		}
		serveDPC(h.client, req)
		ts := transformed
		if member {
			ts = rsc.TS
		}
		got := datasetValues(t, ts)
		require.Equal(t, "53", got[base.Unix()], "member %t", member)
		require.Equal(t, "13", got[base.Add(pbBuckets*pbStep).Unix()], "member %t", member)
	}
}

func TestPartialBucketFetchErrorLeavesTheBucketOut(t *testing.T) {
	base := time.Now().Add(-6 * time.Hour).Truncate(pbStep)
	start, end := base.Add(pbStartSkew), base.Add(pbBuckets*pbStep+pbEndSkew)
	// at a limit of one, a failed bucket that kept its slot would hold the interior forever
	for _, limit := range []int{bo.DefaultFetchConcurrencyLimit, 1} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			h := newBucketHarness(t, timeseries.StepAlignmentPartial)
			h.query = pbQuery + "{" + bucketsim.ModFailUnaligned + "}"
			o := h.client.Configuration()
			o.FetchConcurrencyLimit = limit
			failed := metrics.ProxyPartialBucketFetches.WithLabelValues(o.Name, o.Provider,
				timeseries.BucketEdgeStart.String(), statusErr)
			before := testutil.ToFloat64(failed)
			resp := h.serve(start, end, 0)
			// the interior is answered as ever, and each failed bucket is reported and left out
			requireResult(t, resp, engineDPC, status.StatusKeyMiss)
			require.Equal(t, wholeBuckets(base.Add(pbStep), base.Add((pbBuckets-1)*pbStep), pbStep),
				bucketValues(t, resp.body))
			requirePartialBuckets(t, resp,
				pbResult(start, base.Add(pbStep), timeseries.BucketEdgeStart, statusErr),
				pbResult(base.Add(pbBuckets*pbStep), end, timeseries.BucketEdgeEnd, statusErr))
			require.Equal(t, before+1, testutil.ToFloat64(failed))
		})
	}
}

func TestRangeWithoutACompleteBucketIsServedAsSent(t *testing.T) {
	for _, mode := range []timeseries.StepAlignment{
		timeseries.StepAlignmentTruncate, timeseries.StepAlignmentDrop, timeseries.StepAlignmentPartial,
	} {
		t.Run(mode.String(), func(t *testing.T) {
			h := newBucketHarness(t, mode)
			base := time.Now().Add(-6 * time.Hour).Truncate(pbStep)
			start, end := base.Add(pbStartSkew), base.Add(50*time.Second)
			req := h.request(start, end, 0)
			resp := serveDPC(h.client, req)
			// every bucket is partial, so the origin's own answer is kept in the object cache
			requireResult(t, resp, engineOPC, status.StatusKeyMiss)
			require.Equal(t, map[int64]string{base.Unix(): "43"}, bucketValues(t, resp.body))
			require.Equal(t, []string{upstreamRange(start, end)}, h.up.take())
			require.Equal(t, time.Duration(h.client.Configuration().PartialBucketTTL),
				request.GetResources(req).AlternateCacheTTL)
		})
	}
}

func TestKeepLabel(t *testing.T) {
	label := time.Unix(1800, 0)
	point := func(sec int64) dataset.Point {
		return dataset.Point{Epoch: epoch.Epoch(sec * int64(time.Second)), Size: 32, Values: []any{"1"}}
	}
	ds := &dataset.DataSet{Results: dataset.Results{{SeriesList: dataset.SeriesList{
		{Points: dataset.Points{point(1740), point(1800), point(1860)}},
		nil,
	}}, nil}}
	// an origin that answers a bucket with its neighbors has only the bucket's own row merged
	keepLabel(ds, label)
	require.Equal(t, dataset.Points{point(1800)}, ds.Results[0].SeriesList[0].Points)
	require.Equal(t, dataset.Points{point(1800)}.Size(), ds.Results[0].SeriesList[0].PointSize)
	require.Equal(t, timeseries.ExtentList{{Start: label, End: label}}, ds.ExtentList)
}

func TestOnlyTheInstantModelFetchesALivePoint(t *testing.T) {
	ts, _, r, rsc, err := setupTestHarnessDPC()
	require.NoError(t, err)
	defer closeTestHarness(ts, r)
	client := rsc.BackendClient.(*TestClient)
	client.sampleModel = timeseries.SampleModelStored
	client.stepAlignments = timeseries.StepAlignmentTruncate | timeseries.StepAlignmentPartialEnd
	o := rsc.BackendOptions
	o.StepAlignment, o.FastForwardDisable = timeseries.StepAlignmentPartialEnd, false
	const step = 300 * time.Second
	resp, ok := stepwindow.Retry(step, 3, func(attempt int, now time.Time) dpcResponse {
		r.URL.Path = "/prometheus/api/v1/query_range"
		r.URL.RawQuery = "instantKey=stored-" + strconv.Itoa(attempt) + "&step=300&start=" +
			unixString(now.Add(-time.Hour)) + "&end=" + unixString(now) + "&query=" + queryReturnsOKNoLatency
		return serveDPC(client, r)
	})
	require.True(t, ok)
	require.NoError(t, testResultHeaderPartMatch(resp.header, map[string]string{keys.FFStatus: statusOff}))
}

func TestMergeIntoKeepsEachBucketsOwnRow(t *testing.T) {
	header := dataset.SeriesHeader{Name: pbQuery}
	header.CalculateHash()
	series := func(secs ...int64) *dataset.DataSet {
		pts := make(dataset.Points, len(secs))
		for i, sec := range secs {
			pts[i] = dataset.Point{
				Epoch: epoch.Epoch(sec * int64(time.Second)), Size: 32, Values: []any{strconv.FormatInt(sec, 10)},
			}
		}
		return &dataset.DataSet{Results: dataset.Results{{SeriesList: dataset.SeriesList{
			{Header: header, Points: pts},
		}}}}
	}
	rts := series(120, 180)
	pf := &partialFetches{cancel: func() {}, count: 2}
	// an origin that answers each edge bucket with a neighbor too
	pf.fetched[0] = partialFetch{
		pb: timeseries.PartialBucket{Label: time.Unix(60, 0), Lower: time.Unix(67, 0), Upper: time.Unix(120, 0)},
		ts: series(0, 60), status: status.LookupStatusKeyMiss,
	}
	pf.fetched[1] = partialFetch{
		pb: timeseries.PartialBucket{
			Label: time.Unix(240, 0), Lower: time.Unix(240, 0), Upper: time.Unix(253, 0),
			Edge: timeseries.BucketEdgeEnd,
		},
		ts: series(240, 300), status: status.LookupStatusHit,
	}
	pbs, values := pf.mergeInto(rts, nil, time.Now(), nil, nil)
	require.Equal(t, map[int64]string{60: "60", 120: "120", 180: "180", 240: "240"}, datasetValues(t, rts))
	require.Equal(t, int64(2), values)
	require.Equal(t, []headers.PartialBucketResult{
		pbResult(time.Unix(67, 0), time.Unix(120, 0), timeseries.BucketEdgeStart, status.StatusKeyMiss),
		pbResult(time.Unix(240, 0), time.Unix(253, 0), timeseries.BucketEdgeEnd, status.StatusHit),
	}, pbs)
}

func TestUncachedRequestsStillServeTheirPartialBuckets(t *testing.T) {
	h := newBucketHarness(t, timeseries.StepAlignmentPartial)
	base := time.Now().Add(-6 * time.Hour).Truncate(pbStep)
	start, end := base.Add(pbStartSkew), base.Add(pbBuckets*pbStep+pbEndSkew)
	req := h.request(start, end, 0)
	req.Header.Set(headers.NameCacheControl, headers.ValueNoCache)
	resp := serveDPC(h.client, req)
	requireResult(t, resp, engineDPC, status.StatusPurge)
	got := bucketValues(t, resp.body)
	require.Equal(t, "53", got[base.Unix()])
	require.Equal(t, "13", got[base.Add(pbBuckets*pbStep).Unix()])
	requirePartialBuckets(t, resp,
		pbResult(start, base.Add(pbStep), timeseries.BucketEdgeStart, status.StatusKeyMiss),
		pbResult(base.Add(pbBuckets*pbStep), end, timeseries.BucketEdgeEnd, status.StatusKeyMiss))
}

type peakTransport struct {
	inner          http.RoundTripper
	gate           chan struct{}
	inflight, peak atomic.Int64
}

func (p *peakTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	n := p.inflight.Add(1)
	defer p.inflight.Add(-1)
	for old := p.peak.Load(); n > old && !p.peak.CompareAndSwap(old, n); old = p.peak.Load() {
	}
	<-p.gate
	return p.inner.RoundTrip(req)
}

func TestPartialBucketsShareTheFetchConcurrencyLimit(t *testing.T) {
	base := time.Now().Add(-6 * time.Hour).Truncate(pbStep)
	start, end := base.Add(pbStartSkew), base.Add(pbBuckets*pbStep+pbEndSkew)
	const later = 3 * pbStep
	tests := []struct {
		name    string
		status  string
		prime   bool
		noCache bool
	}{
		{"key miss", status.StatusKeyMiss, false, false},
		{"partial hit", status.StatusPartialHit, true, false},
		{"no-cache", status.StatusPurge, false, true},
	}
	for _, test := range tests {
		// two partial buckets and one interior fetch are three origin calls
		for _, limit := range []int64{1, 2, 3} {
			t.Run(test.name+" at "+strconv.FormatInt(limit, 10), func(t *testing.T) {
				h := newBucketHarness(t, timeseries.StepAlignmentPartial)
				o := h.client.Configuration()
				o.FetchConcurrencyLimit = int(limit)
				s, e, b := start, end, base
				if test.prime {
					// the later range's interior needs only the buckets past the cached ones
					h.serve(s, e, 0)
					s, e, b = s.Add(later), e.Add(later), b.Add(later)
				}
				pt := &peakTransport{inner: o.HTTPClient.Transport, gate: make(chan struct{})}
				o.HTTPClient.Transport = pt
				req := h.request(s, e, 0)
				if test.noCache {
					req.Header.Set(headers.NameCacheControl, headers.ValueNoCache)
				}
				done := make(chan dpcResponse)
				go func() { done <- serveDPC(h.client, req) }()
				// the origin holds every call once the limit is reached, and none may join them
				require.Eventually(t, func() bool { return pt.inflight.Load() >= limit },
					5*time.Second, time.Millisecond)
				time.Sleep(50 * time.Millisecond)
				close(pt.gate)
				resp := <-done
				require.Equal(t, limit, pt.peak.Load())
				requireResult(t, resp, engineDPC, test.status)
				got := bucketValues(t, resp.body)
				require.Equal(t, "53", got[b.Unix()])
				require.Equal(t, "13", got[b.Add(pbBuckets*pbStep).Unix()])
			})
		}
	}
}

type heldPartials struct {
	*TestClient
	entered chan struct{}
}

func (h *heldPartials) FetchPartialBucket(r *http.Request, _ *timeseries.TimeRangeQuery,
	_ timeseries.PartialBucket, _ bool,
) (timeseries.Timeseries, status.LookupStatus, error) {
	h.entered <- struct{}{}
	<-r.Context().Done()
	return nil, status.LookupStatusError, r.Context().Err()
}

func withTestResources(r *http.Request) *http.Request {
	return request.SetResources(r, request.NewResources(&bo.Options{}, nil, nil, nil, nil, nil))
}

func TestStoppedPartialBucketsStopWaitingForASlot(t *testing.T) {
	// at a limit of one, the second bucket waits on the first until the request stops them both
	client := &heldPartials{TestClient: &TestClient{}, entered: make(chan struct{}, 2)}
	trq := &timeseries.TimeRangeQuery{Step: pbStep, PartialCount: 2}
	pf := startPartialBuckets(withTestResources(httptest.NewRequest(http.MethodGet, "/", nil)),
		&bo.Options{FetchConcurrencyLimit: 1}, client, trq, time.Now())
	<-client.entered
	time.Sleep(20 * time.Millisecond)
	require.Empty(t, client.entered, "the second bucket passed the limit")
	pf.stop()
	require.Empty(t, client.entered, "the stopped bucket still reached the origin")
	for i := range pf.fetched {
		require.ErrorIs(t, pf.fetched[i].err, context.Canceled)
	}
}

type panickyPartials struct {
	*TestClient
}

func (panickyPartials) FetchPartialBucket(*http.Request, *timeseries.TimeRangeQuery,
	timeseries.PartialBucket, bool,
) (timeseries.Timeseries, status.LookupStatus, error) {
	panic("partial bucket fetch")
}

func TestPartialBucketFetchesThatCannotRunAreLeftOut(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{Step: pbStep, PartialCount: 2}
	tests := []struct {
		name   string
		r      *http.Request
		client backends.TimeseriesBackend
	}{
		{"a panicking fetch", withTestResources(httptest.NewRequest(http.MethodGet, "/", nil)),
			panickyPartials{&TestClient{}}},
		{"an unreadable body", withTestResources(httptest.NewRequest(http.MethodPost, "/",
			iotest.ErrReader(errors.New("unreadable")))), &TestClient{}},
		{"no resources", httptest.NewRequest(http.MethodGet, "/", nil), &TestClient{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// an unset limit falls back to the default
			pf := startPartialBuckets(test.r, &bo.Options{}, test.client, trq, time.Now())
			require.Equal(t, bo.DefaultFetchConcurrencyLimit, cap(pf.limiter))
			pbs, values := pf.mergeInto(&dataset.DataSet{}, nil, time.Now(), nil, nil)
			require.Zero(t, values)
			for i, pb := range pbs {
				require.Equal(t, statusErr, pb.Status)
				require.Error(t, pf.fetched[i].err)
			}
		})
	}
}

func TestFetchPartialBucketPassesTheResponsesFormat(t *testing.T) {
	// a response that names its format, as ClickHouse's Native answers do, is read in that format
	ts, _, r, _, err := tu.NewTestInstance("", nil, http.StatusOK, "rows",
		map[string]string{hnClickHouseFormat: "Native"}, "prometheus", "/", "error")
	require.NoError(t, err)
	defer ts.Close()
	request.GetResources(r).BackendOptions.HTTPClient = &http.Client{}
	var format string
	modeler := &timeseries.Modeler{WireUnmarshalerReader: func(reader io.Reader,
		_ *timeseries.TimeRangeQuery,
	) (timeseries.Timeseries, error) {
		if hint, ok := reader.(*timeseries.FormatHintReader); ok {
			format = hint.Format
		}
		return &dataset.DataSet{}, nil
	}}
	rq, err := PartialBucketRequest(r.Context(), r)
	require.NoError(t, err)
	_, st, err := FetchPartialBucket(rq, nil, &timeseries.TimeRangeQuery{}, modeler)
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusKeyMiss, st)
	require.Equal(t, "Native", format)
	// a model must hold a DataSet, whose rows the bucket's label selects
	modeler.WireUnmarshalerReader = func(io.Reader, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
		return opaqueSeries{&dataset.DataSet{}}, nil
	}
	rq, err = PartialBucketRequest(r.Context(), r)
	require.NoError(t, err)
	_, _, err = FetchPartialBucket(rq, nil, &timeseries.TimeRangeQuery{}, modeler)
	require.ErrorIs(t, err, ErrPartialBucketModel)
	// a request built for one fetch is not reused for another
	_, _, err = FetchPartialBucket(rq, nil, &timeseries.TimeRangeQuery{}, modeler)
	require.ErrorIs(t, err, ErrPartialBucketFetch)
	_, err = PartialBucketRequest(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))
	require.ErrorIs(t, err, ErrPartialBucketFetch)
}

type opaqueSeries struct{ timeseries.Timeseries }

func TestInstantModelDrop(t *testing.T) {
	// drop starts at the first grid instant inside the range, truncate at the one before it, and
	// neither fetches a live point for a past range
	base := time.Now().Add(-6 * time.Hour).Truncate(pbStep)
	start, end := base.Add(pbStartSkew), base.Add(pbBuckets*pbStep+pbEndSkew)
	floorS, ceilS, floorE := base, base.Add(pbStep), base.Add(pbBuckets*pbStep)
	for mode, first := range map[timeseries.StepAlignment]time.Time{
		timeseries.StepAlignmentTruncate: floorS, timeseries.StepAlignmentDrop: ceilS,
	} {
		t.Run(mode.String(), func(t *testing.T) {
			h := newBucketHarness(t, mode)
			h.client.sampleModel = timeseries.SampleModelInstant
			h.client.stepAlignments = timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate |
				timeseries.StepAlignmentDrop | timeseries.StepAlignmentPartialEnd
			resp := h.serve(start, end, 0)
			requireResult(t, resp, engineDPC, status.StatusKeyMiss)
			got := bucketValues(t, resp.body)
			require.Equal(t, wholeBuckets(first, floorE, pbStep), got)
			for label := range got {
				require.False(t, mode == timeseries.StepAlignmentDrop && label < start.Unix(),
					"drop evaluated %d, before the range's start", label)
			}
			require.Zero(t, h.client.liveFetches.Load())
			requirePartialBuckets(t, resp)
		})
	}

	t.Run("a range holding no grid instant answers none", func(t *testing.T) {
		// the origin's answer comes through the object cache as sent, and its points, none on the grid,
		// are left out
		h := newBucketHarness(t, timeseries.StepAlignmentDrop)
		h.client.sampleModel = timeseries.SampleModelInstant
		h.client.stepAlignments = timeseries.StepAlignmentAll
		start, end := base.Add(pbStartSkew), base.Add(50*time.Second)
		for _, want := range []string{status.StatusKeyMiss, status.StatusHit} {
			req := h.request(start, end, 0)
			resp := serveDPC(h.client, req)
			requireResult(t, resp, engineOPC, want)
			require.Empty(t, bucketValues(t, resp.body), want)
			require.Equal(t, time.Duration(h.client.Configuration().PartialBucketTTL),
				request.GetResources(req).AlternateCacheTTL)
		}
		require.Equal(t, []string{upstreamRange(start, end)}, h.up.take())
	})

	t.Run("a range holding one grid instant answers it", func(t *testing.T) {
		h := newBucketHarness(t, timeseries.StepAlignmentDrop)
		h.client.sampleModel = timeseries.SampleModelInstant
		h.client.stepAlignments = timeseries.StepAlignmentAll
		start, end := base.Add(pbStartSkew), base.Add(pbStep+10*time.Second)
		for _, want := range []string{status.StatusKeyMiss, status.StatusHit} {
			resp := h.serve(start, end, 0)
			requireResult(t, resp, engineDPC, want)
			require.Equal(t, wholeBuckets(ceilS, ceilS, pbStep), bucketValues(t, resp.body), want)
		}
	})
}

func TestPartialBucketRequestIsolatesTheClientRequest(t *testing.T) {
	const body = "query=select+1"
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", strings.NewReader(body))
	rsc := request.NewResources(&bo.Options{}, nil, nil, nil, nil, nil)
	rsc.TimeRangeQuery = &timeseries.TimeRangeQuery{}
	r = request.SetResources(r, rsc)
	var wg sync.WaitGroup
	for i := range 8 {
		rq, err := PartialBucketRequest(r.Context(), r)
		require.NoError(t, err)
		rs := request.GetResources(rq)
		require.NotSame(t, rsc, rs)
		require.Same(t, rsc.BackendOptions, rs.BackendOptions)
		require.Nil(t, rs.TimeRangeQuery)
		wg.Go(func() {
			// a provider's rewrite of the bucket's request
			b, err := request.GetBody(rq)
			if err != nil || string(b) != body {
				t.Errorf("bucket request body = %q, %v", b, err)
			}
			request.SetBody(rq, []byte("query=select+"+strconv.Itoa(i)))
		})
	}
	wg.Wait()
	b, err := request.GetBody(r)
	require.NoError(t, err)
	require.Equal(t, body, string(b))
	require.Equal(t, body, string(rsc.RequestBody))
}

func TestPartialBucketConsumersLeaveTheCacheUnchanged(t *testing.T) {
	// what a transformer or a merge may do to the dataset it's handed: write tags and values
	vandalize := func(ts timeseries.Timeseries) {
		ds := ts.(*dataset.DataSet)
		ds.InjectTags(dataset.Tags{"vandal": "yes"})
		for _, r := range ds.Results {
			for _, s := range r.SeriesList {
				for _, p := range s.Points {
					p.Values[0] = "999"
				}
			}
		}
	}
	for _, fail := range []bool{false, true} {
		t.Run("fail="+strconv.FormatBool(fail), func(t *testing.T) {
			h := newBucketHarness(t, timeseries.StepAlignmentPartial)
			if fail {
				h.query = pbQuery + "{" + bucketsim.ModFailUnaligned + "}"
			}
			base := time.Now().Add(-6 * time.Hour).Truncate(pbStep)
			start, end := base.Add(pbStartSkew), base.Add(pbBuckets*pbStep+pbEndSkew)
			want := h.serve(start, end, 0).body
			const n = 4
			var wg sync.WaitGroup
			plain := make([]string, n)
			for i := range n {
				wg.Go(func() {
					req := h.request(start, end, 0)
					rsc := request.GetResources(req)
					if i%2 == 0 {
						rsc.TSTransformer = vandalize
						serveDPC(h.client, req)
						return
					}
					rsc.IsMergeMember = true
					serveDPC(h.client, req)
					vandalize(rsc.TS)
					rsc.TS.(*dataset.DataSet).StripTags([]string{"vandal"})
				})
				wg.Go(func() { plain[i] = h.serve(start, end, 0).body })
			}
			wg.Wait()
			for i := range n {
				require.Equal(t, want, plain[i], "concurrent plain caller %d", i)
			}
			require.Equal(t, want, h.serve(start, end, 0).body)
		})
	}
}
