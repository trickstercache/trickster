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

package prometheus

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/mocks/promsim"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

func TestFetchPartialBucketIsFastForward(t *testing.T) {
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	require.NoError(t, err)
	end := time.Now().Truncate(time.Second)
	query := "partial_bucket_test{series_id=\"1\"}"
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200, "", nil,
		tu.PromSimBackendProvider, promsim.PathQueryRange, "error")
	require.NoError(t, err)
	defer ts.Close()
	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, rsc.CacheClient, nil, nil)
	require.NoError(t, err)
	client := backendClient.(*Client)
	rsc.BackendClient = client
	rsc.BackendOptions.HTTPClient = client.HTTPClient()
	r, err = http.NewRequestWithContext(r.Context(), http.MethodGet, ts.URL+promsim.PathQueryRange+
		"?query="+query+"&step=60&start="+strconv.FormatInt(end.Add(-time.Hour).Unix(), 10)+
		"&end="+strconv.FormatInt(end.Unix(), 10), nil)
	require.NoError(t, err)
	trq := &timeseries.TimeRangeQuery{Step: time.Minute}
	pb := timeseries.PartialBucket{
		Label: end.Truncate(time.Minute), Lower: end.Truncate(time.Minute),
		Upper: end, Edge: timeseries.BucketEdgeEnd,
	}

	// the live point is an instant query at the range's end, kept in the object cache
	for _, want := range []status.LookupStatus{status.LookupStatusKeyMiss, status.LookupStatusHit} {
		ds, st, err := client.FetchPartialBucket(r, trq, pb, true)
		require.NoError(t, err)
		require.Equal(t, want, st)
		require.Len(t, ds.Results[0].SeriesList, 1)
		points := ds.Results[0].SeriesList[0].Points
		require.Len(t, points, 1)
		require.Equal(t, end.UnixNano(), int64(points[0].Epoch))
	}
	// instant-evaluated points have no other partial bucket
	_, _, err = client.FetchPartialBucket(r, trq, pb, false)
	require.ErrorIs(t, err, backends.ErrPartialBucketsUnsupported)
}
