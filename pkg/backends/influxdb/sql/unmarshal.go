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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// maxJSONLLineBytes bounds a single JSONL row; rows wider than this fail the
// unmarshal rather than silently truncating.
const maxJSONLLineBytes = 32 * 1024 * 1024

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
	decoder := json.NewDecoder(r)
	decoder.UseNumber()
	var raw []json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	var columns []string
	rows := make([]map[string]any, 0, len(raw))
	for i, message := range raw {
		if i == 0 {
			keys, err := orderedObjectKeys(message)
			if err != nil {
				return nil, err
			}
			columns = keys
		}
		row, err := decodeRow(message)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rowsToDataSet(columns, rows, trq)
}

func unmarshalJSONL(r io.Reader, trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	var columns []string
	var rows []map[string]any
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxJSONLLineBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if columns == nil {
			keys, err := orderedObjectKeys(line)
			if err != nil {
				return nil, err
			}
			columns = keys
		}
		row, err := decodeRow(line)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rowsToDataSet(columns, rows, trq)
}

func unmarshalCSV(r io.Reader, trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	cr := csv.NewReader(r)
	records, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, timeseries.ErrInvalidBody
	}
	// a header-only body is a valid empty result set
	headers := records[0]
	rows := make([]map[string]any, 0, len(records)-1)
	for _, record := range records[1:] {
		row := make(map[string]any, len(headers))
		for i, h := range headers {
			if i < len(record) {
				row[h] = record[i]
			}
		}
		rows = append(rows, row)
	}
	return rowsToDataSet(headers, rows, trq)
}

// decodeRow unmarshals one response object preserving numeric fidelity.
func decodeRow(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var row map[string]any
	if err := decoder.Decode(&row); err != nil {
		return nil, err
	}
	return row, nil
}

// orderedObjectKeys returns a JSON object's keys in document order, so column
// ordering survives the map-based row representation.
func orderedObjectKeys(raw []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, timeseries.ErrInvalidBody
	}
	var keys []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, timeseries.ErrInvalidBody
		}
		keys = append(keys, key)
		// Decode consumes the key's entire value, nested or not
		var discard json.RawMessage
		if err := decoder.Decode(&discard); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

func rowsToDataSet(columns []string, rows []map[string]any, trq *timeseries.TimeRangeQuery,
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

	// tag columns partition rows into series; they come from the analyzed
	// GROUP BY columns and identify each series for delta merging
	tagNames := make([]string, 0, len(trq.TagFieldDefintions))
	for _, fd := range trq.TagFieldDefintions {
		if slices.Contains(columns, fd.Name) {
			tagNames = append(tagNames, fd.Name)
		}
	}

	fieldNames := make([]string, 0, len(columns))
	for _, name := range columns {
		if name != tsName && !slices.Contains(tagNames, name) {
			fieldNames = append(fieldNames, name)
		}
	}

	tfd := timeseries.FieldDefinition{
		Name:     tsName,
		DataType: timeseries.DateTimeRFC3339Nano,
		Role:     timeseries.RoleTimestamp,
	}
	tagFields := make(timeseries.FieldDefinitions, len(tagNames))
	for i, name := range tagNames {
		tagFields[i] = timeseries.FieldDefinition{Name: name, Role: timeseries.RoleTag}
	}
	vfds := make(timeseries.FieldDefinitions, len(fieldNames))
	for i, name := range fieldNames {
		vfds[i] = timeseries.FieldDefinition{
			Name:           name,
			DataType:       detectFieldType(rows, name),
			OutputPosition: i,
			Role:           timeseries.RoleValue,
		}
	}

	seriesByKey := make(map[string]*dataset.Series)
	seriesKeys := make([]string, 0, 8)
	for _, row := range rows {
		ep, err := parseV3Timestamp(row[tsName])
		if err != nil {
			// a row without a parseable timestamp cannot be placed on the
			// time axis; skip it rather than emitting a zero-epoch point
			continue
		}
		var key string
		var tags dataset.Tags
		if len(tagNames) > 0 {
			tags = make(dataset.Tags, len(tagNames))
			parts := make([]string, len(tagNames))
			for i, name := range tagNames {
				parts[i] = tagString(row[name])
				tags[name] = parts[i]
			}
			key = strings.Join(parts, "\x00")
		} else {
			tags = dataset.Tags{}
		}
		series, ok := seriesByKey[key]
		if !ok {
			sh := dataset.SeriesHeader{
				Name:            "default",
				TimestampField:  tfd,
				TagFieldsList:   tagFields,
				ValueFieldsList: vfds,
				Tags:            tags,
				QueryStatement:  trq.Statement,
			}
			sh.CalculateSize()
			series = &dataset.Series{Header: sh}
			seriesByKey[key] = series
			seriesKeys = append(seriesKeys, key)
		}
		vals := make([]any, len(fieldNames))
		for j, name := range fieldNames {
			vals[j] = coerceValue(row[name], vfds[j].DataType)
		}
		series.Points = append(series.Points, dataset.Point{Epoch: ep, Values: vals})
	}

	slices.Sort(seriesKeys)
	seriesList := make(dataset.SeriesList, len(seriesKeys))
	for i, key := range seriesKeys {
		seriesList[i] = seriesByKey[key]
	}
	ds := &dataset.DataSet{
		TimeRangeQuery: trq,
		ExtentList:     timeseries.ExtentList{trq.Extent},
		Results:        dataset.Results{&dataset.Result{SeriesList: seriesList}},
	}
	return ds, nil
}

