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
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

func (d WFDataItem) MarshalJSON() ([]byte, error) {
	buf := bytes.NewBuffer([]byte{'{'})
	var sep bool
	for _, e := range d {
		if sep {
			buf.Write([]byte{','})
		}
		kb, _ := json.Marshal(e.Key)
		vb, _ := json.Marshal(e.Value)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
		sep = true
	}
	buf.Write([]byte{'}'})
	return buf.Bytes(), nil
}

func marshalTimeseriesJSON(w io.Writer, ds *dataset.DataSet,
	rlo *timeseries.RequestOptions, _ int,
) error {
	fds, _, _, _ := ds.FieldDefinitions()
	opts := formatOptions(rlo)
	if hw, ok := w.(http.ResponseWriter); ok && hw != nil {
		hw.Header().Set(formatHeader, "JSON")
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
		hw.Header().Set(TimezoneHeader, opts.ZoneName())
	}
	cw := tbytes.NewChunkWriter(w)
	appendJSONDocument(&cw, ds, fds, opts)
	cw.Buf = append(cw.Buf, '\n')
	return cw.Close()
}

// appends ds as ClickHouse's JSON format holds it: the meta, then each row by time as an object of its
// fields in output position order, each value typed as its column's type writes it
func appendJSONDocument(cw *tbytes.ChunkWriter, ds *dataset.DataSet, fds timeseries.FieldDefinitions,
	opts *FormatOptions,
) {
	n := len(fds)
	meta := make(WFMeta, n)
	for _, fd := range fds {
		if fd.OutputPosition >= 0 && fd.OutputPosition < n {
			meta[fd.OutputPosition] = WFMetaItem{Name: fd.Name, Type: fd.SDataType}
		}
	}
	cw.Buf = append(cw.Buf, `{"meta":[`...)
	b := cw.Buf
	keys := make([][]byte, n)
	for i, m := range meta {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '{')
		if m.Name != "" {
			b = append(b, `"name":`...)
			b = tstrings.AppendJSON(b, m.Name)
			keys[i] = append(tstrings.AppendJSON(nil, m.Name), ':')
		}
		if m.Type != "" {
			if m.Name != "" {
				b = append(b, ',')
			}
			b = append(b, `"type":`...)
			b = tstrings.AppendJSON(b, m.Type)
		}
		b = append(b, '}')
	}
	if len(ds.Results) == 0 {
		b = append(b, `],"data":[],"rows":0}`...)
		cw.Buf = b
		return
	}
	b = append(b, `],"data":[`...)
	rows, _ := timeOrderedRows(ds.Results[0])
	layout := newOutLayout(fds, opts, len(ds.Results[0].SeriesList))
	written := 0
	for r := range rows {
		cells := layout.rowCells(r)
		if written > 0 {
			b = append(b, ',')
		}
		written++
		b = append(b, '{')
		sep := false
		for i := range cells {
			c := &cells[i]
			// a position without a name or a cell is left out
			if c.kind == cellNone || keys[i] == nil {
				continue
			}
			if sep {
				b = append(b, ',')
			}
			sep = true
			b = append(b, keys[i]...)
			b = appendJSONCell(b, c, r, opts)
		}
		b = append(b, '}')
		cw.Buf = b
		cw.FlushIfFull()
		b = cw.Buf
	}
	b = append(b, `],"rows":`...)
	b = strconv.AppendInt(b, int64(written), 10)
	b = append(b, '}')
	cw.Buf = b
}

// the JSON literal of a NULL, and of a NaN or an infinity unless they're quoted
const jsonNull = "null"

