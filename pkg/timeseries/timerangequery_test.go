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

package timeseries

import (
	"net/url"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestNormalizeExtent(t *testing.T) {

	tmrw := time.Now().Add(time.Duration(24) * time.Hour).Unix()
	expected := (time.Now().Unix() / 10) * 10

	tests := []struct {
		start, end, stepSecs, now int64
		rangeStart, rangeEnd      int64
		err                       bool
	}{
		// Basic test
		{
			1, 100, 1, 1,
			1, 100,
			false,
		},
		// Ensure that it aligns to the step interval
		{
			1, 103, 10, 1,
			0, 100,
			false,
		},
		// Ensure that it brings in future times
		{
			1, tmrw, 10, 1,
			0, expected,
			false,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {

			trq := TimeRangeQuery{Statement: "up", Extent: Extent{Start: time.Unix(test.start, 0),
				End: time.Unix(test.end, 0)}, Step: time.Duration(test.stepSecs) * time.Second}

			trq.NormalizeExtent()

			if trq.Extent.Start.Unix() != test.rangeStart {
				t.Errorf("Mismatch in rangeStart: expected=%d actual=%d", test.rangeStart, trq.Extent.Start.Unix())
			}
			if trq.Extent.End.Unix() != test.rangeEnd {
				t.Errorf("Mismatch in rangeStart: expected=%d actual=%d", test.rangeEnd, trq.Extent.End.Unix())
			}
		})
	}
}

func TestClone(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1/")
	trq := &TimeRangeQuery{Statement: "1234", Extent: Extent{Start: time.Unix(5, 0),
		End: time.Unix(10, 0)}, Step: time.Duration(5) * time.Second, TemplateURL: u}

	trq.TagFieldDefintions = []FieldDefinition{{}}
	trq.ValueFieldDefinitions = []FieldDefinition{{}}

	c := trq.Clone()
	if !reflect.DeepEqual(trq, c) {
		t.Errorf("expected %s got %s", trq.String(), c.String())
	}
}

func TestSizeTRQ(t *testing.T) {

	u, _ := url.Parse("http://127.0.0.1/")
	trq := &TimeRangeQuery{Statement: "1234", Extent: Extent{Start: time.Unix(5, 0),
		End: time.Unix(10, 0)}, Step: time.Duration(5) * time.Second, TemplateURL: u}

	size := trq.Size()

	if size != 119 {
		t.Errorf("expected %d got %d", 119, size)
	}
}

func TestExtractBackfillTolerance(t *testing.T) {

	trq := &TimeRangeQuery{}

	trq.ExtractBackfillTolerance("testing trickster-backfill-tolerance:30 ")

	if trq.BackfillTolerance != time.Second*30 {
		t.Error("expected 30 got", trq.BackfillTolerance)
	}
}

func TestStringTRQ(t *testing.T) {
	const expected = `{ "statement": "1234", "step": "5s", "extent": "5000-10000", "tsd": "{"name":"","type":0,"pos":0,"stype":"","provider1":0}", "td": [], "vd": [] }`
	trq := &TimeRangeQuery{Statement: "1234", Extent: Extent{Start: time.Unix(5, 0),
		End: time.Unix(10, 0)}, Step: time.Duration(5) * time.Second}
	s := trq.String()

	if s != expected {
		t.Errorf("%s\n%s", s, expected)
	}
}

func TestGetBackfillTolerance(t *testing.T) {

	expected := time.Second * 5

	trq := &TimeRangeQuery{Statement: "1234"}
	i := trq.GetBackfillTolerance(expected, 0)
	if i != expected {
		t.Errorf("expected %s got %s", expected, i)
	}

	trq.BackfillTolerance = time.Second * 30
	i = trq.GetBackfillTolerance(expected, 0)
	if i == expected {
		t.Errorf("expected %s got %s", time.Second*30, i)
	}

	trq.Step = 5 * time.Second
	trq.BackfillTolerance = 0

	expected = time.Second * 50
	i = trq.GetBackfillTolerance(time.Second*5, 10)
	if i != expected {
		t.Errorf("expected %s got %s", expected, i)
	}

	trq.BackfillTolerance = -1
	i = trq.GetBackfillTolerance(time.Second*5, 10)
	if i != 0 {
		t.Errorf("expected %d got %d", 0, i)
	}

}
