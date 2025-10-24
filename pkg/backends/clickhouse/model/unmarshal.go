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
	"encoding/csv"
	"io"
	"strconv"
	"strings"
	"time"

	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	dcsv "github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/csv"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/numbers"
	trstr "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

// dataStartRow is the index in a ClickHouse TSV at which the header rows have
// ended and the data rows have started. We use TSVWithNamesAndTypes so it's 2.
const dataStartRow = 2

// UnmarshalTimeseries converts a TSV blob into a Timeseries
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	buf := bytes.NewReader(data)
	return UnmarshalTimeseriesReader(buf, trq)
}

// parser is safe for concurrency
var parser = dcsv.NewParserMust(buildFieldDefinitions, typeToFieldDataType,
	parseTimeField, dataStartRow)

// UnmarshalTimeseriesReader converts a TSV blob into a Timeseries via io.Reader
func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	cr := csv.NewReader(reader)
	cr.Comma = '\t'
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	// for ClickHouse TSV responses, the first 2 rows are annotation rows that
	// describe the data fields and their types, while the fourth row is
	// the standard Header Names row.
	if len(rows) < dataStartRow {
		return nil, timeseries.ErrInvalidBody
	}
	ds, err := parser.ToDataSet(rows, trq)
	return ds, err
}

// buildFieldDefinitions is the FieldParserFunc passed to the Parser
func buildFieldDefinitions(rows [][]string,
	trq *timeseries.TimeRangeQuery) (timeseries.SeriesFields, error) {

	l := len(rows[dataStartRow-1])
	// the first 2 rows must have an identical # of cells
	for i := range dataStartRow - 1 {
		if len(rows[i]) != l {
			return timeseries.SeriesFields{}, timeseries.ErrInvalidBody
		}
	}
	var j, k int
	outTags := make(timeseries.FieldDefinitions, l)
	outVals := make(timeseries.FieldDefinitions, l)
	tfd := timeseries.FieldDefinition{OutputPosition: -1}
	for i := range l {
		fd := loadFieldDef(rows[0][i], rows[1][i], i, trq)
		if fd.OutputPosition < 0 {
			continue
		}
		switch fd.Role {
		case timeseries.RoleTag: // add to tags
			outTags[j] = fd
			j++
		case timeseries.RoleTimestamp: // set as timestamp
			tfd = fd
		case timeseries.RoleValue: // add to values
			outVals[k] = fd
			k++
		}
	}
	return timeseries.SeriesFields{Timestamp: tfd, Tags: outTags[:j],
		Values: outVals[:k], Untracked: make(timeseries.FieldDefinitions, 0),
		ResultNameCol: -1}, nil
}

// loadFieldDef returns a field definition from the name, datatype and group.
func loadFieldDef(fieldName, dataType string, col int,
	trq *timeseries.TimeRangeQuery) timeseries.FieldDefinition {
	fd := timeseries.FieldDefinition{
		Name:           fieldName,
		DataType:       typeToFieldDataType(dataType),
		SDataType:      dataType,
		OutputPosition: col,
		Role:           timeseries.RoleValue,
	}
	switch fieldName {
	case "":
		return timeseries.FieldDefinition{OutputPosition: -1}
	case trq.TimestampDefinition.Name:
		fd.Role = timeseries.RoleTimestamp
		switch fd.SDataType {
		case "DateTime":
			fd.DataType = timeseries.DateTimeSQL
		case "Date":
			fd.DataType = timeseries.DateSQL
		default:
			if trq.TimestampDefinition.DataType > timeseries.Uint64 &&
				(fd.DataType == timeseries.Unknown || fd.DataType == timeseries.Uint64) {
				fd.DataType = trq.TimestampDefinition.DataType
			}
		}
	default:
		for l := range trq.TagFieldDefintions {
			if trq.TagFieldDefintions[l].Name == fieldName {
				fd.Role = timeseries.RoleTag
				break
			}
		}
	}
	return fd
}

func stripSize(input string) string {
	if i := strings.Index(input, "("); i > 0 {
		return input[:i]
	}
	return input
}

