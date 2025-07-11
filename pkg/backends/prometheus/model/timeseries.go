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
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// WFMatrixDocument is the Wire Format Document for prometheus range / timeseries
type WFMatrixDocument struct {
	*Envelope
	Data WFMatrixData `json:"data"`
}

// WFMatrixData is the data section of the WFD for timeseries responses
type WFMatrixData struct {
	ResultType ResultType      `json:"resultType"`
	Results    json.RawMessage `json:"result"`
}

// WFResult is the Result section of the WFD (matrix and vector only)
type WFResult struct {
	Metric dataset.Tags `json:"metric"`
	Values [][]any      `json:"values,omitempty"`
	Value  []any        `json:"value,omitempty"`
}

// WFResultScalar is the Result section of the WFD (scalar only)
type WFResultScalar []any

// NewModeler returns a collection of modeling functions for prometheus interoperability
func NewModeler() *timeseries.Modeler {
	return &timeseries.Modeler{
		WireUnmarshalerReader: UnmarshalTimeseriesReader,
		WireMarshaler:         MarshalTimeseries,
		WireMarshalWriter:     MarshalTimeseriesWriter,
		WireUnmarshaler:       UnmarshalTimeseries,
		CacheMarshaler:        dataset.MarshalDataSet,
		CacheUnmarshaler:      dataset.UnmarshalDataSet,
	}
}

// UnmarshalTimeseries converts a JSON blob into a Timeseries
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	buf := bytes.NewReader(data)
	return UnmarshalTimeseriesReader(buf, trq)
}

// UnmarshalTimeseriesReader converts a JSON blob into a Timeseries via io.Reader
func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	wfd := &WFMatrixDocument{}
	d := json.NewDecoder(reader)
	err := d.Decode(wfd)
	if err != nil {
		return nil, err
	}
	ds := &dataset.DataSet{
		Status:         wfd.Status,
		Error:          wfd.Error,
		ErrorType:      wfd.ErrorType,
		Warnings:       wfd.Warnings,
		TimeRangeQuery: trq,
		ExtentList:     timeseries.ExtentList{trq.Extent},
	}

	if len(wfd.Data.Results) == 0 {
		return ds, nil
	}
	switch wfd.Data.ResultType {
	case Matrix, Vector:
		var wfr []*WFResult
		if err := json.Unmarshal(wfd.Data.Results, &wfr); err != nil {
			return nil, err
		}
		populateSeries(ds, wfr, trq, wfd.Data.ResultType == Vector)
	case Scalar:
		var wfrs WFResultScalar
		if err := json.Unmarshal(wfd.Data.Results, &wfrs); err != nil {
			return nil, err
		}
		wfr := &WFResult{Value: wfrs}
		populateSeries(ds, []*WFResult{wfr}, trq, true)
	}
	return ds, nil
}

func pointFromValues(v []any) (dataset.Point, error) {
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
		Epoch:  epoch.Epoch(f1) * 1000000000,
		Size:   len(s) + 32, // 8 bytes for epoch, 8 bytes for size, 16 bytes for s stringHeader
		Values: []any{s},
	}, nil
}

// MarshalTimeseries converts a Timeseries into a JSON blob
func MarshalTimeseries(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	err := MarshalTimeseriesWriter(ts, rlo, status, buf)
	return buf.Bytes(), err
}

// MarshalTimeseriesWriter converts a Timeseries into a JSON blob via an io.Writer
func MarshalTimeseriesWriter(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int, w io.Writer) error {
	return MarshalTSOrVectorWriter(ts, rlo, status, w, false)
}

// marshalTSOrVectorWriter writes matrix and vector outputs to the provided io.Writer
func MarshalTSOrVectorWriter(ts timeseries.Timeseries, _ *timeseries.RequestOptions,
	status int, w io.Writer, isVector bool) error {

	if w == nil {
		return errors.ErrNilWriter
	}

	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		return timeseries.ErrUnknownFormat
	}
	// With Prometheus we presume only one Result per DataSet
	if len(ds.Results) != 1 {
		return timeseries.ErrUnknownFormat
	}

	if ds.Status == "" {
		ds.Status = "success"
	}

	(&Envelope{ds.Status, ds.Error, ds.ErrorType, ds.Warnings}).StartMarshal(w, status)

	resultType := Matrix
	if isVector {
		resultType = Vector
	}

	fmt.Fprintf(w, `,"data":{"resultType":"%s","result":[`, resultType)

	seriesSep := ""
	for _, s := range ds.Results[0].SeriesList {
		if s == nil || len(s.Points) == 0 {
			continue
		}
		w.Write([]byte(seriesSep + `{"metric":{`))
		sep := ""
		for _, k := range s.Header.Tags.Keys() {
			fmt.Fprintf(w, `%s"%s":"%s"`, sep, k, s.Header.Tags[k])
			sep = ","
		}
		if isVector {
			w.Write([]byte(`},"value":[`))
			if len(s.Points) > 0 {
				fmt.Fprintf(w, `%s,"%s"`,
					strconv.FormatFloat(float64(s.Points[0].Epoch)/1000000000, 'f', -1, 64),
					s.Points[0].Values[0],
				)
			}
			w.Write([]byte("]}"))
		} else {
			w.Write([]byte(`},"values":[`))
			sep = ""
			sort.Sort(s.Points)
			for _, p := range s.Points {
				fmt.Fprintf(w, `%s[%s,"%s"]`,
					sep,
					strconv.FormatFloat(float64(p.Epoch)/1000000000, 'f', -1, 64),
					p.Values[0],
				)
				sep = ","
			}
			w.Write([]byte("]}"))
		}
		seriesSep = ","
	}
	w.Write([]byte("]}}"))
	return nil
}

func populateSeries(ds *dataset.DataSet, result []*WFResult,
	trq *timeseries.TimeRangeQuery, isVector bool) {
	ds.Results = []*dataset.Result{{}}
	ds.Results[0].SeriesList = make([]*dataset.Series, len(result))
	for i, pr := range result {
		sh := dataset.SeriesHeader{
			Tags:           pr.Metric,
			QueryStatement: trq.Statement,
		}
		if n, ok := pr.Metric["__name__"]; ok {
			sh.Name = n
		}
		fd := timeseries.FieldDefinition{
			Name:     "value",
			DataType: timeseries.String,
		}
		sh.ValueFieldsList = []timeseries.FieldDefinition{fd}
		var pts dataset.Points
		l := len(pr.Values)
		var ps int64 = 16
		if !isVector && l > 0 {
			pts = make(dataset.Points, l)
			var wg sync.WaitGroup
			wg.Add(len(pr.Values))
			for i, v := range pr.Values {
				go func(index int, vals []any) {
					pt, _ := pointFromValues(vals)
					if pt.Epoch > 0 {
						atomic.AddInt64(&ps, int64(pt.Size))
						pts[index] = pt
					}
					wg.Done()
				}(i, v)
			}
			wg.Wait()
		} else if isVector && len(pr.Value) == 2 {
			pts = make(dataset.Points, 1)
			pt, _ := pointFromValues(pr.Value)
			ps = int64(pt.Size)
			pts[0] = pt
			t := time.Unix(0, int64(pt.Epoch))
			ds.ExtentList = timeseries.ExtentList{timeseries.Extent{Start: t, End: t}}
		}
		sh.CalculateSize()
		s := &dataset.Series{
			Header:    sh,
			Points:    pts,
			PointSize: ps,
		}
		ds.Results[0].SeriesList[i] = s
	}
}
