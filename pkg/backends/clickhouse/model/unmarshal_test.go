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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func TestStripSize(t *testing.T) {
	input := "myInput(10)"
	out := stripSize(input)
	if out != "myInput" {
		t.Error("failed to properly strip size")
	}
}

func TestUnmarshalTimeseries(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(testDataTSVWithNamesAndTypes), testTRQ.Clone())
	if err != nil {
		t.Error(err)
	}

	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		t.Error("expected non-nil dataset")
		return
	}

	el := testDataSet().ExtentList

	if len(ds.ExtentList) != 1 || !ds.ExtentList[0].Start.Equal(el[0].Start) ||
		!ds.ExtentList[0].End.Equal(el[0].End) {
		t.Error("unexpected extents: ", ds.ExtentList)
	}
}
