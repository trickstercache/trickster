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
	"io"
	"net/http"
	"strconv"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func marshalTimeseriesXSV(w io.Writer, ds *dataset.DataSet,
	rlo *timeseries.RequestOptions, writeNames bool, writeTypes bool,
	separator byte,
) error {
	fds, tags, vals, tfd := ds.FieldDefinitions()

	var ctPart, fmtPart string
	switch separator {
	case '\t':
		ctPart = "tab"
		fmtPart = "TSV"
	default:
		ctPart = "comma"
		fmtPart = "CSV"
		separator = ','
	}

	ctHeader := "text/" + ctPart + "-separated-values; charset=UTF-8"
	var fmtHeader string

	switch {
	case writeTypes:
		fmtHeader = fmtPart + "WithNamesAndTypes"
	case writeNames:
		fmtHeader = fmtPart + "WithNames"
	default:
		fmtHeader = fmtPart
	}

	opts := formatOptions(rlo)
	if hw, ok := w.(http.ResponseWriter); ok && hw != nil {
		hw.Header().Set(headers.NameContentType, ctHeader)
		hw.Header().Set(formatHeader, fmtHeader)
		hw.Header().Set(TimezoneHeader, opts.ZoneName())
	}

	// a response without rows has no fields to name, and is empty
	if len(ds.Results) == 0 || len(fds) == 0 {
		return nil
	}

	if (len(tags) == 0 && len(vals) == 0) || tfd.DataType < 1 {
		return timeseries.ErrNoTimerangeQuery
	}

	fieldCount := len(fds)
	if tfd.OutputPosition > fieldCount {
		return timeseries.ErrTableHeader
	}

	cw := tbytes.NewChunkWriter(w)
	// the header rows are text, which TSV escapes and CSV quotes
	writeFieldRow := func(name bool) {
		row := make([]string, fieldCount)
		set := func(fd timeseries.FieldDefinition) {
			if fd.OutputPosition >= 0 && fd.OutputPosition < fieldCount {
				if name {
					row[fd.OutputPosition] = fd.Name
				} else {
					row[fd.OutputPosition] = fd.SDataType
				}
			}
		}
		set(tfd)
		for _, fd := range tags {
			if fd.Name != tfd.Name {
				set(fd)
			}
		}
		for _, fd := range vals {
			set(fd)
		}
		for i, cell := range row {
			if i > 0 {
				cw.Buf = append(cw.Buf, separator)
			}
			cw.Buf = appendXSVText(cw.Buf, cell, 0, separator)
		}
		cw.Buf = append(cw.Buf, '\n')
	}
	if writeNames || writeTypes {
		writeFieldRow(true)
	}
	if writeTypes {
		writeFieldRow(false)
	}
	rows, _ := timeOrderedRows(ds.Results[0])
	layout := newOutLayout(fds, opts, len(ds.Results[0].SeriesList))
	for r := range rows {
		cells := layout.rowCells(r)
		b := cw.Buf
		for c := range cells {
			if c > 0 {
				b = append(b, separator)
			}
			b = appendXSVCell(b, &cells[c], r, opts.DateTimeFormat, separator)
		}
		b = append(b, '\n')
		cw.Buf = b
		cw.FlushIfFull()
	}
	return cw.Close()
}

// nullXSV is how TSV and CSV write a NULL
const nullXSV = nullToken

// appendXSVCell appends a cell as ClickHouse writes it in TSV or CSV
func appendXSVCell(b []byte, c *outCell, r outputRow, format, sep byte) []byte {
	switch c.kind {
	case cellNone:
		return b
	case cellText:
		return appendXSVText(b, c.text, 0, sep)
	case cellTime:
		f := c.f
		switch f.class {
		case classDateTime:
			b = quoteCSV(b, sep)
			return quoteCSV(f.appendTime(b, r.epoch(), format), sep)
		case classDate:
			b = quoteCSV(b, sep)
			return quoteCSV(r.epoch().AppendFormat(b, timeseries.DateSQL, false), sep)
		}
		return r.epoch().AppendFormat(b, f.fd.DataType, false)
	case cellTag:
		if c.null {
			return append(b, nullXSV...)
		}
		return appendXSVString(b, c.f, c.text, format, sep)
	}
	seg, col, i := r.seg, c.col, r.i
	switch seg.KindAt(col, i) {
	case dataset.KindNull:
		return append(b, nullXSV...)
	case dataset.KindFloat64:
		if c.f.class == classDecimal {
			return strconv.AppendFloat(b, seg.Float64(col, i), 'f', -1, 64)
		}
		return appendFloat(b, seg.Float64(col, i))
	case dataset.KindInt64:
		return strconv.AppendInt(b, seg.Int64(col, i), 10)
	case dataset.KindUint64:
		return strconv.AppendUint(b, seg.Uint64(col, i), 10)
	case dataset.KindBool:
		return strconv.AppendBool(b, seg.Bool(col, i))
	case dataset.KindString:
		return appendXSVString(b, c.f, seg.Text(col, i), format, sep)
	}
	return appendXSVText(b, seg.FormatText(col, i), 0, sep)
}

// appendXSVString appends a tag's or value's text by its type: a number bare, a DateTime in the
// request's zone and format, and other text escaped (TSV) or quoted (CSV)
func appendXSVString(b []byte, f *outField, text string, format, sep byte) []byte {
	switch f.class {
	case classNumber, classDecimal, classFloat, classBool:
		return append(b, text...)
	case classDateTime:
		b = quoteCSV(b, sep)
		return quoteCSV(f.appendStoredTime(b, []byte(text), format), sep)
	case classCompound:
		// TSV writes a compound's literal as it is, its elements escaped within it
		if sep == '\t' {
			return append(b, text...)
		}
	}
	return appendXSVText(b, text, f.fixed, sep)
}

// appendXSVText appends text padded with NULs to width, escaped as ClickHouse TSV escapes it or quoted
// as its CSV quotes every text
func appendXSVText(b []byte, text string, width int, sep byte) []byte {
	if sep == '\t' {
		b = appendEscapedTSV(b, text)
		for range width - len(text) {
			b = append(b, '\\', '0')
		}
		return b
	}
	b = append(b, '"')
	for i := range len(text) {
		if text[i] == '"' {
			b = append(b, '"')
		}
		b = append(b, text[i])
	}
	for range width - len(text) {
		b = append(b, 0)
	}
	return append(b, '"')
}

// quoteCSV appends CSV's quote, and nothing for TSV
func quoteCSV(b []byte, sep byte) []byte {
	if sep == '\t' {
		return b
	}
	return append(b, '"')
}