// appendJSONCell appends a cell as ClickHouse's JSON writes its column's type
func appendJSONCell(b []byte, c *outCell, r outputRow, opts *FormatOptions) []byte {
	f := c.f
	switch c.kind {
	case cellText:
		return tstrings.AppendJSON(b, c.text)
	case cellTime:
		switch f.class {
		case classDateTime:
			return append(f.appendTime(append(b, '"'), r.epoch(), opts.DateTimeFormat), '"')
		case classDate:
			return append(r.epoch().AppendFormat(append(b, '"'), timeseries.DateSQL, false), '"')
		}
		q := f.wide && opts.QuoteInt64
		return quoteIf(r.epoch().AppendFormat(quoteIf(b, q), f.fd.DataType, false), q)
	case cellTag:
		if c.null {
			return append(b, jsonNull...)
		}
		return appendJSONText(b, f, c.text, opts)
	}
	seg, col, i := r.seg, c.col, r.i
	switch seg.KindAt(col, i) {
	case dataset.KindNull:
		return append(b, jsonNull...)
	case dataset.KindFloat64:
		v := seg.Float64(col, i)
		if f.class == classDecimal {
			return quoteIf(strconv.AppendFloat(quoteIf(b, opts.QuoteDecimals), v, 'f', -1, 64), opts.QuoteDecimals)
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			if opts.QuoteDenormals {
				return append(appendFloat(append(b, '"'), v), '"')
			}
			return append(b, jsonNull...)
		}
		return appendFloat(b, v)
	case dataset.KindInt64:
		q := f.wide && opts.QuoteInt64
		return quoteIf(strconv.AppendInt(quoteIf(b, q), seg.Int64(col, i), 10), q)
	case dataset.KindUint64:
		q := f.wide && opts.QuoteInt64
		return quoteIf(strconv.AppendUint(quoteIf(b, q), seg.Uint64(col, i), 10), q)
	case dataset.KindBool:
		return strconv.AppendBool(b, seg.Bool(col, i))
	case dataset.KindString:
		return appendJSONText(b, f, seg.Text(col, i), opts)
	}
	out, err := seg.AppendJSON(b, col, i)
	if err != nil {
		return append(b, jsonNull...)
	}
	return out
}

// quoteIf appends a JSON string's quote when quoted, which opens or closes a quoted number
func quoteIf(b []byte, quoted bool) []byte {
	if quoted {
		return append(b, '"')
	}
	return b
}

// appendJSONText appends a tag's or value's text by its column's type: a number bare, a compound
// value as JSON, a DateTime in the request's zone and format, and other text as a JSON string
func appendJSONText(b []byte, f *outField, text string, opts *FormatOptions) []byte {
	switch f.class {
	case classNumber:
		if isJSONNumber(text) {
			q := f.wide && opts.QuoteInt64
			return quoteIf(append(quoteIf(b, q), text...), q)
		}
	case classFloat:
		if isJSONNumber(text) {
			return append(b, text...)
		}
		if v, err := strconv.ParseFloat(text, 64); err == nil && (math.IsNaN(v) || math.IsInf(v, 0)) {
			if opts.QuoteDenormals {
				return append(appendFloat(append(b, '"'), v), '"')
			}
			return append(b, jsonNull...)
		}
	case classDecimal:
		if isJSONNumber(text) {
			return quoteIf(append(quoteIf(b, opts.QuoteDecimals), text...), opts.QuoteDecimals)
		}
	case classBool:
		if text == "true" || text == "false" {
			return append(b, text...)
		}
	case classDateTime:
		return append(f.appendStoredTime(append(b, '"'), []byte(text), opts.DateTimeFormat), '"')
	case classCompound:
		if out, ok := appendLiteralJSON(b, text, tupleNames(f.fd.SDataType)); ok {
			return out
		}
	}
	if f.fixed > len(text) {
		text += strings.Repeat("\x00", f.fixed-len(text))
	}
	return tstrings.AppendJSON(b, text)
}

// isJSONNumber reports whether text is a JSON number: a sign, an integer without leading zeros, and an
// optional fraction and exponent
func isJSONNumber(text string) bool {
	i := 0
	if i < len(text) && text[i] == '-' {
		i++
	}
	digits := func() int {
		start := i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		return i - start
	}
	if n := digits(); n == 0 || (n > 1 && text[i-n] == '0') {
		return false
	}
	if i < len(text) && text[i] == '.' {
		i++
		if digits() == 0 {
			return false
		}
	}
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			i++
		}
		if digits() == 0 {
			return false
		}
	}
	return i == len(text)
}
