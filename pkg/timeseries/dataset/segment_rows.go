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
	"math"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// SegmentRow is one row of one of the lists SegmentRows merges.
type SegmentRow struct {
	// List is the position of the row's Segments among the lists
	List int
	// Seg holds the row, which must not be modified
	Seg *Segment
	// Index is the row's position within Seg
	Index int
}

// Epoch returns the row's epoch.
func (r SegmentRow) Epoch() epoch.Epoch {
	return r.Seg.epochs[r.Index]
}

// SegmentRowOrder orders the rows SegmentRows yields: by epoch, newest first when Descending, and
// within an epoch by Compare, or by list order when Compare is nil.
type SegmentRowOrder struct {
	Descending bool
	Compare    func(a, b SegmentRow) int
}

// a list's place in the merge; its key orders it, the epoch or, descending, the epoch's complement
type segmentCursor struct {
	key int64
	// the epochs of the Segment holding the cursor's row
	epochs []epoch.Epoch
	// the list's position among the lists, which breaks ties; a finished cursor's is maxRank
	rank int32
	seg  int32
	row  int32
	// the loser of the match at the tree node this cursor's index names, and during setup its winner
	loser, winner int32
}

const maxRank = math.MaxInt32

// SegmentRows yields the lists' rows one at a time, merging them by epoch; each list must be sorted.
func SegmentRows(lists []Segments, order SegmentRowOrder) iter.Seq[SegmentRow] {
	return func(yield func(SegmentRow) bool) {
		t := newSegmentTree(lists, order.Descending)
		if !t.init() {
			return
		}
		if order.Compare == nil {
			// the tree breaks an epoch's ties by list order, so its rows need no buffering
			for t.live > 0 {
				if !yield(t.top()) {
					return
				}
				t.next()
			}
			return
		}
		var group []SegmentRow
		for t.live > 0 {
			key := t.cursors[t.cursors[0].loser].key
			group = group[:0]
			// every list holding the current epoch contributes its row, and advances
			for t.live > 0 && t.cursors[t.cursors[0].loser].key == key {
				group = append(group, t.top())
				t.next()
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

// segmentTree merges lists with a tree of losers: node n > 0 holds the loser between its children 2n and
// 2n+1 (node k+i is cursor i), node 0 the winner, so advancing it replays ~log2(k) matches
type segmentTree struct {
	lists   []Segments
	cursors []segmentCursor
	live    int
	// the step through a list's rows, 1 or -1, and what an epoch is XORed with to make its key
	dir  int32
	mask int64
}

func newSegmentTree(lists []Segments, descending bool) segmentTree {
	if descending {
		// a descending key is the epoch's complement, which orders the epochs newest first
		return segmentTree{lists: lists, dir: -1, mask: -1}
	}
	return segmentTree{lists: lists, dir: 1}
}

// init starts a cursor on each list with rows, and plays the tree; it reports whether any has rows
func (t *segmentTree) init() bool {
	for i := range t.lists {
		if t.lists[i].Len() > 0 {
			t.live++
		}
	}
	if t.live == 0 {
		return false
	}
	t.cursors = make([]segmentCursor, 0, t.live)
	for i := range t.lists {
		// #nosec G115 -- list and segment counts are far below 2^31
		c := segmentCursor{rank: int32(i), seg: -1}
		if t.dir < 0 {
			c.seg = int32(len(t.lists[i])) // #nosec G115 -- as above
		}
		if t.lists[i].Len() > 0 && t.stepSegment(&c) {
			t.cursors = append(t.cursors, c)
		}
	}
	k := len(t.cursors)
	// #nosec G115 -- as above
	for n := k - 1; n > 0; n-- {
		a, b := t.winnerAt(2*n), t.winnerAt(2*n+1)
		if t.less(b, a) {
			a, b = b, a
		}
		t.cursors[n].winner, t.cursors[n].loser = a, b
	}
	t.cursors[0].loser = t.winnerAt(1)
	return true
}

// the winner of the matches below node p
func (t *segmentTree) winnerAt(p int) int32 {
	if k := len(t.cursors); p >= k {
		return int32(p - k) // #nosec G115 -- cursor counts are far below 2^31
	}
	return t.cursors[p].winner
}

func (t *segmentTree) less(a, b int32) bool {
	ca, cb := &t.cursors[a], &t.cursors[b]
	if ca.key != cb.key {
		return ca.key < cb.key
	}
	return ca.rank < cb.rank
}

func (t *segmentTree) top() SegmentRow {
	c := &t.cursors[t.cursors[0].loser]
	return SegmentRow{List: int(c.rank), Seg: &t.lists[c.rank][c.seg], Index: int(c.row)}
}

// next advances the winning cursor, or finishes it, and replays its matches up to the root
func (t *segmentTree) next() {
	w := t.cursors[0].loser
	// the cursor steps within its Segment, or else to the next with rows, or finishes
	if c := &t.cursors[w]; c.row+t.dir >= 0 && int(c.row+t.dir) < len(c.epochs) {
		c.row += t.dir
		c.key = int64(c.epochs[c.row]) ^ t.mask
	} else if !t.stepSegment(c) {
		c.key, c.rank = math.MaxInt64, maxRank
		t.live--
	}
	for n := (int(w) + len(t.cursors)) / 2; n > 0; n /= 2 {
		if l := t.cursors[n].loser; t.less(l, w) {
			t.cursors[n].loser, w = w, l
		}
	}
	t.cursors[0].loser = w
}

// stepSegment moves c to the first row in the tree's direction of the list's next Segment with rows
func (t *segmentTree) stepSegment(c *segmentCursor) bool {
	l := t.lists[c.rank]
	for {
		if c.seg += t.dir; c.seg < 0 || int(c.seg) >= len(l) {
			return false
		}
		if n := len(l[c.seg].epochs); n > 0 {
			c.epochs, c.row = l[c.seg].epochs, 0
			if t.dir < 0 {
				c.row = int32(n - 1) // #nosec G115 -- row counts are far below 2^31
			}
			c.key = int64(c.epochs[c.row]) ^ t.mask
			return true
		}
	}
}

// NewestEpoch returns the nth newest distinct epoch across the lists, each sorted, and whether an
// older one exists; the lists are walked newest first together, collecting and sorting nothing.
func NewestEpoch(lists []Segments, n int) (epoch.Epoch, bool) {
	var only Segments
	withRows := 0
	for _, l := range lists {
		if l.Len() > 0 {
			only = l
			withRows++
		}
	}
	if withRows == 1 {
		return newestInList(only, n)
	}
	t := newSegmentTree(lists, true)
	if !t.init() {
		return 0, false
	}
	var last int64
	var at epoch.Epoch
	for distinct := 0; t.live > 0; t.next() {
		if key := t.cursors[t.cursors[0].loser].key; distinct == 0 || key != last {
			if distinct == n {
				return at, true
			}
			// descending keys are the epochs' complements
			last, at = key, epoch.Epoch(^key)
			distinct++
		}
	}
	return 0, false
}

// newestInList is NewestEpoch for one list, which needs no merge
func newestInList(l Segments, n int) (epoch.Epoch, bool) {
	var at epoch.Epoch
	distinct := 0
	for _, seg := range slices.Backward(l) {
		for _, e := range slices.Backward(seg.epochs) {
			if distinct == 0 || e != at {
				if distinct == n {
					return at, true
				}
				at = e
				distinct++
			}
		}
	}
	return 0, false
}