func tagString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

// v3TimestampLayouts are the string timestamp shapes InfluxDB 3 emits. The
// native v3 output is naive UTC without a zone suffix (2026-08-29T01:33:10);
// RFC3339 variants are accepted for robustness.
var v3TimestampLayouts = []string{
	"2006-01-02T15:04:05.999999999",
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999",
	"2006-01-02",
}

func parseV3Timestamp(v any) (epoch.Epoch, error) {
	switch t := v.(type) {
	case string:
		for _, layout := range v3TimestampLayouts {
			if ts, err := time.ParseInLocation(layout, t, time.UTC); err == nil {
				return epoch.Epoch(ts.UnixNano()), nil
			}
		}
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return epochFromInteger(n), nil
		}
		return 0, timeseries.ErrInvalidTimeFormat
	case float64:
		return epochFromInteger(int64(t)), nil
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0, err
		}
		return epochFromInteger(n), nil
	}
	return 0, timeseries.ErrInvalidTimeFormat
}

// epochFromInteger infers the epoch unit of an integer timestamp by magnitude:
// values below 1e11 are seconds, below 1e14 milliseconds, below 1e17
// microseconds, and nanoseconds beyond.
func epochFromInteger(value int64) epoch.Epoch {
	magnitude := value
	if magnitude < 0 {
		magnitude = -magnitude
	}
	switch {
	case magnitude < 100_000_000_000:
		return epoch.Epoch(value * int64(time.Second))
	case magnitude < 100_000_000_000_000:
		return epoch.Epoch(value * int64(time.Millisecond))
	case magnitude < 100_000_000_000_000_000:
		return epoch.Epoch(value * int64(time.Microsecond))
	default:
		return epoch.Epoch(value)
	}
}

// detectFieldType infers a column's type from its first non-null value.
func detectFieldType(rows []map[string]any, name string) timeseries.FieldDataType {
	for _, row := range rows {
		v := row[name]
		if v == nil {
			continue
		}
		switch t := v.(type) {
		case json.Number:
			if isIntegerNumber(t) {
				return timeseries.Int64
			}
			return timeseries.Float64
		case float64:
			return timeseries.Float64
		case bool:
			return timeseries.Bool
		case string:
			// CSV carries every value as a string; sniff numerics and bools
			if t == "" {
				continue
			}
			if _, err := strconv.ParseInt(t, 10, 64); err == nil {
				return timeseries.Int64
			}
			if _, err := strconv.ParseFloat(t, 64); err == nil {
				return timeseries.Float64
			}
			if _, err := strconv.ParseBool(t); err == nil {
				return timeseries.Bool
			}
			return timeseries.String
		default:
			return timeseries.String
		}
	}
	return timeseries.String
}

func isIntegerNumber(n json.Number) bool {
	s := n.String()
	return !strings.ContainsAny(s, ".eE")
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
		case json.Number:
			if f, err := t.Float64(); err == nil {
				return f
			}
		case string:
			if f, err := strconv.ParseFloat(t, 64); err == nil {
				return f
			}
		}
	case timeseries.Int64:
		switch t := v.(type) {
		case float64:
			return int64(t)
		case json.Number:
			if n, err := t.Int64(); err == nil {
				return n
			}
			// a column typed Int64 can still receive a float value later
			if f, err := t.Float64(); err == nil {
				return f
			}
		case string:
			if n, err := strconv.ParseInt(t, 10, 64); err == nil {
				return n
			}
			if f, err := strconv.ParseFloat(t, 64); err == nil {
				return f
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
		switch t := v.(type) {
		case string:
			return t
		case json.Number:
			return t.String()
		}
		return fmt.Sprint(v)
	}
	return v
}
