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

package clickhouse

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/stretchr/testify/require"
)

const (
	partialSQL = "SELECT toStartOfMinute(ts) AS t, count() AS cnt FROM e WHERE ts >= toDateTime(1516665607) " +
		"AND ts < toDateTime(1516687213) GROUP BY t ORDER BY t"
	// the origin's answer for the start bucket, which holds the rows from 7s past the minute
	partialTSV    = "t\tcnt\nDateTime\tUInt64\n2018-01-23 00:00:00\t53\n"
	partialFormat = "X-ClickHouse-Format"
)

func newPartialBucketClient(t *testing.T, r *http.Request) (*Client, *tu.RecordingTransport) {
	t.Helper()
	rsc := request.GetResources(r)
	backendClient, err := NewClient("test", rsc.BackendOptions, nil, rsc.CacheClient, nil, nil)
	require.NoError(t, err)
	client := backendClient.(*Client)
	rec := &tu.RecordingTransport{Inner: client.HTTPClient().Transport}
	client.HTTPClient().Transport = rec
	rsc.BackendClient, rsc.BackendOptions.HTTPClient = client, client.HTTPClient()
	return client, rec
}

func TestFetchPartialBucket(t *testing.T) {
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	require.NoError(t, err)
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, http.StatusOK, partialTSV,
		map[string]string{partialFormat: "TSVWithNamesAndTypes"}, providers.ClickHouse,
		"/?"+url.Values{upQuery: {partialSQL}}.Encode(), "error")
	require.NoError(t, err)
	defer ts.Close()
	client, rec := newPartialBucketClient(t, r)
	trq, _, _, err := client.ParseTimeRangeQuery(r)
	require.NoError(t, err)
	label := time.Unix(1516665600, 0)
	pb := timeseries.PartialBucket{
		Label: label, Lower: label.Add(7 * time.Second), Upper: label.Add(time.Minute),
		Edge: timeseries.BucketEdgeStart,
	}

	// the bucket's own range is sent once, then answered by the object cache
	for _, want := range []status.LookupStatus{status.LookupStatusKeyMiss, status.LookupStatusHit} {
		ts, st, err := client.FetchPartialBucket(r, trq, pb, false)
		require.NoError(t, err)
		require.Equal(t, want, st)
		ds := ts.(*dataset.DataSet)
		require.Len(t, ds.Results[0].SeriesList, 1)
		points := ds.Results[0].SeriesList[0].Points
		require.Len(t, points, 1)
		require.Equal(t, label.UnixNano(), int64(points[0].Epoch))
	}
	sent := rec.Take()
	require.Len(t, sent, 1)
	query := sent[0].URL.Query().Get(upQuery)
	require.Contains(t, query, "ts >= toDateTime(1516665607)")
	require.Contains(t, query, "ts < toDateTime(1516665660)")
	// the client's request is left as it was
	require.Equal(t, partialSQL, r.URL.Query().Get(upQuery))

	t.Run("in a body", func(t *testing.T) {
		post, err := http.NewRequestWithContext(r.Context(), http.MethodPost, ts.URL+"/",
			strings.NewReader(partialSQL))
		require.NoError(t, err)
		post = request.SetResources(post, request.GetResources(r).Clone())
		trq, _, _, err := client.ParseTimeRangeQuery(post)
		require.NoError(t, err)
		pb := pb
		pb.Lower = label.Add(8 * time.Second)
		_, st, err := client.FetchPartialBucket(post, trq, pb, false)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusKeyMiss, st)
		sent := rec.Take()
		require.Len(t, sent, 1)
		require.Contains(t, string(sent[0].Body), "ts >= toDateTime(1516665608)")
	})

	t.Run("failures", func(t *testing.T) {
		exclusive := pb
		exclusive.LowerExclusive = true
		_, _, err := client.FetchPartialBucket(r, trq, exclusive, false)
		require.ErrorIs(t, err, sqlanalyzer.ErrUnsupportedRange)
		_, _, err = client.FetchPartialBucket(r, &timeseries.TimeRangeQuery{}, pb, false)
		require.ErrorIs(t, err, errMissingQueryPlan)
		_, _, err = client.FetchPartialBucket(nil, trq, pb, false)
		require.ErrorIs(t, err, errInvalidRewriteInput)
		require.Empty(t, rec.Take())
	})
}
