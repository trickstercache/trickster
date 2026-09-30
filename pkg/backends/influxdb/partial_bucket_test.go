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

package influxdb

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/influxql"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

const (
	partialDB     = "trickster"
	partialLabel  = 1704067200
	partialLower  = "2024-01-01T00:00:07Z"
	partialUpper  = "2024-01-01T00:01:00Z"
	partialSelect = "SELECT mean(v) FROM cpu WHERE time >= '" + partialLower + "'"
	partialGroup  = " GROUP BY time(1m)"
	partialV1Body = `{"results":[{"statement_id":0,"series":[{"name":"cpu","columns":["time","mean"],` +
		`"values":[[1704067200000000000,53]]}]}]}`
	partialV3Body = `[{"time":"2024-01-01T00:00:00","v":53}]`
	partialV3SQL  = "SELECT date_bin(INTERVAL '1 minute', time) AS time, avg(v) AS v FROM cpu " +
		"WHERE time >= '" + partialLower + "' AND time < '2024-01-01T01:00:13Z' GROUP BY 1 ORDER BY 1"
)

type partialBucketCase struct {
	name, path, body string
	query            url.Values
	pb               timeseries.PartialBucket
	// want and unwanted are found in, or missing from, the statement the origin receives
	want, unwanted string
}

func TestFetchPartialBucket(t *testing.T) {
	label := time.Unix(partialLabel, 0)
	start := timeseries.PartialBucket{
		Label: label, Lower: label.Add(7 * time.Second), Upper: label.Add(time.Minute),
		Edge: timeseries.BucketEdgeStart,
	}
	live := timeseries.PartialBucket{Label: label, Lower: label, Edge: timeseries.BucketEdgeEnd}
	v1 := func(where string) url.Values {
		return url.Values{influxql.ParamDB: {partialDB}, influxql.ParamQuery: {partialSelect + where + partialGroup}}
	}
	for _, test := range []partialBucketCase{
		{
			"influxql", "/query", partialV1Body, v1(" AND time < '2024-01-01T01:00:13Z'"), start,
			"time >= '" + partialLower + "' AND time < '" + partialUpper + "'", "",
		},
		// a statement with no upper bound, or a bare now() one, keeps it for a range running to now
		{"influxql with no upper bound", "/query", partialV1Body, v1(""), live, "time >= '2024-01-01T00:00:00Z'", "time <"},
		{"influxql ending at now()", "/query", partialV1Body, v1(" AND time <= now()"), live, "time <= now()", ""},
		{
			"influxql over v3", "/api/v3/query_influxql", partialV3Body,
			url.Values{"db": {partialDB}, "format": {"json"}, "q": v1(" AND time < '2024-01-01T01:00:13Z'")[influxql.ParamQuery]},
			start, "time < '" + partialUpper + "'", "",
		},
		{
			"sql", "/api/v3/query_sql", partialV3Body,
			url.Values{"db": {partialDB}, "format": {"json"}, "q": {partialV3SQL}},
			start, `"time" < '` + partialUpper + `'`, "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, rec, r := newPartialBucketInstance(t, test)
			trq, _, _, err := client.ParseTimeRangeQuery(r)
			require.NoError(t, err)
			// the bucket's own range is sent once, then answered by the object cache
			for _, want := range []status.LookupStatus{status.LookupStatusKeyMiss, status.LookupStatusHit} {
				rq, err := engines.PartialBucketRequest(r.Context(), r)
				require.NoError(t, err)
				ds, st, err := client.FetchPartialBucket(rq, trq, test.pb, false)
				require.NoError(t, err)
				require.Equal(t, want, st)
				require.Equal(t, int64(1), ds.ValueCount())
			}
			sent := rec.Take()
			require.Len(t, sent, 1)
			statement := sent[0].URL.Query().Get(influxql.ParamQuery)
			require.Contains(t, statement, test.want)
			if test.unwanted != "" {
				require.NotContains(t, statement, test.unwanted)
			}
			// only v1 asks the origin for nanosecond epochs; v3 requests keep the client's own parameters
			wantEpoch := ""
			if test.path == "/query" {
				wantEpoch = "ns"
			}
			require.Equal(t, wantEpoch, sent[0].URL.Query().Get(influxql.ParamEpoch))
			// the client's request is left as it was
			require.Equal(t, test.query.Get(influxql.ParamQuery), r.URL.Query().Get(influxql.ParamQuery))
		})
	}
}

func TestFetchPartialBucketFailures(t *testing.T) {
	client, rec, r := newPartialBucketInstance(t, partialBucketCase{
		path: "/query", body: partialV1Body, query: url.Values{
			influxql.ParamDB: {partialDB}, influxql.ParamQuery: {partialSelect + partialGroup},
		},
	})
	trq, _, _, err := client.ParseTimeRangeQuery(r)
	require.NoError(t, err)
	pb := timeseries.PartialBucket{Lower: time.Unix(partialLabel, 0), LowerExclusive: true}
	_, _, err = client.FetchPartialBucket(r, trq, pb, false)
	require.ErrorIs(t, err, influxql.ErrUnsupportedRange)
	// Flux, remote read and unparsed queries have no partial buckets
	_, _, err = client.FetchPartialBucket(r, &timeseries.TimeRangeQuery{}, pb, false)
	require.ErrorIs(t, err, backends.ErrPartialBucketsUnsupported)
	_, _, err = client.FetchPartialBucket(nil, trq, pb, false)
	require.ErrorIs(t, err, backends.ErrPartialBucketsUnsupported)
	require.Empty(t, rec.Take())
}

func newPartialBucketInstance(t *testing.T, test partialBucketCase) (*Client, *tu.RecordingTransport, *http.Request) {
	t.Helper()
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	require.NoError(t, err)
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, http.StatusOK, test.body,
		nil, providers.InfluxDB, test.path+"?"+test.query.Encode(), "error")
	require.NoError(t, err)
	t.Cleanup(ts.Close)
	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, rsc.CacheClient, nil, nil)
	require.NoError(t, err)
	client := backendClient.(*Client)
	rec := &tu.RecordingTransport{Inner: client.HTTPClient().Transport}
	client.HTTPClient().Transport = rec
	rsc.BackendClient, rsc.BackendOptions.HTTPClient = client, client.HTTPClient()
	return client, rec, r
}
