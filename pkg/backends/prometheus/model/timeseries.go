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
	"io"
	"runtime"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/errors"
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
	if wfd.Envelope == nil {
		wfd.Envelope = &Envelope{}
	}
	ds := &dataset.DataSet{
		Status:         wfd.Status,
		Error:          wfd.Error,
		ErrorType:      wfd.ErrorType,
		Warnings:       wfd.Warnings,
		TimeRangeQuery: trq,
		ExtentList:     timeseries.ExtentList{trq.Extent},
	}

	switch wfd.Data.ResultType {
	case Matrix, Vector:
		populateSeries(ds, wfd.Data.MatrixResults, trq, wfd.Data.ResultType == Vector)
	case Scalar:
		wfr := &WFResult{Value: wfd.Data.ScalarResult}
		populateSeries(ds, []*WFResult{wfr}, trq, true)
	default:
		return ds, nil
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
		Epoch:  epoch.Epoch(f1 * 1e9),
		Size:   len(s) + 32, // 8 bytes for epoch, 8 bytes for size, 16 bytes for s stringHeader
		Values: []any{s},
	}, nil
}

const fieldNameHistogram = "histogram"

func pointFromHistogram(v []any) (dataset.Point, error) {
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
	s := string(hb)
	return dataset.Point{
		Epoch:  epoch.Epoch(f1 * 1e9),
		Size:   len(s) + 32,
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

// seriesGroup collects value and histogram series that share the same metric tags.
type seriesGroup struct {
	tagsJSON  string
	valueSer  *dataset.Series
	histSer   *dataset.Series
}

// MarshalTSOrVectorWriter writes matrix and vector outputs to the provided io.Writer
func MarshalTSOrVectorWriter(ts timeseries.Timeseries, _ *timeseries.RequestOptions,
	status int, w io.Writer, isVector bool,
) error {
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
		ds.Status = statusSuccess
	}

	(&Envelope{ds.Status, ds.Error, ds.ErrorType, ds.Warnings}).StartMarshal(w, status)

	resultType := Matrix
	if isVector {
		resultType = Vector
	}

	w.Write([]byte(`,"data":{"resultType":"`))
	w.Write([]byte(resultType))
	w.Write([]byte(`","result":[`))

	// Group series by metric tags so a single Prometheus result object can
	// contain both "values" and "histograms" arrays when a metric has mixed types.
	groups := groupSeriesByTags(ds.Results[0].SeriesList)

	var buf [64]byte
	var seriesSep bool
	for _, g := range groups {
		if (g.valueSer == nil || len(g.valueSer.Points) == 0) &&
			(g.histSer == nil || len(g.histSer.Points) == 0) {
			continue
		}
		if seriesSep {
			w.Write([]byte(`,{"metric":`))
		} else {
			w.Write([]byte(`{"metric":`))
			seriesSep = true
		}
		w.Write([]byte(g.tagsJSON))

		if isVector {
			marshalVectorGroup(w, g, buf)
		} else {
			marshalMatrixGroup(w, g, buf)
		}
		w.Write([]byte("}"))
	}
	w.Write([]byte("]}}"))
	return nil
}

func groupSeriesByTags(seriesList []*dataset.Series) []seriesGroup {
	order := make([]string, 0, len(seriesList))
	idx := make(map[string]int, len(seriesList))
	var groups []seriesGroup
	for _, s := range seriesList {
		if s == nil || len(s.Points) == 0 {
			continue
		}
		tj := s.Header.Tags.JSON()
		gi, exists := idx[tj]
		if !exists {
			gi = len(groups)
			idx[tj] = gi
			groups = append(groups, seriesGroup{tagsJSON: tj})
			order = append(order, tj)
		}
		isHist := len(s.Header.ValueFieldsList) > 0 &&
			s.Header.ValueFieldsList[0].Name == fieldNameHistogram
		if isHist {
			groups[gi].histSer = s
		} else {
			groups[gi].valueSer = s
		}
	}
	return groups
}

func marshalVectorGroup(w io.Writer, g seriesGroup, buf [64]byte) {
	if g.histSer != nil && len(g.histSer.Points) > 0 {
		w.Write([]byte(`,"histogram":[`))
		b := strconv.AppendFloat(buf[:0], float64(g.histSer.Points[0].Epoch)/1e9, 'f', -1, 64)
		w.Write(b)
		w.Write([]byte(`,`))
		w.Write([]byte(g.histSer.Points[0].Values[0].(string)))
		w.Write([]byte(`]`))
	} else if g.valueSer != nil && len(g.valueSer.Points) > 0 {
		w.Write([]byte(`,"value":[`))
		b := strconv.AppendFloat(buf[:0], float64(g.valueSer.Points[0].Epoch)/1e9, 'f', -1, 64)
		w.Write(b)
		w.Write([]byte(`,"`))
		w.Write([]byte(g.valueSer.Points[0].Values[0].(string)))
		w.Write([]byte(`"]`))
	}
}

func marshalMatrixGroup(w io.Writer, g seriesGroup, buf [64]byte) {
	if g.valueSer != nil && len(g.valueSer.Points) > 0 {
		if !sort.IsSorted(g.valueSer.Points) {
			sort.Sort(g.valueSer.Points)
		}
		w.Write([]byte(`,"values":[`))
		for i, p := range g.valueSer.Points {
			if i > 0 {
				w.Write([]byte{','})
			}
			w.Write([]byte{'['})
			b := strconv.AppendFloat(buf[:0], float64(p.Epoch)/1e9, 'f', -1, 64)
			w.Write(b)
			w.Write([]byte(`,"`))
			w.Write([]byte(p.Values[0].(string)))
			w.Write([]byte(`"]`))
		}
		w.Write([]byte(`]`))
	}
	if g.histSer != nil && len(g.histSer.Points) > 0 {
		if !sort.IsSorted(g.histSer.Points) {
			sort.Sort(g.histSer.Points)
		}
		w.Write([]byte(`,"histograms":[`))
		for i, p := range g.histSer.Points {
			if i > 0 {
				w.Write([]byte{','})
			}
			w.Write([]byte{'['})
			b := strconv.AppendFloat(buf[:0], float64(p.Epoch)/1e9, 'f', -1, 64)
			w.Write(b)
			w.Write([]byte(`,`))
			w.Write([]byte(p.Values[0].(string)))
			w.Write([]byte(`]`))
		}
		w.Write([]byte(`]`))
	}
}

func populateSeries(ds *dataset.DataSet, result []*WFResult,
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
			var ps int64 = 16
			if !isVector && len(pr.Values) > 0 {
				l := len(pr.Values)
				pts = make(dataset.Points, l)
				var eg errgroup.Group
				eg.SetLimit(runtime.GOMAXPROCS(0))
				for i, v := range pr.Values {
					eg.Go(func() error {
						pt, _ := pointFromValues(v)
						if pt.Epoch > 0 {
							atomic.AddInt64(&ps, int64(pt.Size))
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
				pt, _ := pointFromValues(pr.Value)
				ps = int64(pt.Size)
				pts[0] = pt
				t := time.Unix(0, int64(pt.Epoch))
				ds.ExtentList = timeseries.ExtentList{timeseries.Extent{Start: t, End: t}}
			}
			sh.CalculateSize()
			ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, &dataset.Series{
				Header:    sh,
				Points:    pts,
				PointSize: ps,
			})
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
			var ps int64 = 16
			if !isVector {
				l := len(pr.Histograms)
				pts = make(dataset.Points, l)
				var eg errgroup.Group
				eg.SetLimit(runtime.GOMAXPROCS(0))
				for i, v := range pr.Histograms {
					eg.Go(func() error {
						pt, _ := pointFromHistogram(v)
						if pt.Epoch > 0 {
							atomic.AddInt64(&ps, int64(pt.Size))
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
				pt, _ := pointFromHistogram(pr.Histogram)
				ps = int64(pt.Size)
				pts[0] = pt
				t := time.Unix(0, int64(pt.Epoch))
				ds.ExtentList = timeseries.ExtentList{timeseries.Extent{Start: t, End: t}}
			}
			sh.CalculateSize()
			ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, &dataset.Series{
				Header:    sh,
				Points:    pts,
				PointSize: ps,
			})
		}
	}
}
