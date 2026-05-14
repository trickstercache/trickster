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

package sql

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// MarshalTimeseries converts a Timeseries into a v3 response body
func MarshalTimeseries(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, _ int,
) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := MarshalTimeseriesWriter(ts, rlo, 0, buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MarshalTimeseriesWriter converts a Timeseries into a v3 response body via io.Writer
func MarshalTimeseriesWriter(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, _ int, w io.Writer,
) error {
	if ts == nil {
		return timeseries.ErrUnknownFormat
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return timeseries.ErrUnknownFormat
	}
	var of byte
	if rlo != nil {
		of = rlo.OutputFormat
	}
	switch of {
	case iofmt.V3OutputJSONL:
		return marshalJSONL(w, ds)
	case iofmt.V3OutputCSV:
		return marshalCSV(w, ds)
	default:
		return marshalJSON(w, ds)
	}
}

func marshalJSON(w io.Writer, ds *dataset.DataSet) error {
	rows := dataSetToRows(ds)
	enc := json.NewEncoder(w)
	return enc.Encode(rows)
}

func marshalJSONL(w io.Writer, ds *dataset.DataSet) error {
	rows := dataSetToRows(ds)
	enc := json.NewEncoder(w)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	return nil
}

func marshalCSV(w io.Writer, ds *dataset.DataSet) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	for _, result := range ds.Results {
		for _, series := range result.SeriesList {
			// header row
			header := make([]string, 0, 1+len(series.Header.ValueFieldsList))
			header = append(header, series.Header.TimestampField.Name)
			for _, fd := range series.Header.ValueFieldsList {
				header = append(header, fd.Name)
			}
			if err := cw.Write(header); err != nil {
				return err
			}
			// data rows
			for _, pt := range series.Points {
				record := make([]string, 0, 1+len(pt.Values))
				record = append(record, time.Unix(0, int64(pt.Epoch)).UTC().Format(time.RFC3339Nano))
				for _, v := range pt.Values {
					record = append(record, formatValue(v))
				}
				if err := cw.Write(record); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func dataSetToRows(ds *dataset.DataSet) []map[string]any {
	var rows []map[string]any
	for _, result := range ds.Results {
		for _, series := range result.SeriesList {
			tsName := series.Header.TimestampField.Name
			if tsName == "" {
				tsName = DefaultTimestampField
			}
			for _, pt := range series.Points {
				row := make(map[string]any, 1+len(pt.Values))
				row[tsName] = time.Unix(0, int64(pt.Epoch)).UTC().Format(time.RFC3339Nano)
				for i, fd := range series.Header.ValueFieldsList {
					if i < len(pt.Values) {
						row[fd.Name] = pt.Values[i]
					}
				}
				rows = append(rows, row)
			}
		}
	}
	if rows == nil {
		return []map[string]any{}
	}
	return rows
}

func formatValue(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%g", t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", t)
	}
}
