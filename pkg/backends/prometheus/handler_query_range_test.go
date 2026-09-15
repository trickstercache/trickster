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
	"io"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	responsemerge "github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func TestQueryRangeHandler(t *testing.T) {
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200, "{}",
		nil, providers.Prometheus, "/query_range?q=up&start=0&end=900&step=15", "debug")
	if err != nil {
		t.Error(err)
	} else {
		defer ts.Close()
	}
	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	rsc.BackendClient = client
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()
	rsc.IsMergeMember = true

	client.QueryRangeHandler(w, r)
	if rsc.BatchMergeFunc == nil {
		t.Error("expected query range merge member to configure BatchMergeFunc")
	}
	accumulator := responsemerge.NewAccumulator()
	items := []responsemerge.BatchItem{
		{Data: dedupRangeDataSet("1"), Member: 0},
		{Data: dedupRangeDataSet("2"), Member: 1},
	}
	handled, err := rsc.BatchMergeFunc(accumulator, items)
	if err != nil || !handled {
		t.Fatalf("dedup batch merge failed: handled=%v err=%v", handled, err)
	}
	merged := accumulator.GetTSData().(*dataset.DataSet)
	points := merged.Results[0].SeriesList[0].Points
	if len(points) != 1 || points[0].Values[0] != "2" {
		t.Fatalf("dedup points got %#v, want one last-value-wins point", points)
	}

	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "{}" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}
}

func dedupRangeDataSet(value string) *dataset.DataSet {
	return &dataset.DataSet{Results: dataset.Results{{SeriesList: dataset.SeriesList{{
		Header: dataset.SeriesHeader{Name: "up"},
		Points: dataset.Points{{Epoch: epoch.Epoch(1), Values: []any{value}}},
	}}}}}
}
