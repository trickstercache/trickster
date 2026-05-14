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
	"bufio"
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
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// UnmarshalTimeseries converts a v3 response body into a Timeseries
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	return UnmarshalTimeseriesReader(bytes.NewReader(data), trq)
}

// UnmarshalTimeseriesReader converts a v3 response body into a Timeseries via io.Reader
func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	// peek at first byte to determine format
	br := bufio.NewReader(reader)
	b, err := br.Peek(1)
	if err != nil {
		return nil, timeseries.ErrInvalidBody
	}
	var of byte
	switch b[0] {
	case '[':
		of = iofmt.V3OutputJSON
	case '{':
		of = iofmt.V3OutputJSONL
	default:
		of = iofmt.V3OutputCSV
	}
	switch of {
	case iofmt.V3OutputJSONL:
		return unmarshalJSONL(br, trq)
	case iofmt.V3OutputCSV:
		return unmarshalCSV(br, trq)
	default:
		return unmarshalJSON(br, trq)
	}
}

func unmarshalJSON(r io.Reader, trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	var rows []map[string]any
	if err := json.NewDecoder(r).Decode(&rows); err != nil {
		return nil, err
	}
	return rowsToDataSet(rows, trq)
}

func unmarshalJSONL(r io.Reader, trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	var rows []map[string]any
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			continue
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rowsToDataSet(rows, trq)
}

func unmarshalCSV(r io.Reader, trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	cr := csv.NewReader(r)
	records, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) < 2 {
		return nil, timeseries.ErrInvalidBody
	}
	headers := records[0]
	var rows []map[string]any
	for _, record := range records[1:] {
		row := make(map[string]any, len(headers))
		for i, h := range headers {
			if i < len(record) {
				row[h] = record[i]
			}
		}
		rows = append(rows, row)
	}
	return rowsToDataSet(rows, trq)
}

func rowsToDataSet(rows []map[string]any, trq *timeseries.TimeRangeQuery,
) (*dataset.DataSet, error) {
	if len(rows) == 0 {
		ds := &dataset.DataSet{
			TimeRangeQuery: trq,
			ExtentList:     timeseries.ExtentList{trq.Extent},
			Results:        dataset.Results{&dataset.Result{SeriesList: dataset.SeriesList{}}},
		}
		return ds, nil
	}

	tsName := trq.TimestampDefinition.Name
	if tsName == "" {
		tsName = DefaultTimestampField
	}

	// determine field names from first row (stable ordering)
	var fieldNames []string
	for k := range rows[0] {
		if k != tsName {
			fieldNames = append(fieldNames, k)
		}
	}

	// build field definitions
	tfd := timeseries.FieldDefinition{
		Name:     tsName,
		DataType: timeseries.DateTimeRFC3339Nano,
		Role:     timeseries.RoleTimestamp,
	}
	vfds := make(timeseries.FieldDefinitions, len(fieldNames))
	for i, name := range fieldNames {
		vfds[i] = timeseries.FieldDefinition{
			Name:           name,
			DataType:       detectFieldType(rows[0][name]),
			OutputPosition: i,
			Role:           timeseries.RoleValue,
		}
	}

	points := make(dataset.Points, len(rows))
	for i, row := range rows {
		ep, err := parseV3Timestamp(row[tsName])
		if err != nil {
			continue
		}
		vals := make([]any, len(fieldNames))
		for j, name := range fieldNames {
			vals[j] = coerceValue(row[name], vfds[j].DataType)
		}
		points[i] = dataset.Point{
			Epoch:  ep,
			Values: vals,
		}
	}

	sh := dataset.SeriesHeader{
		Name:            "default",
		TimestampField:  tfd,
		ValueFieldsList: vfds,
		Tags:            map[string]string{},
	}
	series := &dataset.Series{
		Header: sh,
		Points: points,
	}
	result := &dataset.Result{
		SeriesList: dataset.SeriesList{series},
	}
	ds := &dataset.DataSet{
		TimeRangeQuery: trq,
		ExtentList:     timeseries.ExtentList{trq.Extent},
		Results:        dataset.Results{result},
	}
	return ds, nil
}

func parseV3Timestamp(v any) (epoch.Epoch, error) {
	switch t := v.(type) {
	case string:
		// try RFC3339 first (the common v3 format)
		if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return epoch.Epoch(ts.UnixNano()), nil
		}
		if ts, err := time.Parse(time.RFC3339, t); err == nil {
			return epoch.Epoch(ts.UnixNano()), nil
		}
		// try epoch seconds
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return epoch.Epoch(n * 1e9), nil
		}
		return 0, timeseries.ErrInvalidTimeFormat
	case float64:
		return epoch.Epoch(int64(t) * 1e9), nil
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0, err
		}
		return epoch.Epoch(n * 1e9), nil
	}
	return 0, timeseries.ErrInvalidTimeFormat
}

func detectFieldType(v any) timeseries.FieldDataType {
	switch v := v.(type) {
	case float64:
		return timeseries.Float64
	case bool:
		return timeseries.Bool
	case string:
		s := v
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			return timeseries.Float64
		}
		if _, err := strconv.ParseInt(s, 10, 64); err == nil {
			return timeseries.Int64
		}
		return timeseries.String
	}
	return timeseries.String
}

func coerceValue(v any, dt timeseries.FieldDataType) any {
	if v == nil {
		return nil
	}
	switch dt {
	case timeseries.Float64:
		switch t := v.(type) {
		case float64:
			return t
		case string:
			if f, err := strconv.ParseFloat(t, 64); err == nil {
				return f
			}
		}
	case timeseries.Int64:
		switch t := v.(type) {
		case float64:
			return int64(t)
		case string:
			if n, err := strconv.ParseInt(t, 10, 64); err == nil {
				return n
			}
		}
	case timeseries.Bool:
		switch t := v.(type) {
		case bool:
			return t
		case string:
			if b, err := strconv.ParseBool(t); err == nil {
				return b
			}
		}
	case timeseries.String:
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	return v
}
