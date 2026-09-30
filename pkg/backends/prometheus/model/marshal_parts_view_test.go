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

package model

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"
)

// a dataset of the given series, one point a step from lo to hi, with some series histograms
func partsTestSet(rng *weaktest.Rand, lo, hi int, hosts []string) *dataset.DataSet {
	r := &dataset.Result{}
	for _, host := range hosts {
		hist := rng.IntN(4) == 0
		s := dataset.NewSeries(dataset.SeriesHeader{Tags: dataset.Tags{"job": "node", "instance": host}}, nil)
		if hist {
			s.Header.ValueFieldsList = []timeseries.FieldDefinition{{Name: fieldNameHistogram}}
		} else {
			s.Header.ValueFieldsList = []timeseries.FieldDefinition{{Name: "value"}}
		}
		for at := lo; at <= hi; at += 1 + rng.IntN(2) {
			v := strconv.Itoa(rng.IntN(1000))
			if hist {
				v = `{"count":"` + v + `"}`
			}
			s.SetPoints(append(s.Points(), dataset.Point{Epoch: epoch.Epoch(at) * 15e9, Values: []any{v}}))
		}
		r.SeriesList = append(r.SeriesList, s)
	}
	return &dataset.DataSet{Results: dataset.Results{r}, Status: "success"}
}

func TestMarshalReadsSeriesParts(t *testing.T) {
	rng := weaktest.NewRand(8, 21)
	hosts := []string{"a", "b", "c", "d"}
	var withParts int
	for trial := range 300 {
		view := partsTestSet(rng, 10, 20, hosts[:1+rng.IntN(4)]).FullView()
		view.Status = "success"
		// a start bucket, then a live point or end bucket, which may bring a series of its own
		view.MergeParts(true, partsTestSet(rng, 5, 9, hosts[:1+rng.IntN(4)]))
		live := partsTestSet(rng, 21, 21+rng.IntN(2), hosts[rng.IntN(4):])
		live.Warnings = []string{"live"}
		view.MergeParts(false, live)
		if view.HasParts() {
			withParts++
		}
		for _, isVector := range []bool{false, true} {
			var got, want bytes.Buffer
			if err := MarshalTSOrVectorWriter(view, nil, 200, &got, isVector); err != nil {
				t.Fatal(err)
			}
			if err := MarshalTSOrVectorWriter(view.Flat(), nil, 200, &want, isVector); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatalf("trial %d, vector %v:\n got %s\nwant %s", trial, isVector, got.Bytes(), want.Bytes())
			}
		}
	}
	if withParts < 100 {
		t.Fatal(fmt.Sprint("only ", withParts, " trials had parts"))
	}
}
