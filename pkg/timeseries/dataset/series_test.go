/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package dataset

import (
	"testing"

	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

func testSeries() *Series {
	sh := testSeriesHeader()
	return &Series{
		Header: sh,
		Points: testPoints(),
	}
}

func testSeriesHeader() SeriesHeader {
	sh := SeriesHeader{
		Name:           "test",
		QueryStatement: "SELECT TRICKSTER!",
		Tags:           Tags{"test1": "value1"},
		FieldsList: []timeseries.FieldDefinition{
			{
				Name:     "Field1",
				DataType: timeseries.FieldDataType(1),
			},
		},
		TimestampIndex: 37,
		Size:           56,
	}
	return sh
}

func TestSeriesHeaderCalculateHash(t *testing.T) {
	sh := testSeriesHeader()
	if sh.CalculateHash() == 0 {
		t.Error("invalid hash value")
	}
}

func TestSeriesHeaderClone(t *testing.T) {
	sh := testSeriesHeader()
	sh2 := sh.Clone()
	if sh2.Size != sh.Size ||
		len(sh2.FieldsList) != 1 || //len(sh2.FieldsLookup) != 1 ||
		sh2.FieldsList[0].Name != "Field1" {
		t.Error("series header clone mismatch")
	}

}

func TestSeriesClone(t *testing.T) {

	s := testSeries()
	s2 := s.Clone()

	if s2.Header.CalculateHash() != s.Header.CalculateHash() {
		t.Error("series clone mismatch")
	}

	if s2.Points[0].Epoch != s.Points[0].Epoch {
		t.Error("series clone mismatch")
	}

}
