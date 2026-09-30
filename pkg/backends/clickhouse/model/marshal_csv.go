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
	"fmt"
	"io"
	"net/http"
	"strconv"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

func marshalTimeseriesXSV(w io.Writer, ds *dataset.DataSet,
	_ *timeseries.RequestOptions, writeNames bool, writeTypes bool,
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

	if hw, ok := w.(http.ResponseWriter); ok && hw != nil {
		hw.Header().Set(headers.NameContentType, ctHeader)
		hw.Header().Set(formatHeader, fmtHeader)
	}

	if (len(tags) == 0 && len(vals) == 0) || tfd.DataType < 1 {
		return timeseries.ErrNoTimerangeQuery
	}

	if len(ds.Results) == 0 {
		return nil
	}

	fieldCount := len(fds)
	if tfd.OutputPosition > fieldCount {
		return timeseries.ErrTableHeader
	}

	// ClickHouse TSV never quotes; it escapes. CSV quotes as encoding/csv's Writer does.
	appendField := func(b []byte, field string) []byte {
		if separator == '\t' {
			return appendEscapedTSV(b, field)
		}
		return tstrings.AppendCSVField(b, field, separator)
	}
	cw := tbytes.NewChunkWriter(w)
	writeRow := func(row []string) {
		for i, cell := range row {
			if i > 0 {
				cw.Buf = append(cw.Buf, separator)
			}
			cw.Buf = appendField(cw.Buf, cell)
		}
		cw.Buf = append(cw.Buf, '\n')
	}

	// Helper function to write a row with field data
	writeFieldRow := func(getValue func(timeseries.FieldDefinition) string) {
		row := make([]string, fieldCount)
		set := func(fd timeseries.FieldDefinition) {
			if fd.OutputPosition >= 0 && fd.OutputPosition < fieldCount {
				row[fd.OutputPosition] = getValue(fd)
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
		writeRow(row)
	}

	if writeNames || writeTypes {
		writeFieldRow(func(fd timeseries.FieldDefinition) string { return fd.Name })
	}
	if writeTypes {
		writeFieldRow(func(fd timeseries.FieldDefinition) string { return fd.SDataType })
	}
	// each row's cells by position; a field that writes text keeps it, and the others reference it
	cells := make([]xsvCell, fieldCount)
	rows, _ := timeOrderedRows(ds.Results[0])
	for r := range rows {
		s, p := r.series, r.point
		clear(cells)
		var i int
		for f := range fds {
			fd := &fds[f]
			if fd.OutputPosition >= fieldCount || fd.OutputPosition < 0 {
				continue
			}
			switch fd.Role {
			case timeseries.RoleTimestamp:
				cells[fd.OutputPosition] = xsvCell{kind: xsvCellTime, fd: fd}
			case timeseries.RoleUntracked:
				if fd.DefaultValue != "" {
					cells[fd.OutputPosition] = xsvCell{text: fd.DefaultValue}
				}
			case timeseries.RoleTag:
				cells[fd.OutputPosition] = xsvCell{text: s.Header.Tags[fd.Name]}
			case timeseries.RoleValue:
				if i < len(p.Values) {
					cells[fd.OutputPosition] = xsvCell{kind: xsvCellValue, value: p.Values[i]}
					i++
				}
			}
		}
		b := cw.Buf
		for c := range cells {
			if c > 0 {
				b = append(b, separator)
			}
			b = appendXSVCell(b, &cells[c], p, appendField)
		}
		b = append(b, '\n')
		cw.Buf = b
		cw.FlushIfFull()
	}
	return cw.Close()
}

// one cell of a data row: text, or a point's time or value
type xsvCell struct {
	kind  byte
	text  string
	fd    *timeseries.FieldDefinition
	value any
}

const (
	xsvCellText byte = iota
	xsvCellTime
	xsvCellValue
)

// appends the cell as fmt's %v writes it; a time or a number holds nothing to quote or escape
func appendXSVCell(b []byte, c *xsvCell, p *dataset.Point, appendField func([]byte, string) []byte) []byte {
	switch c.kind {
	case xsvCellTime:
		return p.Epoch.AppendFormat(b, c.fd.DataType, false)
	case xsvCellValue:
		switch v := c.value.(type) {
		case string:
			return appendField(b, v)
		case float64:
			return strconv.AppendFloat(b, v, 'g', -1, 64)
		case float32:
			return strconv.AppendFloat(b, float64(v), 'g', -1, 32)
		case int64:
			return strconv.AppendInt(b, v, 10)
		case int:
			return strconv.AppendInt(b, int64(v), 10)
		case uint64:
			return strconv.AppendUint(b, v, 10)
		case bool:
			return strconv.AppendBool(b, v)
		case nil:
			return append(b, "<nil>"...)
		}
		return appendField(b, fmt.Sprint(c.value))
	}
	return appendField(b, c.text)
}
