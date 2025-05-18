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
	"bytes"
	"encoding/csv"
	"io"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	dcsv "github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/csv"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// dataStartRow is the index in a Flux CSV matrix at which the header rows have
// ended and the data rows have started
const dataStartRow = 4

// parser is safe for concurrency
var parser = dcsv.NewParserMust(buildFieldDefinitions, typeToFieldDataType,
	parseTimeField, dataStartRow)

// UnmarshalTimeseries converts a Flux CSV into a Timeseries
func UnmarshalTimeseries(data []byte,
	trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	buf := bytes.NewReader(data)
	return UnmarshalTimeseriesReader(buf, trq)
}

// UnmarshalTimeseriesReader converts a Flux CSV into a Timeseries via io.Reader
func UnmarshalTimeseriesReader(reader io.Reader,
	trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	rows, err := csv.NewReader(reader).ReadAll()
	if err != nil {
		return nil, err
	}
	// for Flux CSV responses, the first 3 rows are annotation rows that
	// describe the data fields and their types, while the fourth row is
	// the standard Header Names row.
	if len(rows) < dataStartRow {
		return nil, timeseries.ErrInvalidBody
	}
	for i := range dataStartRow - 1 {
		if len(rows[i]) == 0 || !strings.HasPrefix(rows[i][0], "#") {
			return nil, timeseries.ErrInvalidBody
		}
	}
	return parser.ToDataSet(rows, trq)
}

// buildFieldDefinitions is the FieldParserFunc passed to the Parser
func buildFieldDefinitions(rows [][]string,
	_ *timeseries.TimeRangeQuery) (timeseries.SeriesFields, error) {
	l := len(rows[dataStartRow-1])
	if l < dataStartRow {
		return timeseries.SeriesFields{}, timeseries.ErrInvalidBody
	}
	// the first 4 rows must have an identical # of cells
	for i := range dataStartRow - 1 {
		if len(rows[i]) != l {
			return timeseries.SeriesFields{}, timeseries.ErrInvalidBody
		}
	}
	var j, k, u int
	outTags := make(timeseries.FieldDefinitions, l)
	outVals := make(timeseries.FieldDefinitions, l)
	outUntracked := make(timeseries.FieldDefinitions, l)
	tfd := timeseries.FieldDefinition{OutputPosition: -1}
	for i := range l {
		if i == 0 {
			// empty column on flux CSVs
			outUntracked[u] = timeseries.FieldDefinition{}
			u++
			continue
		}
		fd := loadFieldDef(rows[3][i], rows[0][i], rows[1][i], rows[2][i], i)
		switch fd.Role {
		case timeseries.RoleTag: // add to tags
			outTags[j] = fd
			j++
		case timeseries.RoleTimestamp: // set as timestamp
			tfd = fd
		case timeseries.RoleUntracked: // skip untracked field
			outUntracked[u] = fd
			u++
		case timeseries.RoleValue: // add to values
			outVals[k] = fd
			k++
		}
	}
	return timeseries.SeriesFields{Timestamp: tfd, Tags: outTags[:j],
		Values: outVals[:k], Untracked: outUntracked[:u], ResultNameCol: 1}, nil
}

// loadFieldDef returns a field definition from the name, datatype and group.
func loadFieldDef(n, d, g, v string, pos int) timeseries.FieldDefinition {
	fd := timeseries.FieldDefinition{
		Name:           n,
		DataType:       typeToFieldDataType(d),
		SDataType:      d,
		DefaultValue:   v,
		OutputPosition: pos,
		Role:           timeseries.RoleValue,
	}
	switch {
	case n == stopColumnName || n == startColumnName || n == tableColumnName:
		fd.Role = timeseries.RoleUntracked // is an untracked field
	case n == timeColumnName || n == timeAltColumnName:
		fd.Role = timeseries.RoleTimestamp // is a timestamp field
	case n == resultColumnName || g == sTrue:
		fd.Role = timeseries.RoleTag // is a key/tag field
	}
	return fd
}

// typeToFieldDataType is the DataTypeParserFunc passed to the Parser
func typeToFieldDataType(input string) timeseries.FieldDataType {
	switch input {
	case TypeString:
		return timeseries.String
	case TypeLong:
		return timeseries.Int64
	case TypeUnsignedLong:
		return timeseries.Uint64
	case TypeDouble:
		return timeseries.Float64
	case TypeBool:
		return timeseries.Bool
	case TypeDuration:
		return timeseries.String
	case TypeRFC3339:
		return timeseries.DateTimeRFC3339
	case TypeRFC3339Nano, timeColumnName:
		return timeseries.DateTimeRFC3339Nano
	case TypeNull:
		return timeseries.Null
	}
	return timeseries.Unknown
}

func parseTimeField(input string, tfd timeseries.FieldDefinition) (epoch.Epoch, error) {
	var f string
	switch tfd.DataType {
	case timeseries.DateTimeRFC3339:
		f = time.RFC3339
	case timeseries.DateTimeRFC3339Nano:
		f = time.RFC3339Nano
	default:
		return 0, timeseries.ErrInvalidTimeFormat
	}
	t, err := time.Parse(f, input)
	if err != nil {
		return 0, err
	}
	return epoch.Epoch(t.UnixNano()), nil
}
