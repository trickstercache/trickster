/*
 * Copyright 2026 The Trickster Authors
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
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type column struct {
	Name string `json:"name"`
	Type string `json:"data_type"`
}

type schema struct {
	Columns []column `json:"column_schemas"`
}

type records struct {
	Schema  schema                     `json:"schema"`
	Rows    [][]any                    `json:"rows"`
	Total   *uint64                    `json:"total_rows"`
	Metrics map[string]json.RawMessage `json:"metrics,omitempty"`
}

type output struct {
	Records *records `json:"records"`
}

type response struct {
	Output        []output `json:"output"`
	ExecutionTime *uint64  `json:"execution_time_ms"`
}

func UnmarshalTimeseries(body []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return UnmarshalTimeseriesReader(bytes.NewReader(body), trq)
}

func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if reader == nil || trq == nil {
		return nil, timeseries.ErrInvalidBody
	}
	plan, ok := trq.ParsedQuery.(*sqlanalyzer.QueryPlan)
	if !ok || plan == nil || trq.Step <= 0 {
		return nil, timeseries.ErrInvalidBody
	}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var body response
	if err := decoder.Decode(&body); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || len(body.Output) != 1 || body.ExecutionTime == nil {
		return nil, timeseries.ErrInvalidBody
	}
	r := body.Output[0].Records
	if r == nil || r.Rows == nil || r.Total == nil || *r.Total != uint64(len(r.Rows)) || len(r.Metrics) != 0 {
		return nil, timeseries.ErrInvalidBody
	}
	fields, err := fieldDefinitions(r.Schema.Columns, plan)
	if err != nil {
		return nil, err
	}
	orderedFloats := make([]bool, len(fields))
	for i, field := range fields {
		orderedFloats[i] = field.DataType == timeseries.Float64 && slices.ContainsFunc(plan.Ordering, func(term timeseries.OrderTerm) bool {
			return term.Column == field.Name
		})
	}
	result := &dataset.Result{SeriesList: dataset.SeriesList{}}
	d := &dataSet{DataSet: &dataset.DataSet{
		TimeRangeQuery: trq, ExtentList: timeseries.ExtentList{trq.Extent}, Results: dataset.Results{result},
	}, fields: fields}
	byKey := make(map[string]*dataset.Series)
	for _, row := range r.Rows {
		if len(row) != len(fields) {
			return nil, timeseries.ErrInvalidBody
		}
		header := dataset.SeriesHeader{Name: "sql", Tags: dataset.Tags{}, QueryStatement: trq.Statement}
		point := dataset.Point{Size: 16}
		for i, field := range fields {
			// JSON null erases the distinction between SQL NULL and NaN/Inf,
			// which have different positions in DataFusion's numeric ordering.
			if orderedFloats[i] && row[i] == nil {
				return nil, timeseries.ErrInvalidBody
			}
			v, err := decodeValue(row[i], field.SDataType)
			if err != nil {
				return nil, err
			}
			switch field.Role {
			case timeseries.RoleTimestamp:
				ep, err := parseEpoch(row[i], field)
				if err != nil || !onGrid(ep, trq) {
					return nil, timeseries.ErrInvalidTimeFormat
				}
				point.Epoch = epoch.Epoch(ep)
				header.TimestampField = field
			case timeseries.RoleTag:
				encoded, err := json.Marshal(v)
				if err != nil {
					return nil, err
				}
				header.Tags[field.Name] = string(encoded)
				header.TagFieldsList = append(header.TagFieldsList, field)
			case timeseries.RoleValue:
				point.Values = append(point.Values, v)
				point.Size += 16
				if s, ok := v.(string); ok {
					point.Size += len(s)
				}
				header.ValueFieldsList = append(header.ValueFieldsList, field)
			}
		}
		key := header.Tags.JSON()
		series := byKey[key]
		if series == nil {
			header.CalculateSize()
			series = &dataset.Series{Header: header}
			byKey[key] = series
			result.SeriesList = append(result.SeriesList, series)
		}
		series.Points = append(series.Points, point)
		series.PointSize += int64(point.Size)
	}
	for _, series := range result.SeriesList {
		slices.SortFunc(series.Points, func(a, b dataset.Point) int {
			if a.Epoch < b.Epoch {
				return -1
			}
			if a.Epoch > b.Epoch {
				return 1
			}
			return 0
		})
		for i := 1; i < len(series.Points); i++ {
			if series.Points[i-1].Epoch == series.Points[i].Epoch {
				return nil, timeseries.ErrInvalidBody
			}
		}
	}
	return d, nil
}

func fieldDefinitions(columns []column, plan *sqlanalyzer.QueryPlan) (timeseries.FieldDefinitions, error) {
	roles := map[string]timeseries.FieldRole{plan.OutputColumn: timeseries.RoleTimestamp}
	for _, name := range plan.GroupColumns {
		roles[name] = timeseries.RoleTag
	}
	for _, name := range plan.ValueColumns {
		roles[name] = timeseries.RoleValue
	}
	if len(roles) > len(columns) || (len(plan.ValueColumns) > 0 && len(roles) != len(columns)) {
		return nil, timeseries.ErrInvalidBody
	}
	fields := make(timeseries.FieldDefinitions, len(columns))
	seen := make(map[string]bool, len(columns))
	values := 0
	for i, col := range columns {
		role, ok := roles[col.Name]
		// The Cockroach adapter currently names only timestamp/group fields.
		// Remaining columns are typed values supplied by the origin schema.
		if !ok && len(plan.ValueColumns) == 0 {
			role, ok = timeseries.RoleValue, true
		}
		typ, supported := fieldType(col.Type)
		if !ok || !supported || col.Name == "" || seen[col.Name] {
			return nil, timeseries.ErrInvalidBody
		}
		seen[col.Name] = true
		delete(roles, col.Name)
		field := timeseries.FieldDefinition{
			Name: col.Name, SDataType: col.Type,
			DataType: typ, Role: role, OutputPosition: i,
		}
		if role == timeseries.RoleTimestamp {
			field.ProviderData1 = byte(plan.OutputUnit)
			if axisScale(field) == 0 {
				return nil, timeseries.ErrInvalidTimeFormat
			}
		}
		fields[i] = field
		if role == timeseries.RoleValue {
			values++
		}
	}
	// The common dataset treats a series without values as empty when cropping.
	if len(roles) != 0 || values == 0 {
		return nil, timeseries.ErrInvalidBody
	}
	for _, term := range plan.Ordering {
		if !seen[term.Column] {
			return nil, timeseries.ErrInvalidBody
		}
	}
	return fields, nil
}

func onGrid(value int64, trq *timeseries.TimeRangeQuery) bool {
	step := int64(trq.Step)
	r, phase := value%step, int64(trq.Phase)%step
	if r < 0 {
		r += step
	}
	if phase < 0 {
		phase += step
	}
	return r == phase
}
