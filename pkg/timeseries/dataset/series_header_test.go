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
		ValueFieldsList: []timeseries.FieldDefinition{
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
	const expected = 633
	sh := testHeader()
	i := sh.CalculateSize()
	if i != expected {
		t.Errorf("expected %d got %d", expected, i)
	}
}

func TestSeriesHeaderString(t *testing.T) {
	const expected = `{"name":"test","query":"SELECT TRICKSTER!","tags":"tag1=value1;tag2=trickster","valueFields":["time","value1"],"timeStampField":""}`
	if s := testHeader().String(); s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}
}

func TestSeriesHeaderFieldDefinitions(t *testing.T) {
	t.Run("all field types with output positions", func(t *testing.T) {
		sh := SeriesHeader{
			TimestampField: timeseries.FieldDefinition{
				Name: "time", DataType: timeseries.Int64, OutputPosition: 0,
			},
			TagFieldsList: timeseries.FieldDefinitions{
				{Name: "host", DataType: timeseries.String, OutputPosition: 1},
			},
			ValueFieldsList: timeseries.FieldDefinitions{
				{Name: "value", DataType: timeseries.Float64, OutputPosition: 2},
			},
			UntrackedFieldsList: timeseries.FieldDefinitions{
				{Name: "extra", DataType: timeseries.String, OutputPosition: 3},
			},
		}
		fds := sh.FieldDefinitions()
		if len(fds) != 4 {
			t.Fatalf("expected 4 fields, got %d", len(fds))
		}
		// verify sorted by OutputPosition
		for i := 1; i < len(fds); i++ {
			if fds[i].OutputPosition < fds[i-1].OutputPosition {
				t.Errorf("not sorted by OutputPosition at index %d", i)
			}
		}
		if fds[0].Name != "time" || fds[1].Name != "host" ||
			fds[2].Name != "value" || fds[3].Name != "extra" {
			t.Errorf("unexpected field order: %v", fds)
		}
	})

	t.Run("empty header returns timestamp field", func(t *testing.T) {
		// even an empty header has a zero-value TimestampField with OutputPosition 0,
		// which passes the position filter
		sh := SeriesHeader{}
		fds := sh.FieldDefinitions()
		if len(fds) != 1 {
			t.Errorf("expected 1 field (default timestamp), got %d", len(fds))
		}
	})

	t.Run("negative output position filtered", func(t *testing.T) {
		sh := SeriesHeader{
			TimestampField: timeseries.FieldDefinition{
				Name: "time", OutputPosition: -1, // filtered out
			},
			ValueFieldsList: timeseries.FieldDefinitions{
				{Name: "good", DataType: timeseries.Int64, OutputPosition: 0},
				{Name: "bad", DataType: timeseries.Int64, OutputPosition: -1},
			},
		}
		fds := sh.FieldDefinitions()
		if len(fds) != 1 {
			t.Errorf("expected 1 field (negatives filtered), got %d", len(fds))
		}
		if len(fds) > 0 && fds[0].Name != "good" {
			t.Errorf("expected 'good', got %q", fds[0].Name)
		}
	})
}

func TestCalculateHashCaching(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		sh1 := testSeriesHeader()
		sh2 := testSeriesHeader()
		if sh1.CalculateHash() != sh2.CalculateHash() {
			t.Error("same header should produce same hash")
		}
	})

	t.Run("different names produce different hashes", func(t *testing.T) {
		sh1 := testSeriesHeader()
		sh2 := testSeriesHeader()
		sh2.Name = "different"
		if sh1.CalculateHash() == sh2.CalculateHash(true) {
			t.Error("different names should produce different hashes")
		}
	})

	t.Run("cached hash returned without rehash", func(t *testing.T) {
		sh := testSeriesHeader()
		h1 := sh.CalculateHash()
		h2 := sh.CalculateHash() // should return cached value
		if h1 != h2 {
			t.Error("cached hash should be stable")
		}
	})

	t.Run("rehash forces recalculation", func(t *testing.T) {
		sh := testSeriesHeader()
		h1 := sh.CalculateHash()
		sh.Name = "changed"
		// without rehash, still returns cached value
		h2 := sh.CalculateHash()
		if h1 != h2 {
			t.Error("without rehash, should return cached value")
		}
		// with rehash=true, recalculates
		h3 := sh.CalculateHash(true)
		if h1 == h3 {
			t.Error("rehash should produce different hash after name change")
		}
	})
}
