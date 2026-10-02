/*
 * Copyright 2026 The Trickster Authors
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

package prometheus

import (
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// The TSM finalizers read and rebuild series in place, by Segment and row, rather than through boxed
// Points. A sample's value is its first column, held as the text Prometheus wrote.

// sampleFloat returns a row's sample text parsed as a float; a row whose first value isn't text has none
func sampleFloat(seg *dataset.Segment, i int) (float64, bool) {
	if seg.NumCols() == 0 || seg.KindAt(0, i) != dataset.KindString {
		return 0, false
	}
	f, err := strconv.ParseFloat(seg.Text(0, i), 64)
	return f, err == nil
}

// sampleNumber returns a row's first value as a float: text parsed, or a float as it's held
func sampleNumber(seg *dataset.Segment, i int) (float64, bool) {
	if seg.NumCols() == 0 {
		return 0, false
	}
	switch seg.KindAt(0, i) {
	case dataset.KindString:
		f, err := strconv.ParseFloat(seg.Text(0, i), 64)
		return f, err == nil
	case dataset.KindFloat64:
		return seg.Float64(0, i), true
	case dataset.KindExt:
		if f, ok := seg.Value(0, i).(float32); ok {
			return float64(f), true
		}
	}
	return 0, false
}

// rowCursor reads a series' rows in the order it holds them; n is the row's place in the series
type rowCursor struct {
	segs dataset.Segments
	k, i int
	n    int
}

func newRowCursor(segs dataset.Segments) rowCursor {
	c := rowCursor{segs: segs}
	c.skipEmpty()
	return c
}

func (c *rowCursor) done() bool {
	return c.k >= len(c.segs)
}

func (c *rowCursor) seg() *dataset.Segment {
	return &c.segs[c.k]
}

func (c *rowCursor) epoch() epoch.Epoch {
	return c.segs[c.k].Epoch(c.i)
}

func (c *rowCursor) next() {
	c.i++
	c.n++
	c.skipEmpty()
}

func (c *rowCursor) skipEmpty() {
	for c.k < len(c.segs) && c.i >= c.segs[c.k].Len() {
		c.k++
		c.i = 0
	}
}

// rewriteFirstValues returns each series' rows that rewrite keeps, the first value replaced by the text it
// appends to dst, laid out together; a series keeping none has nil
func rewriteFirstValues(list dataset.SeriesList,
	rewrite func(series *dataset.Series, seg *dataset.Segment, i int, dst []byte) ([]byte, bool),
) []dataset.Segments {
	log := dataset.NewColumnLog(dataset.DuplicatesKeep)
	ids := make([]int, len(list))
	var text []byte
	for si, series := range list {
		ids[si] = -1
		if series == nil {
			continue
		}
		segs := series.Segments()
		// every row is as wide as the series' first, as its Points are
		width := segs.NumCols()
		ids[si] = log.AddSeries(max(width, 1))
		for k := range segs {
			seg := &segs[k]
			for i := range seg.Len() {
				var keep bool
				if text, keep = rewrite(series, seg, i, text[:0]); !keep {
					continue
				}
				log.AddString(text)
				for c := 1; c < width; c++ {
					if c < seg.NumCols() {
						log.AddValue(seg.Value(c, i))
					} else {
						log.AddNull()
					}
				}
				// a row as wide as its series' can't fail
				_ = log.Commit(ids[si], seg.Epoch(i))
			}
		}
	}
	laid, err := log.Finish()
	out := make([]dataset.Segments, len(list))
	if err != nil {
		return out
	}
	for si, id := range ids {
		if id >= 0 && laid[id].Len() > 0 {
			out[si] = dataset.Segments{laid[id]}
		}
	}
	return out
}

// textSeriesLog lays out aggregated series, a float per row written as its text
type textSeriesLog struct {
	log  *dataset.ColumnLog
	text []byte
}

func newTextSeriesLog() *textSeriesLog {
	return &textSeriesLog{log: dataset.NewColumnLog(dataset.DuplicatesKeep)}
}

func (l *textSeriesLog) addSeries() int {
	return l.log.AddSeries(1)
}

func (l *textSeriesLog) add(series int, e epoch.Epoch, v float64) {
	l.text = strconv.AppendFloat(l.text[:0], v, 'f', -1, 64)
	l.log.AddString(l.text)
	// a row of the one value an added series takes can't fail
	_ = l.log.Commit(series, e)
}

// finish returns each series' rows, nil for one without any
func (l *textSeriesLog) finish() []dataset.Segments {
	laid, err := l.log.Finish()
	out := make([]dataset.Segments, len(laid))
	if err != nil {
		return out
	}
	for i := range laid {
		if laid[i].Len() > 0 {
			out[i] = dataset.Segments{laid[i]}
		}
	}
	return out
}
