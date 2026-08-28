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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/testutil/golden"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// goldenPoint is the on-disk representation of dataset.Point. We use a local
// type because dataset.Point.Values is []any -- json round-trips strings as
// strings, but the rest of dataset.DataSet has unexported and function-typed
// fields that encoding/json cannot handle directly. Keeping the on-disk shape
// minimal and explicit avoids depending on future dataset struct tags.
type goldenPoint struct {
	Epoch  int64 `json:"epoch"`
	Size   int   `json:"size"`
	Values []any `json:"values"`
}

type goldenSeriesJSON struct {
	Name           string            `json:"name"`
	Tags           map[string]string `json:"tags,omitempty"`
	QueryStatement string            `json:"query,omitempty"`
	Points         []goldenPoint     `json:"points"`
}

type goldenResult struct {
	StatementID int                `json:"statement_id"`
	Series      []goldenSeriesJSON `json:"series"`
}

type goldenDataSet struct {
	Results []goldenResult `json:"results"`
}

func toGolden(ds *dataset.DataSet) goldenDataSet {
	out := goldenDataSet{Results: make([]goldenResult, 0, len(ds.Results))}
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		gr := goldenResult{StatementID: r.StatementID, Series: make([]goldenSeriesJSON, 0, len(r.SeriesList))}
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			gs := goldenSeriesJSON{
				Name:           s.Header.Name,
				Tags:           map[string]string(s.Header.Tags),
				QueryStatement: s.Header.QueryStatement,
				Points:         make([]goldenPoint, len(s.Points)),
			}
			for i, p := range s.Points {
				gs.Points[i] = goldenPoint{
					Epoch:  int64(p.Epoch),
					Size:   p.Size,
					Values: append([]any(nil), p.Values...),
				}
			}
			gr.Series = append(gr.Series, gs)
		}
		out.Results = append(out.Results, gr)
	}
	return out
}

func fromGolden(g goldenDataSet) *dataset.DataSet {
	ds := &dataset.DataSet{Results: make(dataset.Results, len(g.Results))}
	for i, gr := range g.Results {
		series := make(dataset.SeriesList, len(gr.Series))
		for j, gs := range gr.Series {
			points := make(dataset.Points, len(gs.Points))
			for k, gp := range gs.Points {
				points[k] = dataset.Point{
					Epoch:  epoch.Epoch(gp.Epoch),
					Size:   gp.Size,
					Values: gp.Values,
				}
			}
			series[j] = &dataset.Series{
				Header: dataset.SeriesHeader{
					Name:           gs.Name,
					Tags:           dataset.Tags(gs.Tags),
					QueryStatement: gs.QueryStatement,
				},
				Points: points,
			}
		}
		ds.Results[i] = &dataset.Result{StatementID: gr.StatementID, SeriesList: series}
	}
	return ds
}

func loadGoldenDataSet(t testing.TB, name string) *dataset.DataSet {
	t.Helper()
	var g goldenDataSet
	golden.LoadJSON(t, name, &g)
	return fromGolden(g)
}

func writeGoldenDataSet(t testing.TB, name string, ds *dataset.DataSet) {
	t.Helper()
	golden.WriteJSON(t, name, toGolden(ds))
}
