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

package streamtest

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// CompareOptions relaxes Compare.
type CompareOptions struct {
	// IgnoreSizes skips the size fields of points, series and series headers.
	IgnoreSizes bool
	// IgnoreSeriesOrder matches each result's series by name and tags, not position.
	IgnoreSeriesOrder bool
}

// Compare returns an error describing the first difference between want and got,
// or nil when they match. Float NaN values are equal to each other.
func Compare(want, got *dataset.DataSet, o CompareOptions) error {
	if want == nil || got == nil {
		if want == nil && got == nil {
			return nil
		}
		return fmt.Errorf("dataset: want nil %t, got nil %t", want == nil, got == nil)
	}
	for _, f := range []struct{ name, want, got string }{
		{"sourceResultType", want.SourceResultType, got.SourceResultType},
		{"status", want.Status, got.Status},
		{"error", want.Error, got.Error},
		{"errorType", want.ErrorType, got.ErrorType},
	} {
		if f.want != f.got {
			return fmt.Errorf("%s: want %q, got %q", f.name, f.want, f.got)
		}
	}
	if !slices.Equal(want.Warnings, got.Warnings) {
		return fmt.Errorf("warnings: want %q, got %q", want.Warnings, got.Warnings)
	}
	if err := compareExtents("extents", want.ExtentList, got.ExtentList); err != nil {
		return err
	}
	if err := compareExtents("volatileExtents", want.VolatileExtentList, got.VolatileExtentList); err != nil {
		return err
	}
	if len(want.Results) != len(got.Results) {
		return fmt.Errorf("results: want %d, got %d", len(want.Results), len(got.Results))
	}
	for i := range want.Results {
		if err := compareResult(want.Results[i], got.Results[i], o); err != nil {
			return fmt.Errorf("results[%d].%w", i, err)
		}
	}
	return nil
}

func compareExtents(name string, want, got timeseries.ExtentList) error {
	if len(want) != len(got) {
		return fmt.Errorf("%s: want %v, got %v", name, want, got)
	}
	for i := range want {
		if !want[i].Start.Equal(got[i].Start) || !want[i].End.Equal(got[i].End) {
			return fmt.Errorf("%s: want %v, got %v", name, want, got)
		}
	}
	return nil
}

func compareResult(want, got *dataset.Result, o CompareOptions) error {
	if want == nil || got == nil {
		if want == nil && got == nil {
			return nil
		}
		return fmt.Errorf("result: want nil %t, got nil %t", want == nil, got == nil)
	}
	if want.StatementID != got.StatementID || want.Name != got.Name || want.Error != got.Error {
		return fmt.Errorf("result: want {%d %q %q}, got {%d %q %q}", want.StatementID, want.Name,
			want.Error, got.StatementID, got.Name, got.Error)
	}
	if len(want.SeriesList) != len(got.SeriesList) {
		return fmt.Errorf("series: want %d, got %d", len(want.SeriesList), len(got.SeriesList))
	}
	ws, gs := want.SeriesList, got.SeriesList
	if o.IgnoreSeriesOrder {
		ws, gs = sortedByKey(ws), sortedByKey(gs)
	}
	for i := range ws {
		if err := compareSeries(ws[i], gs[i], o); err != nil {
			return fmt.Errorf("series[%d]%w", i, err)
		}
	}
	return nil
}

func sortedByKey(sl dataset.SeriesList) dataset.SeriesList {
	keys := make(map[*dataset.Series]string, len(sl))
	for _, s := range sl {
		if s != nil {
			keys[s] = s.Header.Name + "\x00" + s.Header.Tags.JSON()
		}
	}
	out := slices.Clone(sl)
	slices.SortStableFunc(out, func(a, b *dataset.Series) int {
		return strings.Compare(keys[a], keys[b])
	})
	return out
}

func compareSeries(want, got *dataset.Series, o CompareOptions) error {
	if want == nil || got == nil {
		if want == nil && got == nil {
			return nil
		}
		return fmt.Errorf(": want nil %t, got nil %t", want == nil, got == nil)
	}
	wh, gh := &want.Header, &got.Header
	switch {
	case wh.Name != gh.Name:
		return fmt.Errorf(".name: want %q, got %q", wh.Name, gh.Name)
	case wh.QueryStatement != gh.QueryStatement:
		return fmt.Errorf(".query: want %q, got %q", wh.QueryStatement, gh.QueryStatement)
	case !maps.Equal(wh.Tags, gh.Tags):
		return fmt.Errorf(".tags: want %v, got %v", wh.Tags, gh.Tags)
	case wh.TimestampField != gh.TimestampField:
		return fmt.Errorf(".timestampField: want %v, got %v", wh.TimestampField, gh.TimestampField)
	case !slices.Equal(wh.TagFieldsList, gh.TagFieldsList):
		return fmt.Errorf(".tagFields: want %v, got %v", wh.TagFieldsList, gh.TagFieldsList)
	case !slices.Equal(wh.ValueFieldsList, gh.ValueFieldsList):
		return fmt.Errorf(".valueFields: want %v, got %v", wh.ValueFieldsList, gh.ValueFieldsList)
	case !slices.Equal(wh.UntrackedFieldsList, gh.UntrackedFieldsList):
		return fmt.Errorf(".untrackedFields: want %v, got %v", wh.UntrackedFieldsList, gh.UntrackedFieldsList)
	case !o.IgnoreSizes && wh.Size != gh.Size:
		return fmt.Errorf(".headerSize: want %d, got %d", wh.Size, gh.Size)
	case !o.IgnoreSizes && want.PointSize != got.PointSize:
		return fmt.Errorf(".pointSize: want %d, got %d", want.PointSize, got.PointSize)
	case len(want.Points) != len(got.Points):
		return fmt.Errorf(".points: want %d, got %d", len(want.Points), len(got.Points))
	}
	for i := range want.Points {
		wp, gp := &want.Points[i], &got.Points[i]
		switch {
		case wp.Epoch != gp.Epoch:
			return fmt.Errorf(".points[%d].epoch: want %d, got %d", i, wp.Epoch, gp.Epoch)
		case !o.IgnoreSizes && wp.Size != gp.Size:
			return fmt.Errorf(".points[%d].size: want %d, got %d", i, wp.Size, gp.Size)
		case len(wp.Values) != len(gp.Values):
			return fmt.Errorf(".points[%d].values: want %v, got %v", i, wp.Values, gp.Values)
		}
		for j := range wp.Values {
			if !valueEqual(wp.Values[j], gp.Values[j]) {
				return fmt.Errorf(".points[%d].values[%d]: want %#v, got %#v", i, j,
					wp.Values[j], gp.Values[j])
			}
		}
	}
	return nil
}

func valueEqual(want, got any) bool {
	switch w := want.(type) {
	case float64:
		g, ok := got.(float64)
		return ok && (w == g || (math.IsNaN(w) && math.IsNaN(g)))
	case float32:
		g, ok := got.(float32)
		return ok && (w == g || (math.IsNaN(float64(w)) && math.IsNaN(float64(g))))
	case []byte:
		g, ok := got.([]byte)
		return ok && bytes.Equal(w, g)
	}
	return reflect.DeepEqual(want, got)
}
