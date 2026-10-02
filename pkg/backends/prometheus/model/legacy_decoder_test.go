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

// This file keeps the Prometheus decoder from before the stream decoder, as the oracle the stream
// decoder's conformance tests compare it with.

import (
	"encoding/json"
	"io"
	"runtime"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"golang.org/x/sync/errgroup"
)

// WFMatrixDocument is the Wire Format Document for prometheus range / timeseries
type WFMatrixDocument struct {
	*Envelope
	Data WFMatrixData `json:"data"`
}

// WFMatrixData is the data section of the WFD for timeseries responses
type WFMatrixData struct {
	ResultType    ResultType     `json:"resultType"`
	MatrixResults []*WFResult    `json:"-"`
	ScalarResult  WFResultScalar `json:"-"`
}

func (d *WFMatrixData) UnmarshalJSON(data []byte) error {
	// First pass: decode just the resultType using a lightweight struct
	var envelope struct {
		ResultType ResultType      `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	d.ResultType = envelope.ResultType
	if len(envelope.Result) == 0 {
		return nil
	}
	switch d.ResultType {
	case Matrix, Vector:
		return json.Unmarshal(envelope.Result, &d.MatrixResults)
	case Scalar:
		return json.Unmarshal(envelope.Result, &d.ScalarResult)
	}
	return nil
}

// WFResult is the Result section of the WFD (matrix and vector only)
type WFResult struct {
	Metric     dataset.Tags `json:"metric"`
	Values     [][]any      `json:"values,omitempty"`
	Value      []any        `json:"value,omitempty"`
	Histograms [][]any      `json:"histograms,omitempty"`
	Histogram  []any        `json:"histogram,omitempty"`
}

// WFResultScalar is the Result section of the WFD (scalar only)
type WFResultScalar []any

// legacyUnmarshalTimeseriesReader is the decoder the stream decoder replaced, kept as its oracle
func legacyUnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	if reader == nil {
		return nil, io.ErrUnexpectedEOF
	}
	wfd := &WFMatrixDocument{}
	d := json.NewDecoder(reader)
	err := d.Decode(wfd)
	if err != nil {
		return nil, err
	}
	if wfd.Envelope == nil {
		wfd.Envelope = &Envelope{}
	}
	ds := &dataset.DataSet{
		SourceResultType: string(wfd.Data.ResultType),
		Status:           wfd.Status,
		Error:            wfd.Error,
		ErrorType:        wfd.ErrorType,
		Warnings:         wfd.Warnings,
		TimeRangeQuery:   trq,
		ExtentList:       timeseries.ExtentList{trq.Extent},
		ValueOperations:  prometheusValueOperations,
	}

	switch wfd.Data.ResultType {
	case Matrix, Vector:
		legacyPopulateSeries(ds, wfd.Data.MatrixResults, trq, wfd.Data.ResultType == Vector)
	case Scalar:
		wfr := &WFResult{Value: wfd.Data.ScalarResult}
		legacyPopulateSeries(ds, []*WFResult{wfr}, trq, true)
	default:
		return ds, nil
	}
	return ds, nil
}

func legacyPointFromValues(v []any) (dataset.Point, error) {
	if len(v) != 2 {
		return dataset.Point{}, timeseries.ErrInvalidBody
	}
	var f1 float64
	var s string
	var ok bool
	if f1, ok = v[0].(float64); !ok {
		return dataset.Point{}, timeseries.ErrInvalidBody
	}
	if s, ok = v[1].(string); !ok {
		return dataset.Point{}, timeseries.ErrInvalidBody
	}
	return dataset.Point{
		Epoch:  epoch.Epoch(f1 * 1e9),
		Values: []any{s},
	}, nil
}

func legacyPointFromHistogram(v []any) (dataset.Point, error) {
	if len(v) != 2 {
		return dataset.Point{}, timeseries.ErrInvalidBody
	}
	f1, ok := v[0].(float64)
	if !ok {
		return dataset.Point{}, timeseries.ErrInvalidBody
	}
	hb, err := json.Marshal(v[1])
	if err != nil {
		return dataset.Point{}, err
	}
	return dataset.Point{
		Epoch:  epoch.Epoch(f1 * 1e9),
		Values: []any{string(hb)},
	}, nil
}

func legacyPopulateSeries(ds *dataset.DataSet, result []*WFResult,
	trq *timeseries.TimeRangeQuery, isVector bool,
) {
	ds.Results = []*dataset.Result{{}}
	ds.Results[0].SeriesList = make([]*dataset.Series, 0, len(result))

	fdValue := timeseries.FieldDefinition{
		Name:     "value",
		DataType: timeseries.String,
	}
	fdHist := timeseries.FieldDefinition{
		Name:     fieldNameHistogram,
		DataType: timeseries.String,
	}

	for _, pr := range result {
		baseName := ""
		if n, ok := pr.Metric["__name__"]; ok {
			baseName = n
		}

		hasValues := (!isVector && len(pr.Values) > 0) || (isVector && len(pr.Value) >= 1)
		hasHistograms := (!isVector && len(pr.Histograms) > 0) || (isVector && len(pr.Histogram) == 2)

		// Always emit at least one series per result to preserve SeriesList
		// length for callers that expect it (e.g. scalar results with no data).
		if hasValues || !hasHistograms {
			sh := dataset.SeriesHeader{
				Tags:            pr.Metric,
				QueryStatement:  trq.Statement,
				Name:            baseName,
				ValueFieldsList: []timeseries.FieldDefinition{fdValue},
			}
			var pts dataset.Points
			if !isVector && len(pr.Values) > 0 {
				l := len(pr.Values)
				pts = make(dataset.Points, l)
				var eg errgroup.Group
				eg.SetLimit(runtime.GOMAXPROCS(0))
				for i, v := range pr.Values {
					eg.Go(func() error {
						pt, _ := legacyPointFromValues(v)
						if pt.Epoch > 0 {
							pts[i] = pt
						}
						return nil
					})
				}
				eg.Wait()
				j := 0
				for _, p := range pts {
					if p.Epoch > 0 {
						pts[j] = p
						j++
					}
				}
				pts = pts[:j]
			} else if isVector && len(pr.Value) == 2 {
				pts = make(dataset.Points, 1)
				pt, _ := legacyPointFromValues(pr.Value)
				pts[0] = pt
				t := time.Unix(0, int64(pt.Epoch))
				ds.ExtentList = timeseries.ExtentList{timeseries.Extent{Start: t, End: t}}
			}
			sh.CalculateSize()
			ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, dataset.NewSeries(sh, pts))
		}

		// Histogram series
		if hasHistograms {
			sh := dataset.SeriesHeader{
				Tags:            pr.Metric,
				QueryStatement:  trq.Statement,
				Name:            baseName,
				ValueFieldsList: []timeseries.FieldDefinition{fdHist},
			}
			var pts dataset.Points
			if !isVector {
				l := len(pr.Histograms)
				pts = make(dataset.Points, l)
				var eg errgroup.Group
				eg.SetLimit(runtime.GOMAXPROCS(0))
				for i, v := range pr.Histograms {
					eg.Go(func() error {
						pt, _ := legacyPointFromHistogram(v)
						if pt.Epoch > 0 {
							pts[i] = pt
						}
						return nil
					})
				}
				eg.Wait()
				j := 0
				for _, p := range pts {
					if p.Epoch > 0 {
						pts[j] = p
						j++
					}
				}
				pts = pts[:j]
			} else {
				pts = make(dataset.Points, 1)
				pt, _ := legacyPointFromHistogram(pr.Histogram)
				pts[0] = pt
				t := time.Unix(0, int64(pt.Epoch))
				ds.ExtentList = timeseries.ExtentList{timeseries.Extent{Start: t, End: t}}
			}
			sh.CalculateSize()
			ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, dataset.NewSeries(sh, pts))
		}
	}
}
