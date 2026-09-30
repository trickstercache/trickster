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
	"encoding/csv"
	"io"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	dcsv "github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/csv"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// legacyParser is the parser the legacy decoder built each table with
var legacyParser = dcsv.NewParserMust(buildFieldDefinitions, typeToFieldDataType,
	legacyParseTimeField, dataStartRow)

// legacyUnmarshalTimeseriesReader is the decoder the stream decoder replaced, kept as its oracle
func legacyUnmarshalTimeseriesReader(reader io.Reader,
	trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	// Each Flux table has its own annotations and may have a different schema.
	cr := csv.NewReader(reader)
	cr.FieldsPerRecord = -1
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < dataStartRow {
		return nil, timeseries.ErrInvalidBody
	}
	var ds *dataset.DataSet
	var resultsByName map[string]*dataset.Result
	start := 0
	for i := 1; i <= len(rows); i++ {
		if i < len(rows) && (len(rows[i]) == 0 || rows[i][0] != "#datatype") {
			continue
		}
		table := rows[start:i]
		if len(table) < dataStartRow || len(table[0]) == 0 ||
			table[0][0] != "#datatype" || len(table[1]) == 0 ||
			table[1][0] != "#group" || len(table[2]) == 0 ||
			table[2][0] != "#default" {
			return nil, timeseries.ErrInvalidBody
		}
		parsed, err := legacyParser.ToDataSet(table, trq)
		if err != nil {
			return nil, err
		}
		if ds == nil {
			ds = parsed
		} else {
			if resultsByName == nil {
				resultsByName = make(map[string]*dataset.Result, len(ds.Results)+len(parsed.Results))
				for _, result := range ds.Results {
					resultsByName[result.Name] = result
				}
			}
			for _, result := range parsed.Results {
				if existing := resultsByName[result.Name]; existing != nil {
					existing.SeriesList = append(existing.SeriesList, result.SeriesList...)
				} else {
					ds.Results = append(ds.Results, result)
					resultsByName[result.Name] = result
				}
			}
		}
		start = i
	}
	return ds, nil
}

func legacyParseTimeField(input string, tfd timeseries.FieldDefinition) (epoch.Epoch, error) {
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
