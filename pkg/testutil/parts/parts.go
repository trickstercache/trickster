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

// Package parts builds responses whose series hold point parts, for marshalers' tests
package parts

import (
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// Of returns a view of base with a start bucket and live point as parts: its first and last points
// copied a step before and after, and a series only the live point has
func Of(base *dataset.DataSet, step epoch.Epoch) *dataset.DataSet {
	view := base.FullView()
	view.MergeParts(true, shifted(base, true, step))
	live := shifted(base, false, step)
	if len(live.Results) > 0 && len(base.Results) > 0 && len(base.Results[0].SeriesList) > 0 {
		if s := base.Results[0].SeriesList[0]; s != nil && s.PointCount() > 0 {
			extra := &dataset.Series{Header: s.Header.Clone(), Points: dataset.Points{*s.PointAt(0)}}
			extra.Header.Name += "_live"
			extra.Points[0].Epoch = s.PointAt(s.PointCount()-1).Epoch + 2*step
			live.Results[0].SeriesList = append(live.Results[0].SeriesList, extra)
		}
	}
	view.MergeParts(false, live)
	return view
}

// a dataset with one point per series of base: its first moved a step earlier, or its last a step later
func shifted(base *dataset.DataSet, first bool, step epoch.Epoch) *dataset.DataSet {
	out := &dataset.DataSet{TimeRangeQuery: base.TimeRangeQuery, Status: base.Status}
	for _, r := range base.Results {
		if r == nil {
			continue
		}
		nr := &dataset.Result{StatementID: r.StatementID, Name: r.Name}
		for _, s := range r.SeriesList {
			if s == nil || s.PointCount() == 0 {
				continue
			}
			p := *s.PointAt(s.PointCount() - 1)
			p.Epoch += step
			if first {
				p = *s.PointAt(0)
				p.Epoch -= step
			}
			nr.SeriesList = append(nr.SeriesList, &dataset.Series{Header: s.Header, Points: dataset.Points{p}})
		}
		out.Results = append(out.Results, nr)
	}
	return out
}
