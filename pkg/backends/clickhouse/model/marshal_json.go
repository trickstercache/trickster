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

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func (d WFDataItem) MarshalJSON() ([]byte, error) {
	buf := bytes.NewBuffer([]byte{'{'})
	var sep bool
	for _, e := range d {
		if sep {
			buf.Write([]byte{','})
		}
		fmt.Fprintf(buf, `"%s":"%s"`, e.Key, e.Value)
		sep = true
	}
	buf.Write([]byte{'}'})
	return buf.Bytes(), nil
}

func marshalTimeseriesJSON(w io.Writer, ds *dataset.DataSet,
	_ *timeseries.RequestOptions, _ int,
) error {
	wf, err := toWireFormat(ds)
	if err != nil {
		logger.Error("failed to convert dataset to clickhouse wire format",
			logging.Pairs{"error": err})
	}
	if hw, ok := w.(http.ResponseWriter); ok && hw != nil {
		hw.Header().Set(formatHeader, "JSON")
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	}
	return json.NewEncoder(w).Encode(wf)
}

func toWireFormat(ds *dataset.DataSet) (*WFDocument, error) {
	fds, _, _, _ := ds.FieldDefinitions()
	fieldCount := len(fds)
	d := &WFDocument{
		Meta: make(WFMeta, fieldCount),
	}
	for _, fd := range fds {
		if fd.OutputPosition >= fieldCount || fd.OutputPosition < 0 {
			continue
		}
		d.Meta[fd.OutputPosition] = WFMetaItem{
			Name: fd.Name,
			Type: fd.SDataType,
		}
	}
	var maxRowCount, k int
	if len(ds.Results) == 0 {
		d.Rows = &k
		return d, nil
	}
	for _, s := range ds.Results[0].SeriesList {
		maxRowCount += len(s.Points)
	}
	data := make(WFData, maxRowCount)
	for _, s := range ds.Results[0].SeriesList {
		for _, p := range s.Points {
			item := make(WFDataItem, fieldCount)
			var i int
			for _, fd := range fds {
				if fd.OutputPosition > fieldCount {
					continue
				}
				switch fd.Role {
				case timeseries.RoleTimestamp:
					item[fd.OutputPosition] = WFDataItemElement{
						Key:   d.Meta[fd.OutputPosition].Name,
						Value: p.Epoch.Format(ds.TimeRangeQuery.TimestampDefinition.DataType, false),
					}
				case timeseries.RoleTag:
					item[fd.OutputPosition] = WFDataItemElement{
						Key:   d.Meta[fd.OutputPosition].Name,
						Value: s.Header.Tags[fd.Name],
					}
				case timeseries.RoleValue:
					if i >= len(p.Values) {
						continue
					}
					item[fd.OutputPosition] = WFDataItemElement{
						Key:   d.Meta[fd.OutputPosition].Name,
						Value: fmt.Sprintf("%v", p.Values[i]),
					}
					i++
				}
			}
			var j int
			for i := range item {
				if item[i].Key == "" {
					continue
				}
				item[j] = item[i]
				j++
			}
			data[k] = item[:j]
			k++
		}
	}
	d.Data = data[:k]
	d.Rows = &k
	return d, nil
}
