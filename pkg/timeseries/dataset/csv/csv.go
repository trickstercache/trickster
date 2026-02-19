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

package csv

import (
	"errors"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/numbers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

type (
	FieldParserFunc     func([][]string, *timeseries.TimeRangeQuery) (timeseries.SeriesFields, error)
	DataTypeParserFunc  func(string) timeseries.FieldDataType
	TimestampParserFunc func(string, timeseries.FieldDefinition) (epoch.Epoch, error)
)

// Parser represents a CSV-to-DataSet Parser
type Parser interface {
	ToDataSet([][]string, *timeseries.TimeRangeQuery) (*dataset.DataSet, error)
	ToTimeseries([][]string, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error)
}

type seriesKeyDatum struct {
	name  string
	value string
}

type seriesKeyData []seriesKeyDatum

type seriesKeyDataCacheItem struct {
	seriesKeyData seriesKeyData
	s             string
	resultID      string
}

type seriesKeyDataCacheItems []seriesKeyDataCacheItem

// delim is an internal delimiter that just needs to be something that a
// real user in all probability would not use in a result set name.
const (
	delim1 = `/°|³\`
	delim2 = `/✓¿⨉\`
)

var (
	ErrInvalidFieldParserFunc     = errors.New("invalid Field Parser Function")
	ErrInvalidDataTypeParserFunc  = errors.New("invalid Data Type Parser Function")
	ErrInvalidTimestampParserFunc = errors.New("invalid Timestamp Parser Function")
)

// NewParser returns a concurrency-safe CSV parser
func NewParser(fp FieldParserFunc, dp DataTypeParserFunc,
	tp TimestampParserFunc, firstDataRow int,
) (Parser, error) {
	if fp == nil {
		return nil, ErrInvalidFieldParserFunc
	}
	if dp == nil {
		return nil, ErrInvalidDataTypeParserFunc
	}
	if tp == nil {
		return nil, ErrInvalidTimestampParserFunc
	}
	if firstDataRow < 0 {
		firstDataRow = 0
	}
	return &parser{
		fieldParser:     fp,
		dataTypeParser:  dp,
		firstDataRow:    firstDataRow,
		timestampParser: tp,
	}, nil
}

// NewParserMust returns a concurrency-safe CSV parser or panics if the provided
// options are invalid
func NewParserMust(fp FieldParserFunc, dp DataTypeParserFunc,
	tp TimestampParserFunc, firstDataRow int,
) Parser {
	p, err := NewParser(fp, dp, tp, firstDataRow)
	if err != nil {
		panic(err)
	}
	if p == nil {
		panic("nill parser")
	}
	return p
}

type parser struct {
	fieldParser     FieldParserFunc
	dataTypeParser  DataTypeParserFunc
	timestampParser TimestampParserFunc
	firstDataRow    int
}

type csvState struct {
	ds            *dataset.DataSet
	rowSeriesKeys seriesKeyDataCacheItems
	seriesOrder   seriesKeyDataCacheItems
	seriesCounts  sets.StringCounterSet
	pointCounts   sets.StringCounterSet
	pointsUsed    sets.StringCounterSet
}

func (p *parser) ToTimeseries(matrix [][]string,
	trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	return p.ToDataSet(matrix, trq)
}

func (p *parser) ToDataSet(matrix [][]string,
	trq *timeseries.TimeRangeQuery,
) (*dataset.DataSet, error) {
	sf, err := p.fieldParser(matrix, trq)
	if err != nil {
		return nil, err
	}
	if sf.Timestamp.OutputPosition < 0 {
		return nil, timeseries.ErrInvalidBody
	}
	state, err := p.analyzeCSV(sf, matrix)
	if err != nil {
		return nil, err
	}
	state.ds.TimeRangeQuery = trq
	state.ds.ExtentList = timeseries.ExtentList{trq.Extent}
	err = p.populateFromCSV(state, matrix)
	if err != nil {
		return nil, err
	}
	return state.ds, nil
}

func (p *parser) analyzeCSV(sf timeseries.SeriesFields,
	matrix [][]string,
) (*csvState, error) {
	out := &csvState{
		seriesCounts:  sets.NewStringCounterSetCap(4),  // counts # series per-result
		pointCounts:   sets.NewStringCounterSetCap(32), // counts # points per-series
		pointsUsed:    sets.NewStringCounterSetCap(32), // holds the next point slice index per-series
		seriesOrder:   make(seriesKeyDataCacheItems, 0, 32),
		rowSeriesKeys: make(seriesKeyDataCacheItems, len(matrix)), // caches calculated series for each row
	}
	// This analyzes each row to help determine overall slice sizing
	for i, row := range matrix {
		if i < p.firstDataRow || len(row) != len(matrix[0]) { // skip header and mis-sized rows
			continue
		}
		kd := getSeriesKeyData(row, sf)
		out.rowSeriesKeys[i] = kd
		// if it's a new series, increment the result's series count
		if _, _, ok := out.pointCounts.Increment(kd.s, 1); !ok {
			out.seriesOrder = append(out.seriesOrder, kd)
			out.seriesCounts.Increment(kd.resultID, 1)
		}
	}
	out.ds = emptyDataSet(sf, out.seriesCounts, out.pointCounts, out.seriesOrder)
	return out, nil
}

// populateFromCSV populates the provided dataset with the matrix data
func (p *parser) populateFromCSV(state *csvState, matrix [][]string) error {
	type seriesLookup map[string]*dataset.Series
	lkp := make(seriesLookup)
	for _, r := range state.ds.Results {
		for _, s := range r.SeriesList {
			lkp[s.Header.Name] = s
		}
	}
	for i, row := range matrix {
		sk := state.rowSeriesKeys[i]
		s, ok := lkp[sk.s]
		if !ok {
			continue
		}
		p.addRowToSeries(state, row, s)
	}
	for _, s := range lkp {
		if i, ok := state.pointsUsed.Value(s.Header.Name); ok {
			s.Points = s.Points[:i]
		}
	}
	return nil
}

func (p *parser) addRowToSeries(state *csvState, row []string, s *dataset.Series) {
	var pt dataset.Point
	var err error
	pt.Epoch, err = p.timestampParser(row[s.Header.TimestampField.OutputPosition],
		s.Header.TimestampField)
	if err != nil {
		logger.Error("failed to parse timestamp", logging.Pairs{"error": err})
		return
	}
	pt.Values = make([]any, len(s.Header.ValueFieldsList))
	for i, fd := range s.Header.ValueFieldsList {
		if row[fd.OutputPosition] == "" {
			continue
		}
		pt.Size, _ = numbers.SafeAdd(pt.Size,
			addValue(row[fd.OutputPosition], pt.Values, i, fd.DataType))
	}
	i, _, _ := state.pointsUsed.Increment(s.Header.Name, 1)
	if ps, ok := numbers.SafeAdd64(s.PointSize, int64(pt.Size)); ok {
		s.PointSize = ps
	}
	s.Points[i] = pt
}

// addValue parses the input to a number and adds to the values slice. the
// memory size in bytes of the parsed value is returned.
func addValue(input string, vals []any, i int, t timeseries.FieldDataType) int {
	switch t {
	case timeseries.Int64:
		v, err := strconv.ParseInt(input, 10, 64)
		if err != nil {
			return 0
		}
		vals[i] = v
		return 8
	case timeseries.Float64:
		v, err := strconv.ParseFloat(input, 64)
		if err != nil {
			return 0
		}
		vals[i] = v
		return 8
	case timeseries.String, timeseries.DateTimeRFC3339, timeseries.DateTimeRFC3339Nano:
		vals[i] = input
		return len(input)
	case timeseries.Bool:
		v, err := strconv.ParseBool(input)
		if err != nil {
			return 0
		}
		vals[i] = v
		return 1
	case timeseries.Byte:
		v, err := strconv.ParseInt(input, 10, 8)
		if err != nil {
			return 0
		}
		vals[i] = v
		return 1
	case timeseries.Int16:
		v, err := strconv.ParseInt(input, 10, 16)
		if err != nil {
			return 0
		}
		vals[i] = v
		return 2
	case timeseries.Uint64:
		v, err := strconv.ParseUint(input, 10, 64)
		if err != nil {
			return 0
		}
		vals[i] = v
		return 8
	case timeseries.Unknown, timeseries.Null:
		return 0
	}
	return 0
}

func getSeriesKeyData(row []string, sf timeseries.SeriesFields) seriesKeyDataCacheItem {
	var result string
	if sf.ResultNameCol >= 0 {
		result = row[sf.ResultNameCol]
	}
	var k int
	parts := make(seriesKeyData, len(sf.Tags))
	for _, fd := range sf.Tags {
		if row[fd.OutputPosition] == "" {
			continue
		}
		parts[k] = seriesKeyDatum{name: fd.Name, value: row[fd.OutputPosition]}
		k++
	}
	return seriesKeyDataCacheItem{
		resultID:      result,
		seriesKeyData: parts[:k],
		s:             parts[:k].String(result),
	}
}

func emptyDataSet(sf timeseries.SeriesFields, sbr, pbc sets.StringCounterSet,
	so seriesKeyDataCacheItems,
) *dataset.DataSet {
	rsl := make(map[string]dataset.SeriesList, 16)
	used := sets.NewStringCounterSetCap(len(so))
	out := &dataset.DataSet{}
	ro := make(dataset.Results, 0, 4) // usually 1 item, sometimes 2, rarely 3+
	for _, kd := range so {
		pc, ok := pbc.Value(kd.s)
		if !ok || pc < 1 {
			continue
		}
		j, _ := sbr.Value(kd.resultID)
		sl, ok := rsl[kd.resultID]
		if !ok {
			sl = make(dataset.SeriesList, j)
			rsl[kd.resultID] = sl
			ro = append(ro, &dataset.Result{
				Name:       kd.resultID,
				SeriesList: sl,
			})
		}
		s := &dataset.Series{
			Header: dataset.SeriesHeader{
				Name:                kd.s,
				TimestampField:      sf.Timestamp,
				TagFieldsList:       sf.Tags,
				ValueFieldsList:     sf.Values,
				UntrackedFieldsList: sf.Untracked,
				Tags:                kd.seriesKeyData.Map(),
			},
			Points: make(dataset.Points, pc),
		}
		si, _ := used.Value(kd.resultID)
		if si < 0 {
			si = 0
		}
		used.Increment(kd.resultID, 1)
		sl[si] = s
	}
	out.Results = ro
	return out
}

func (d seriesKeyData) String(prefix string) string {
	var k int
	pairs := make([]string, len(d))
	for _, kvp := range d {
		if kvp.name == "" || kvp.value == "" {
			continue
		}
		pairs[k] = kvp.name + delim1 + kvp.value
		k++
	}
	return prefix + "." + strings.Join(pairs[:k], delim2)
}

func (d seriesKeyData) Map() map[string]string {
	out := make(map[string]string, len(d))
	for _, kd := range d {
		out[kd.name] = kd.value
	}
	return out
}
