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
	_ *timeseries.RequestOptions, _ int,
) error {
	fds, _, _, _ := ds.FieldDefinitions()
	if hw, ok := w.(http.ResponseWriter); ok && hw != nil {
		hw.Header().Set(formatHeader, "JSON")
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	}
	cw := tbytes.NewChunkWriter(w)
	appendJSONDocument(&cw, ds, fds)
	cw.Buf = append(cw.Buf, '\n')
	return cw.Close()
}

// one output position of a row, which is left out when it has no key
type jsonCell struct {
	key   string
	role  timeseries.FieldRole
	tag   string
	value any
}

// appends ds as encoding/json writes its WFDocument: each row holds its fields in output position
// order, named by the meta, with each value written as fmt's %v writes it
func appendJSONDocument(cw *tbytes.ChunkWriter, ds *dataset.DataSet, fds timeseries.FieldDefinitions) {
	n := len(fds)
	meta := make(WFMeta, n)
	for _, fd := range fds {
		if fd.OutputPosition >= 0 && fd.OutputPosition < n {
			meta[fd.OutputPosition] = WFMetaItem{Name: fd.Name, Type: fd.SDataType}
		}
	}
	cw.Buf = append(cw.Buf, `{"meta":[`...)
	b := cw.Buf
	for i, m := range meta {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '{')
		if m.Name != "" {
			b = append(b, `"name":`...)
			b = tstrings.AppendJSON(b, m.Name)
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
		b = append(b, `],"data":null,"rows":0}`...)
		cw.Buf = b
		return
	}
	b = append(b, `],"data":[`...)
	var tf timeseries.FieldDataType
	if ds.TimeRangeQuery != nil {
		tf = ds.TimeRangeQuery.TimestampDefinition.DataType
	}
	cells := make([]jsonCell, n)
	rows := 0
	for _, s := range ds.Results[0].SeriesList {
		if s == nil {
			continue
		}
		for pi := range s.PointCount() {
			p := s.PointAt(pi)
			clear(cells)
			// the fields fill the positions in their own order, a later one replacing an earlier
			var vi int
			for _, fd := range fds {
				at := fd.OutputPosition
				if at < 0 || at >= n {
					continue
				}
				switch fd.Role {
				case timeseries.RoleTimestamp:
					cells[at] = jsonCell{key: meta[at].Name, role: fd.Role}
				case timeseries.RoleTag:
					cells[at] = jsonCell{key: meta[at].Name, role: fd.Role, tag: s.Header.Tags[fd.Name]}
				case timeseries.RoleValue:
					if vi >= len(p.Values) {
						continue
					}
					cells[at] = jsonCell{key: meta[at].Name, role: fd.Role, value: p.Values[vi]}
					vi++
				}
			}
			if rows > 0 {
				b = append(b, ',')
			}
			rows++
			b = append(b, '{')
			sep := false
			for i := range cells {
				c := &cells[i]
				if c.key == "" {
					continue
				}
				if sep {
					b = append(b, ',')
				}
				sep = true
				b = tstrings.AppendJSON(b, c.key)
				b = append(b, ':')
				switch c.role {
				case timeseries.RoleTimestamp:
					// a formatted time holds nothing JSON escapes
					b = append(p.Epoch.AppendFormat(append(b, '"'), tf, false), '"')
				case timeseries.RoleTag:
					b = tstrings.AppendJSON(b, c.tag)
				default:
					b = appendValueString(b, c.value)
				}
			}
			b = append(b, '}')
			cw.Buf = b
			cw.FlushIfFull()
			b = cw.Buf
		}
	}
	b = append(b, `],"rows":`...)
	b = strconv.AppendInt(b, int64(rows), 10)
	b = append(b, '}')
	cw.Buf = b
}

// appends v as a JSON string of what fmt's %v writes for it
func appendValueString(b []byte, v any) []byte {
	switch t := v.(type) {
	case string:
		return tstrings.AppendJSON(b, t)
	case float64:
		return append(strconv.AppendFloat(append(b, '"'), t, 'g', -1, 64), '"')
	case float32:
		return append(strconv.AppendFloat(append(b, '"'), float64(t), 'g', -1, 32), '"')
	case int64:
		return append(strconv.AppendInt(append(b, '"'), t, 10), '"')
	case int:
		return append(strconv.AppendInt(append(b, '"'), int64(t), 10), '"')
	case uint64:
		return append(strconv.AppendUint(append(b, '"'), t, 10), '"')
	case bool:
		return append(strconv.AppendBool(append(b, '"'), t), '"')
	case nil:
		return append(b, `"<nil>"`...)
	}
	return tstrings.AppendJSON(b, fmt.Sprint(v))
}
