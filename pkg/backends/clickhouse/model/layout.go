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
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// what an output position of a row holds
const (
	cellNone byte = iota
	cellTime
	cellTag
	cellValue
	cellText
)

// outCell is an output position of a series' rows: its time, a tag's text (or NULL), a value
// column, or fixed text
type outCell struct {
	kind byte
	f    *outField
	text string
	null bool
	col  int
}

// outLayout lays out a response's rows by output position, a series at a time
type outLayout struct {
	fds    timeseries.FieldDefinitions
	fields []outField
	// each series' cells, by its position in the result, and the value columns they were laid out for
	cells [][]outCell
	cols  []int
}

func newOutLayout(fds timeseries.FieldDefinitions, opts *FormatOptions, series int) *outLayout {
	l := &outLayout{
		fds: fds, fields: make([]outField, len(fds)), cells: make([][]outCell, series),
		cols: make([]int, series),
	}
	for i := range fds {
		l.fields[i] = newOutField(&fds[i], opts)
	}
	return l
}

// rowCells returns the cells of a row's series, laid out once for the series' value columns
func (l *outLayout) rowCells(r outputRow) []outCell {
	cols := r.seg.NumCols()
	if cells := l.cells[r.list]; cells != nil && l.cols[r.list] == cols {
		return cells
	}
	n := len(l.fds)
	cells := make([]outCell, n)
	// the fields fill the positions in their own order, a later one replacing an earlier
	var vi int
	for f := range l.fds {
		fd := &l.fds[f]
		at := fd.OutputPosition
		if at < 0 || at >= n {
			continue
		}
		c := outCell{f: &l.fields[f]}
		switch fd.Role {
		case timeseries.RoleTimestamp:
			c.kind = cellTime
		case timeseries.RoleTag:
			c.kind = cellTag
			c.text, c.null = tagText(r.series, fd)
		case timeseries.RoleValue:
			if vi >= cols {
				continue
			}
			c.kind, c.col = cellValue, vi
			vi++
		case timeseries.RoleUntracked:
			if fd.DefaultValue == "" {
				continue
			}
			c.kind, c.text = cellText, fd.DefaultValue
		default:
			continue
		}
		cells[at] = c
	}
	l.cells[r.list], l.cols[r.list] = cells, cols
	return cells
}

// tagText returns a series' tag, and whether it's NULL, which a tag the series doesn't hold is
func tagText(s *dataset.Series, fd *timeseries.FieldDefinition) (string, bool) {
	v, ok := s.Header.Tags[fd.Name]
	return v, !ok
}
