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
	"testing"
	"time"
)

// mockTimeseries is a minimal Timeseries implementation for testing List methods.
type mockTimeseries struct {
	id      int
	extents ExtentList
	merged  []Timeseries
}

func (m *mockTimeseries) SetExtents(el ExtentList)          { m.extents = el }
func (m *mockTimeseries) Extents() ExtentList               { return m.extents }
func (m *mockTimeseries) VolatileExtents() ExtentList       { return nil }
func (m *mockTimeseries) SetVolatileExtents(ExtentList)     {}
func (m *mockTimeseries) TimestampCount() int64             { return 0 }
func (m *mockTimeseries) Step() time.Duration               { return 0 }
func (m *mockTimeseries) Sort()                             {}
func (m *mockTimeseries) SeriesCount() int                  { return 1 }
func (m *mockTimeseries) ValueCount() int64                 { return 0 }
func (m *mockTimeseries) Size() int64                       { return 0 }
func (m *mockTimeseries) SetTimeRangeQuery(*TimeRangeQuery) {}
func (m *mockTimeseries) CropToRange(Extent)                {}
func (m *mockTimeseries) CropToSize(int, time.Time, Extent) {}
func (m *mockTimeseries) CroppedClone(Extent) Timeseries    { return m.Clone() }

func (m *mockTimeseries) Clone() Timeseries {
	return &mockTimeseries{id: m.id, extents: m.extents}
}

func (m *mockTimeseries) Merge(_ bool, others ...Timeseries) {
	m.merged = append(m.merged, others...)
}

func TestListCompress(t *testing.T) {
	m1 := &mockTimeseries{id: 1}
	m2 := &mockTimeseries{id: 2}

	tests := []struct {
		name    string
		input   List
		wantLen int
	}{
		{"empty list", List{}, 0},
		{"all nil", List{nil, nil, nil}, 0},
		{"no nils", List{m1, m2}, 2},
		{"mixed nil and non-nil", List{m1, nil, m2, nil}, 2},
		{"single nil", List{nil}, 0},
		{"single non-nil", List{m1}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := tt.input.Compress()
			if len(out) != tt.wantLen {
				t.Errorf("expected length %d, got %d", tt.wantLen, len(out))
			}
			// verify no nil entries in output
			for i, ts := range out {
				if ts == nil {
					t.Errorf("output[%d] is nil", i)
				}
			}
		})
	}

	// verify order preservation
	t.Run("order preserved", func(t *testing.T) {
		out := List{m1, nil, m2}.Compress()
		if out[0].(*mockTimeseries).id != 1 || out[1].(*mockTimeseries).id != 2 {
			t.Error("order not preserved after compress")
		}
	})
}

func TestListMerge(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		out := List{}.Merge(true)
		if out != nil {
			t.Error("expected nil for empty list")
		}
	})

	t.Run("all nil entries", func(t *testing.T) {
		out := List{nil, nil}.Merge(true)
		if out != nil {
			t.Error("expected nil for all-nil list")
		}
	})

	t.Run("single element with clone", func(t *testing.T) {
		m := &mockTimeseries{id: 1}
		out := List{m}.Merge(true)
		if out == nil {
			t.Fatal("expected non-nil result")
		}
		// useClone=true should return a different object
		if out == Timeseries(m) {
			t.Error("expected cloned copy, got same reference")
		}
		if out.(*mockTimeseries).id != 1 {
			t.Error("clone should preserve id")
		}
	})

	t.Run("single element without clone", func(t *testing.T) {
		m := &mockTimeseries{id: 1}
		out := List{m}.Merge(false)
		if out == nil {
			t.Fatal("expected non-nil result")
		}
		// useClone=false should return the same object
		if out != Timeseries(m) {
			t.Error("expected same reference, got different object")
		}
	})

	t.Run("two elements", func(t *testing.T) {
		m1 := &mockTimeseries{id: 1}
		m2 := &mockTimeseries{id: 2}
		out := List{m1, m2}.Merge(true)
		if out == nil {
			t.Fatal("expected non-nil result")
		}
		// the cloned first element should have had Merge called with m2
		mock := out.(*mockTimeseries)
		if len(mock.merged) != 1 {
			t.Errorf("expected 1 merge call, got %d", len(mock.merged))
		}
	})

	t.Run("nils skipped in multi-element list", func(t *testing.T) {
		m1 := &mockTimeseries{id: 1}
		m2 := &mockTimeseries{id: 2}
		out := List{nil, m1, nil, m2, nil}.Merge(true)
		if out == nil {
			t.Fatal("expected non-nil result")
		}
		mock := out.(*mockTimeseries)
		if mock.id != 1 {
			t.Errorf("expected first non-nil element (id=1), got id=%d", mock.id)
		}
		if len(mock.merged) != 1 {
			t.Errorf("expected 1 merge call, got %d", len(mock.merged))
		}
	})
}
