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

package tsm

import (
	"os"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/testutil/golden"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// goldenSeries builds a Series with the given name, tags, optional query
// statement, and one or more (epoch, string-value) points. Used only by
// the bootstrap generator below; the actual tests load DataSets from JSON.
func goldenSeries(name string, tags dataset.Tags, query string, pts ...struct {
	e int64
	v string
}) *dataset.Series {
	points := make(dataset.Points, len(pts))
	for i, p := range pts {
		points[i] = dataset.Point{Epoch: epoch.Epoch(p.e), Size: 32, Values: []any{p.v}}
	}
	return &dataset.Series{
		Header: dataset.SeriesHeader{Name: name, Tags: tags, QueryStatement: query},
		Points: points,
	}
}

func dsWith(series ...*dataset.Series) *dataset.DataSet {
	return &dataset.DataSet{
		Results: dataset.Results{{StatementID: 0, SeriesList: dataset.SeriesList(series)}},
	}
}

// TestGenerateGoldenFixtures (re)generates testdata/weighted_avg/*.json from
// in-Go DataSet builders. It only runs when -update is passed, so normal test
// runs skip it. After regeneration, the produced JSON is the canonical input
// for the migrated weighted-avg tests.
func TestGenerateGoldenFixtures(t *testing.T) {
	if !*golden.Update {
		t.Skip("pass -update to regenerate testdata fixtures")
	}
	// Require TRICKSTER_REGEN_GOLDENS=1 in addition to -update so a repo-wide
	// `go test -update ./...` can't silently overwrite these fixtures.
	if os.Getenv("TRICKSTER_REGEN_GOLDENS") != "1" {
		t.Skip("set TRICKSTER_REGEN_GOLDENS=1 to regenerate")
	}
	type pt = struct {
		e int64
		v string
	}

	// drops_unpaired: sum has us-east-1 (paired) and us-west-2 (unpaired); count has only us-east-1.
	writeGoldenDataSet(t, "weighted_avg/drops_unpaired_sum", dsWith(
		goldenSeries("rps", dataset.Tags{"region": "us-east-1"}, "", pt{100, "100"}),
		goldenSeries("rps", dataset.Tags{"region": "us-west-2"}, "", pt{100, "200"}),
	))
	writeGoldenDataSet(t, "weighted_avg/drops_unpaired_count", dsWith(
		goldenSeries("rps", dataset.Tags{"region": "us-east-1"}, "", pt{100, "10"}),
	))

	// baseline_no_prune: single unpaired series in sum, empty count.
	writeGoldenDataSet(t, "weighted_avg/baseline_no_prune_sum", dsWith(
		goldenSeries("rps", dataset.Tags{"region": "us-west-2"}, "", pt{100, "200"}),
	))
	writeGoldenDataSet(t, "weighted_avg/baseline_no_prune_count", &dataset.DataSet{
		Results: dataset.Results{{StatementID: 0, SeriesList: dataset.SeriesList{}}},
	})

	// subset_epochs: paired series but one epoch has no count match.
	writeGoldenDataSet(t, "weighted_avg/subset_epochs_sum", dsWith(
		goldenSeries("rps", dataset.Tags{"region": "us-east-1"}, "", pt{100, "10"}, pt{200, "20"}),
	))
	writeGoldenDataSet(t, "weighted_avg/subset_epochs_count", dsWith(
		goldenSeries("rps", dataset.Tags{"region": "us-east-1"}, "", pt{100, "2"}),
	))

	// pairing_statement: sum/count have different QueryStatement values; pairing arg aligns them.
	writeGoldenDataSet(t, "weighted_avg/pairing_statement_sum", dsWith(
		goldenSeries("rps", dataset.Tags{"region": "us-east-1"}, "sum(rps)", pt{100, "100"}),
	))
	writeGoldenDataSet(t, "weighted_avg/pairing_statement_count", dsWith(
		goldenSeries("rps", dataset.Tags{"region": "us-east-1"}, "count(rps)", pt{100, "4"}),
	))
}
