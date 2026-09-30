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

package flux

import (
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

type state struct {
	s, prev    *dataset.Series
	fds        timeseries.FieldDefinitions
	cells      []csvCell
	k          int
	e          timeseries.Extent
	w          *csvRecords
	h, t, g, d bool
}

// writes records as encoding/csv's Writer writes them with its defaults
type csvRecords struct {
	cw tbytes.ChunkWriter
}

func (r *csvRecords) Write(record []string) error {
	for i, field := range record {
		if i > 0 {
			r.cw.Buf = append(r.cw.Buf, ',')
		}
		r.cw.Buf = tstrings.AppendCSVField(r.cw.Buf, field, ',')
	}
	r.cw.Buf = append(r.cw.Buf, '\n')
	r.cw.FlushIfFull()
	return r.cw.Err()
}

func marshalTimeseriesCSVWriter(ds *dataset.DataSet, frb *JSONRequestBody,
	status int, w io.Writer,
) error {
	if hw, ok := w.(http.ResponseWriter); ok {
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationCSV)
		hw.WriteHeader(status)
	}
	st := &state{
		e: rangeExtent(ds.TimeRangeQuery),
		w: &csvRecords{cw: tbytes.NewChunkWriter(w)},
	}
	for _, s := range frb.Dialect.Annotations {
		switch s {
		case AnnotationDatatype:
			st.t = true
		case AnnotationGroup:
			st.g = true
		case AnnotationDefault:
			st.d = true
		}
	}
	st.h = frb.Dialect.Header == nil || *frb.Dialect.Header
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s != nil {
				st.s = s
				processCsvSeriesData(st)
			}
			st.k++
		}
	}
	return st.w.cw.Close()
}

// printCsvAnnotationRow is a generic helper function for printing CSV annotation rows
func printCsvAnnotationRow(w *csvRecords,
	fds timeseries.FieldDefinitions,
	annotationType string,
	getValue func(timeseries.FieldDefinition) string,
) error {
	cells := make([]string, len(fds))
	for i, fd := range fds {
		if i == 0 {
			cells[i] = annotationType
			continue
		}
		cells[i] = getValue(fd)
	}
	return w.Write(cells)
}

func printCsvDatatypeAnnotationRow(w *csvRecords,
	fds timeseries.FieldDefinitions,
) error {
	return printCsvAnnotationRow(w, fds, "#datatype", func(fd timeseries.FieldDefinition) string {
		return fd.SDataType
	})
}

func printCsvGroupAnnotationRow(w *csvRecords,
	fds timeseries.FieldDefinitions,
) error {
	cells := make([]string, len(fds))
	for i, fd := range fds {
		if i == 0 {
			cells[i] = "#group"
			continue
		}
		if (fd.Role == timeseries.RoleTag && fd.Name != resultColumnName) ||
			fd.Name == startColumnName ||
			fd.Name == stopColumnName {
			cells[i] = sTrue
		} else {
			cells[i] = sFalse
		}
	}
	return w.Write(cells)
}

func printCsvDefaultAnnotationRow(w *csvRecords,
	fds timeseries.FieldDefinitions,
) error {
	return printCsvAnnotationRow(w, fds, "#default", func(fd timeseries.FieldDefinition) string {
		return fd.DefaultValue
	})
}

func printCsvHeaderRow(w *csvRecords, fds timeseries.FieldDefinitions) error {
	cells := make([]string, len(fds))
	for i, fd := range fds {
		if i == 0 {
			continue
		}
		cells[i] = fd.Name
	}
	return w.Write(cells)
}

