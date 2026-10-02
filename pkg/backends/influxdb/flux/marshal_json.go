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
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

type WFDocument struct {
	*dataset.DataSet
}

func marshalTimeseriesJSONWriter(ds *dataset.DataSet,
	_ *JSONRequestBody, status int, w io.Writer,
) error {
	if hw, ok := w.(http.ResponseWriter); ok {
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
		hw.WriteHeader(status)
	}
	return writeJSON(ds, w)
}

// the JSON literal for a value JSON can't hold
const jsonNull = "null"

// what a record's cell holds: text fixed for its table, the row's time, or a value column's value
const (
	jsonCellFixed byte = iota
	jsonCellTime
	jsonCellValue
)

// jsonCell is one column of a table's records: its key, and its fixed text or where its value is
type jsonCell struct {
	kind byte
	key  []byte
	text []byte
	// a value's column among the row's values, or a time's type
	value int
	time  timeseries.FieldDataType
}

// writeJSON writes ds as tables of records, each series a table; a record holds a cell for every
// column, with a column's default for a null or empty value
func writeJSON(ds *dataset.DataSet, w io.Writer) error {
	cw := tbytes.NewChunkWriter(w)
	cw.Buf = append(cw.Buf, `{"results":[`...)
	var cells []jsonCell
	for i, r := range ds.Results {
		cw.Buf = append(cw.Buf, `{"tables":[`...)
		for j, s := range r.SeriesList {
			cells = appendJSONTable(&cw, ds, s, j, cells[:0])
			if j < len(r.SeriesList)-1 {
				cw.Buf = append(cw.Buf, ',')
			}
		}
		cw.Buf = append(cw.Buf, `]}`...)
		if i < len(ds.Results)-1 {
			cw.Buf = append(cw.Buf, ',')
		}
	}
	cw.Buf = append(cw.Buf, `]}`...)
	return cw.Close()
}

func appendJSONTable(cw *tbytes.ChunkWriter, ds *dataset.DataSet, s *dataset.Series, table int,
	cells []jsonCell,
) []jsonCell {
	fds := s.Header.FieldDefinitions()
	setStartStopTimes(fds, rangeExtent(ds.TimeRangeQuery))
	cw.Buf = append(cw.Buf, `{"columns":[`...)
	// the first column is the annotations' and has no cells
	values := 0
	for k := 1; k < len(fds); k++ {
		fd := &fds[k]
		cw.Buf = append(cw.Buf, `{"name":`...)
		cw.Buf = tstrings.AppendJSON(cw.Buf, fd.Name)
		cw.Buf = append(cw.Buf, `,"datatype":`...)
		cw.Buf = tstrings.AppendJSON(cw.Buf, fd.SDataType)
		cw.Buf = append(cw.Buf, '}')
		if k < len(fds)-1 {
			cw.Buf = append(cw.Buf, ',')
		}
		c := jsonCell{key: append(tstrings.AppendJSON(nil, fd.Name), ':'), text: tstrings.AppendJSON(nil, fd.DefaultValue)}
		switch fd.Role {
		case timeseries.RoleTimestamp:
			c.kind, c.time = jsonCellTime, fd.DataType
		case timeseries.RoleTag:
			if v := s.Header.Tags[fd.Name]; v != "" {
				c.text = tstrings.AppendJSON(nil, v)
			}
		case timeseries.RoleValue:
			c.kind, c.value = jsonCellValue, values
			values++
		case timeseries.RoleUntracked:
			switch fd.Name {
			case tableColumnName:
				c.text = strconv.AppendInt(nil, int64(table), 10)
			case startColumnName, stopColumnName:
				if !strings.Contains(fd.DefaultValue, ":") {
					c.text = []byte(fd.DefaultValue)
				}
			}
		}
		cells = append(cells, c)
	}
	cw.Buf = append(cw.Buf, `],"records":[`...)
	segs := s.Segments()
	rows := segs.Len()
	for k := range segs {
		seg := &segs[k]
		for i, e := range seg.Epochs() {
			b := cw.Buf
			b = append(b, `{"values":{`...)
			for n := range cells {
				c := &cells[n]
				b = append(b, c.key...)
				switch {
				case c.kind == jsonCellTime:
					b = appendJSONTime(b, e, c.time)
				case c.kind == jsonCellValue && c.value < seg.NumCols():
					b = appendJSONValue(b, seg, c.value, i, c.text)
				default:
					b = append(b, c.text...)
				}
				if n < len(cells)-1 {
					b = append(b, ',')
				}
			}
			b = append(b, `}}`...)
			if rows--; rows > 0 {
				b = append(b, ',')
			}
			cw.Buf = b
			cw.FlushIfFull()
		}
	}
	cw.Buf = append(cw.Buf, `]}`...)
	return cells
}

// appendJSONTime appends a row's time as a quoted RFC 3339 time, or for any other type as its epoch
func appendJSONTime(b []byte, e epoch.Epoch, dt timeseries.FieldDataType) []byte {
	if dt != timeseries.DateTimeRFC3339 && dt != timeseries.DateTimeRFC3339Nano {
		return strconv.AppendInt(b, int64(e), 10)
	}
	// the layouts write nothing JSON escapes
	b = append(b, '"')
	b = epoch.AppendCanonicalTime(b, e, dt == timeseries.DateTimeRFC3339Nano, true)
	return append(b, '"')
}

// appendJSONValue appends value c of row i as encoding/json writes it, the column's default for a
// null or empty text, and null for one JSON can't hold, such as NaN, as InfluxDB 3 writes it
func appendJSONValue(b []byte, seg *dataset.Segment, c, i int, dflt []byte) []byte {
	switch k := seg.KindAt(c, i); k {
	case dataset.KindNull:
		return append(b, dflt...)
	case dataset.KindString:
		text := seg.Bytes(c, i)
		if len(text) == 0 {
			return append(b, dflt...)
		}
		return tstrings.AppendJSON(b, string(text))
	case dataset.KindFloat64:
		if out, ok := tstrings.AppendJSONFloat(b, seg.Float64(c, i), 64); ok {
			return out
		}
		return append(b, jsonNull...)
	case dataset.KindInt64:
		return strconv.AppendInt(b, seg.Int64(c, i), 10)
	case dataset.KindUint64:
		return strconv.AppendUint(b, seg.Uint64(c, i), 10)
	case dataset.KindBool:
		return strconv.AppendBool(b, seg.Bool(c, i))
	}
	out, err := json.Marshal(seg.Value(c, i))
	if err != nil {
		return append(b, jsonNull...)
	}
	return append(b, out...)
}
