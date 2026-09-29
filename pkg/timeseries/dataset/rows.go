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
	"iter"
	"slices"
)

// Row is one point of a series, as Result.Rows yields it.
type Row struct {
	Series *Series
	Point  *Point
	// SeriesIndex is the series' position in its result's SeriesList
	SeriesIndex int
}

// RowOrder orders the rows Result.Rows yields: by epoch, newest first when Descending, and within
// an epoch by Compare, or by series order when Compare is nil.
type RowOrder struct {
	Descending bool
	Compare    func(a, b Row) int
}

type rowCursor struct {
	// the cursor's epoch is cached, so the heap compares without reaching into the series
	at     int64
	point  int32
	series int32
}

// Rows yields the result's points one row at a time, merging its series, which must each be sorted
// by epoch. The Points yielded are the result's own and must not be modified.
func (r *Result) Rows(order RowOrder) iter.Seq[Row] {
	return func(yield func(Row) bool) {
		if r == nil {
			return
		}
		list := r.SeriesList
		cursors := make([]rowCursor, 0, len(list))
		for i, s := range list {
			if s == nil || len(s.Points) == 0 {
				continue
			}
			start := 0
			if order.Descending {
				start = len(s.Points) - 1
			}
			// #nosec G115 -- a series' length and a result's series count are far below 2^31
			cursors = append(cursors, rowCursor{at: int64(s.Points[start].Epoch), point: int32(start), series: int32(i)})
		}
		h := rowHeap{list: list, cursors: cursors, descending: order.Descending}
		h.init()
		if order.Compare == nil {
			// the heap breaks an epoch's ties by series order, so its rows need no buffering
			for len(h.cursors) > 0 {
				c := h.cursors[0]
				s := list[c.series]
				if !yield(Row{Series: s, Point: &s.Points[c.point], SeriesIndex: int(c.series)}) {
					return
				}
				if h.advance(0) {
					h.down(0)
				} else {
					h.pop()
				}
			}
			return
		}
		var group []Row
		for len(h.cursors) > 0 {
			at := h.cursors[0].at
			group = group[:0]
			// every series holding the current epoch contributes its point, and advances
			for len(h.cursors) > 0 && h.cursors[0].at == at {
				c := h.cursors[0]
				s := list[c.series]
				group = append(group, Row{Series: s, Point: &s.Points[c.point], SeriesIndex: int(c.series)})
				if h.advance(0) {
					h.down(0)
				} else {
					h.pop()
				}
			}
			if len(group) > 1 {
				slices.SortStableFunc(group, order.Compare)
			}
			for _, row := range group {
				if !yield(row) {
					return
				}
			}
		}
	}
}

type rowHeap struct {
	list       SeriesList
	cursors    []rowCursor
	descending bool
}

func (h *rowHeap) less(i, j int) bool {
	a, b := &h.cursors[i], &h.cursors[j]
	if a.at == b.at {
		return a.series < b.series
	}
	if h.descending {
		return a.at > b.at
	}
	return a.at < b.at
}

func (h *rowHeap) advance(i int) bool {
	c := &h.cursors[i]
	if h.descending {
		c.point--
	} else {
		c.point++
	}
	points := h.list[c.series].Points
	if c.point < 0 || int(c.point) >= len(points) {
		return false
	}
	c.at = int64(points[c.point].Epoch)
	return true
}

func (h *rowHeap) init() {
	for i := len(h.cursors)/2 - 1; i >= 0; i-- {
		h.down(i)
	}
}

func (h *rowHeap) down(i int) {
	n := len(h.cursors)
	for {
		smallest, left, right := i, 2*i+1, 2*i+2
		if left < n && h.less(left, smallest) {
			smallest = left
		}
		if right < n && h.less(right, smallest) {
			smallest = right
		}
		if smallest == i {
			return
		}
		h.cursors[i], h.cursors[smallest] = h.cursors[smallest], h.cursors[i]
		i = smallest
	}
}

func (h *rowHeap) pop() {
	last := len(h.cursors) - 1
	h.cursors[0] = h.cursors[last]
	h.cursors = h.cursors[:last]
	if last > 0 {
		h.down(0)
	}
}