func processSeriesHeader(st *state) {
	if st == nil || st.s == nil {
		return
	}
	st.fds = st.s.Header.FieldDefinitions()
	if st.prev != nil {
		_ = st.w.Write(nil)
	}
	if st.t {
		if err := printCsvDatatypeAnnotationRow(st.w, st.fds); err != nil {
			logger.Error("failed to write csv datatype annotation row",
				logging.Pairs{keys.Error: err})
		}
	}
	if st.g {
		if err := printCsvGroupAnnotationRow(st.w, st.fds); err != nil {
			logger.Error("failed to write csv group annotation row",
				logging.Pairs{keys.Error: err})
		}
	}
	if st.d {
		if err := printCsvDefaultAnnotationRow(st.w, st.fds); err != nil {
			logger.Error("failed to write csv default annotation row",
				logging.Pairs{keys.Error: err})
		}
	}
	if st.h {
		if err := printCsvHeaderRow(st.w, st.fds); err != nil {
			logger.Error("failed to write csv header row",
				logging.Pairs{keys.Error: err})
		}
	}
	setStartStopTimes(st.fds, st.e)
	st.prev = st.s
}

func processCsvSeriesData(st *state) {
	processSeriesHeader(st)
	st.cells = slices.Grow(st.cells[:0], len(st.fds))[:len(st.fds)]
	segs := st.s.Segments()
	for k := range segs {
		for i := range segs[k].Len() {
			processCsvRowData(st, &segs[k], i)
		}
	}
}

// one cell of a data row: text written as it is, or a row's time, value (by column) or table number
type csvCell struct {
	kind byte
	text string
	fd   *timeseries.FieldDefinition
	col  int
}

const (
	csvCellText byte = iota
	csvCellTime
	csvCellValue
	csvCellTable
)

func processCsvRowData(st *state, seg *dataset.Segment, row int) {
	clear(st.cells)
	// the fields fill the positions in their own order, a later one replacing an earlier
	var o int
	for i := range st.fds {
		fd := &st.fds[i]
		c, usedVal := csvCellFor(st.s.Header.Tags, fd, seg, row, o)
		if usedVal {
			o++
		}
		if fd.OutputPosition >= 0 && fd.OutputPosition < len(st.cells) {
			st.cells[fd.OutputPosition] = c
		}
	}
	b := st.w.cw.Buf
	for i := range st.cells {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendCsvCell(b, &st.cells[i], seg, row, st.k)
	}
	b = append(b, '\n')
	st.w.cw.Buf = b
	st.w.cw.FlushIfFull()
}

func csvCellFor(tags dataset.Tags, fd *timeseries.FieldDefinition, seg *dataset.Segment, row, nextValue int,
) (csvCell, bool) {
	switch fd.Role {
	case timeseries.RoleTimestamp:
		return csvCell{kind: csvCellTime, fd: fd}, false
	case timeseries.RoleTag:
		return csvCell{text: tags[fd.Name]}, false
	case timeseries.RoleValue:
		if nextValue < seg.NumCols() {
			// a null or empty text takes the field's default
			if k := seg.KindAt(nextValue, row); k == dataset.KindNull ||
				k == dataset.KindString && len(seg.Bytes(nextValue, row)) == 0 {
				return csvCell{text: fd.DefaultValue}, true
			}
			return csvCell{kind: csvCellValue, col: nextValue}, true
		}
	case timeseries.RoleUntracked:
		if fd.Name == tableColumnName {
			return csvCell{kind: csvCellTable}, false
		}
	}
	return csvCell{text: fd.DefaultValue}, false
}

// appends the cell as fmt's %v writes it, quoted as encoding/csv quotes it; a time or a number
// holds nothing that needs quoting
func appendCsvCell(b []byte, c *csvCell, seg *dataset.Segment, row, table int) []byte {
	switch c.kind {
	case csvCellTime:
		e := seg.Epoch(row)
		switch c.fd.DataType {
		case timeseries.DateTimeRFC3339:
			return time.Unix(0, int64(e)).UTC().AppendFormat(b, time.RFC3339)
		case timeseries.DateTimeRFC3339Nano:
			return time.Unix(0, int64(e)).UTC().AppendFormat(b, time.RFC3339Nano)
		}
		return strconv.AppendInt(b, int64(e), 10)
	case csvCellTable:
		return strconv.AppendInt(b, int64(table), 10)
	case csvCellValue:
		if out, ok := seg.AppendFormatted(b, c.col, row); ok {
			return out
		}
		return tstrings.AppendCSVField(b, seg.FormatText(c.col, row), ',')
	}
	return tstrings.AppendCSVField(b, c.text, ',')
}