// typeToFieldDataType is the DataTypeParserFunc passed to the Parser
func typeToFieldDataType(input string) timeseries.FieldDataType {
	input = stripSize(input)
	switch input {
	case "String", "UUID", "FixedString":
		return timeseries.String
	case "Int8", "Int16", "Int32", "Int64":
		return timeseries.Int64
	case "UInt8", "UInt16", "UInt32", "UInt64":
		return timeseries.Uint64
	case "Float32", "Float64", "Decimal", "Decimal32", "Decimal64", "Decimal128", "Decimal256":
		return timeseries.Float64
	case "DateTime", "DateTime64":
		return timeseries.DateTimeSQL
	case "Date":
		return timeseries.DateSQL
	case "Nothing":
		return timeseries.Null
	}
	return timeseries.Unknown
}

func parseTimeField(input string, tfd timeseries.FieldDefinition) (epoch.Epoch, error) {
	var timeLayout string
	input = strings.TrimSpace(input)
	parse := time.Parse
	switch tfd.DataType {
	case timeseries.DateTimeSQL:
		parse = parseClickHouseTimestamp
		timeLayout = lsql.SQLDateTimeLayout
	case timeseries.TimeSQL:
		timeLayout = lsql.SQLTimeLayout
	case timeseries.DateSQL:
		timeLayout = lsql.SQLDateLayout
	case timeseries.DateTimeRFC3339Nano:
		timeLayout = time.RFC3339Nano
	default:
		if !trstr.IsApparentSQLDateFormat(input) {
			if !numbers.IsStringUint(input) {
				return 0, timeseries.ErrInvalidTimeFormat
			}
			i, err := strconv.ParseInt(input, 10, 64)
			if err != nil {
				return 0, err
			}
			if len(input) >= 19 { // assume nanoseconds
				return epoch.Epoch(i), nil
			}
			if len(input) >= 16 { // assume microseconds
				return epoch.Epoch(i * 1000), nil
			}
			if len(input) >= 13 { // assume milliseconds
				return epoch.Epoch(i * 1000000), nil
			}
			if len(input) >= 10 { // assume seconds
				return epoch.Epoch(i * 1000000000), nil
			}
			return 0, timeseries.ErrInvalidTimeFormat
		}
		timeLayout = lsql.SQLDateTimeLayout
		parse = parseClickHouseTimestamp
	}
	t, err := parse(timeLayout, input)
	if err != nil {
		return 0, err
	}
	return epoch.Epoch(t.UnixNano()), nil
}

func parseClickHouseTimestamp(_, input string) (time.Time, error) {
	if !strings.Contains(input, ".") {
		return time.Parse(lsql.SQLDateTimeLayout, input)
	}
	// this splits the seconds+ part of the time from the sub-second part
	parts := strings.SplitN(input, ".", 2)
	if len(parts[1]) == 0 {
		return time.Parse(lsql.SQLDateTimeLayout, strings.TrimSuffix(input, "."))
	}
	if len(parts[1]) > 9 {
		// this truncates sub-nanosecond values if present
		parts[1] = parts[1][:9]
	}
	input = strings.Join(parts, ".")
	switch len(parts[1]) {
	case 1:
		return time.Parse(lsql.SQLDateTimeSubSec1Layout, input)
	case 2:
		return time.Parse(lsql.SQLDateTimeSubSec2Layout, input)
	case 3:
		return time.Parse(lsql.SQLDateTimeSubSec3Layout, input)
	case 4:
		return time.Parse(lsql.SQLDateTimeSubSec4Layout, input)
	case 5:
		return time.Parse(lsql.SQLDateTimeSubSec5Layout, input)
	case 6:
		return time.Parse(lsql.SQLDateTimeSubSec6Layout, input)
	case 7:
		return time.Parse(lsql.SQLDateTimeSubSec7Layout, input)
	case 8:
		return time.Parse(lsql.SQLDateTimeSubSec8Layout, input)
	case 9:
		return time.Parse(lsql.SQLDateTimeSubSec9Layout, input)
	}
	return time.Parse(lsql.SQLDateTimeLayout, input)
}
