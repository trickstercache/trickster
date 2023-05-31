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

package dataset

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func testHeader() *SeriesHeader {
	return &SeriesHeader{
		Name: "test",
		Tags: Tags{"tag1": "value1", "tag2": "trickster"},
		FieldsList: []timeseries.FieldDefinition{
			{
				Name:     "time",
				DataType: timeseries.Int64,
			},
			{
				Name:     "value1",
				DataType: timeseries.Int64,
			},
		},
		QueryStatement: "SELECT TRICKSTER!",
	}
}

func TestCalculateSeriesHeaderSize(t *testing.T) {

	const expected = 492
	sh := testHeader()
	i := sh.CalculateSize()
	if i != expected {
		t.Errorf("expected %d got %d", expected, i)
	}
}

func TestSeriesHeaderString(t *testing.T) {

	const expected = `{"name":"test","query":"SELECT TRICKSTER!","tags":"tag1=value1;tag2=trickster","fields":["time","value1"],"timestampIndex":0}`

	if s := testHeader().String(); s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

}
