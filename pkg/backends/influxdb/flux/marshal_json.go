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
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type WFDocument struct {
	*dataset.DataSet
}

func marshalTimeseriesJSONWriter(ds *dataset.DataSet,
	frb *JSONRequestBody, status int, w io.Writer) error {
	if hw, ok := w.(http.ResponseWriter); ok {
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
		hw.WriteHeader(status)
	}
	return writeJSON(ds, w)
}

func writeJSON(ds *dataset.DataSet, w io.Writer) error {
	w.Write([]byte(`{"results":[`))
	for i, r := range ds.Results {
		w.Write([]byte(`{"tables":[`))
		for j, s := range r.SeriesList {
			w.Write([]byte(`{"columns":[`))
			fds := s.Header.FieldDefinitions()
			setStartStopTimes(fds, ds.TimeRangeQuery.Extent)
			for k, c := range fds {
				if k == 0 {
					continue
				}
				nb, _ := json.Marshal(c.Name)
				tb, _ := json.Marshal(c.SDataType)
				w.Write([]byte(`{"name":`))
				w.Write(nb)
				w.Write([]byte(`,"datatype":`))
				w.Write(tb)
				w.Write([]byte(`}`))
				if k < len(fds)-1 {
					w.Write([]byte(`,`))
				}
			}
			w.Write([]byte(`],"records":[`))
			for k, c := range s.Points {
				w.Write([]byte(`{"values":{`))
				var o int
				for n, fd := range fds {
					if n == 0 {
						continue
					}
					b, _ := json.Marshal(fd.Name)
					w.Write(b)
					w.Write([]byte{':'})
					var usedValue bool
					b, usedValue = getCellValue(s.Header, fd, c, i, j)
					w.Write(b)
					if usedValue {
						o++
					}
					if n < len(fds)-1 {
						w.Write([]byte(`,`))
					}
				}
				w.Write([]byte(`}}`))
				if k < len(s.Points)-1 {
					w.Write([]byte(`,`))
				}
			}
			w.Write([]byte(`]}`))
			if j < len(r.SeriesList)-1 {
				w.Write([]byte(`,`))
			}
		}
		w.Write([]byte(`]}`))
		if i < len(ds.Results)-1 {
			w.Write([]byte(`,`))
		}
	}
	w.Write([]byte(`]}`))
	return nil
}

func getFormattedTimestamp(e epoch.Epoch, tfd timeseries.FieldDefinition) any {
	switch tfd.DataType {
	case timeseries.DateTimeRFC3339:
		return time.Unix(0, int64(e)).UTC().Format(time.RFC3339)
	case timeseries.DateTimeRFC3339Nano:
		return time.Unix(0, int64(e)).UTC().Format(time.RFC3339Nano)
	}
	return e
}

func getCellValue(sh dataset.SeriesHeader, fd timeseries.FieldDefinition,
	c dataset.Point, nextValue, table int) ([]byte, bool) {
	switch fd.Role {
	case timeseries.RoleTimestamp:
		b, _ := json.Marshal(getFormattedTimestamp(c.Epoch, fd))
		return b, false
	case timeseries.RoleTag:
		val := sh.Tags[fd.Name]
		if val == "" {
			val = fd.DefaultValue
		}
		b, _ := json.Marshal(val)
		return b, false
	case timeseries.RoleValue:
		if nextValue < len(c.Values) {
			if c.Values[nextValue] == nil {
				b, _ := json.Marshal(fd.DefaultValue)
				return b, true
			} else if s, ok := c.Values[nextValue].(string); ok && s == "" {
				b, _ := json.Marshal(fd.DefaultValue)
				return b, true
			} else {
				b, _ := json.Marshal(c.Values[nextValue])
				return b, true
			}
		}
	case timeseries.RoleUntracked:
		switch fd.Name {
		case tableColumnName:
			b, _ := json.Marshal(table)
			return b, false
		case startColumnName, stopColumnName:
			if strings.Contains(fd.DefaultValue, ":") {
				b, _ := json.Marshal(fd.DefaultValue)
				return b, true
			}
			return []byte(fd.DefaultValue), false
		}
	}
	b, _ := json.Marshal(fd.DefaultValue)
	return b, false
}
