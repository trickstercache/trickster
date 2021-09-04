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

// Package model is a test utility package that serializes between a *DataSet
// and the  Prometheus HTTP API JSON Format
package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// WFDocument the Wire Format Document for the timeseries
type WFDocument struct {
	Status string `json:"status"`
	Data   WFData `json:"data"`
}

// WFData is the data section of the WFD
type WFData struct {
	ResultType string     `json:"resultType"`
	Results    []WFResult `json:"result"`
}

// WFResult is the Result section of the WFD
type WFResult struct {
	Metric dataset.Tags    `json:"metric"`
	Values [][]interface{} `json:"values"`
	Value  []interface{}   `json:"value"`
}

// Modeler returns a default Modeler
func Modeler() *timeseries.Modeler {
	return &timeseries.Modeler{
		WireMarshalWriter:     MarshalTimeseriesWriter,
		WireUnmarshalerReader: UnmarshalTimeseriesReader,
		WireMarshaler:         MarshalTimeseries,
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
	wfd := &WFDocument{}
	d := json.NewDecoder(reader)
	err := d.Decode(wfd)
	if err != nil {
		return nil, err
	}
	ds := &dataset.DataSet{
		Status:         wfd.Status,
		Results:        []*dataset.Result{{}},
		TimeRangeQuery: trq,
	}
	if trq != nil {
		ds.ExtentList = timeseries.ExtentList{trq.Extent}
	}
	ds.Results[0].SeriesList = make([]*dataset.Series, len(wfd.Data.Results))

	for i, pr := range wfd.Data.Results {
		sh := dataset.SeriesHeader{
			Tags: pr.Metric,
		}
		if n, ok := pr.Metric["__name__"]; ok {
			sh.Name = n
		}
		fd := timeseries.FieldDefinition{
			Name:     "value",
			DataType: timeseries.String,
		}
		sh.FieldsList = []timeseries.FieldDefinition{fd}
		sh.CalculateHash()
		var pts dataset.Points
		l := len(pr.Values)
		if wfd.Data.ResultType == "matrix" && l > 0 {
			pts = make(dataset.Points, 0, l)
			for _, v := range pr.Values {
				pts = append(pts, pointFromValues(v))
			}
		} else if wfd.Data.ResultType == "vector" && len(pr.Value) == 2 {
			pts = make(dataset.Points, 1)
			pt := pointFromValues(pr.Value)
			pts[0] = pt
			t := time.Unix(0, int64(pt.Epoch))
			ds.ExtentList = timeseries.ExtentList{timeseries.Extent{Start: t, End: t}}
		}
		s := &dataset.Series{
			Header: sh,
			Points: pts,
		}
		ds.Results[0].SeriesList[i] = s
	}
	return ds, nil
}

func pointFromValues(v []interface{}) dataset.Point {
	if len(v) != 2 {
		return dataset.Point{}
	}
	var f1 float64
	var s string
	var ok bool
	if f1, ok = v[0].(float64); !ok {
		return dataset.Point{}
	}
	if s, ok = v[1].(string); !ok {
		return dataset.Point{}
	}
	return dataset.Point{
		Epoch:  epoch.Epoch(f1) * 1000000000,
		Size:   len(s) + 16,
		Values: []interface{}{s},
	}
}

// MarshalTimeseries converts a Timeseries into a JSON blob
func MarshalTimeseries(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	err := MarshalTimeseriesWriter(ts, rlo, status, buf)
	return buf.Bytes(), err
}

// MarshalTimeseriesWriter converts a Timeseries into a JSON blob via an io.Writer
func MarshalTimeseriesWriter(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int, w io.Writer) error {

	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return timeseries.ErrUnknownFormat
	}
	// With Prometheus we presume only one Result per Dataset
	if len(ds.Results) != 1 {
		return timeseries.ErrUnknownFormat
	}

	w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[`)) // todo: always "success" ?

	seriesSep := ""
	for _, s := range ds.Results[0].SeriesList {
		if s == nil {
			continue
		}
		w.Write([]byte(seriesSep + `{"metric":{`))
		sep := ""
		for _, k := range s.Header.Tags.Keys() {
			w.Write([]byte(fmt.Sprintf(`%s"%s":"%s"`, sep, k, s.Header.Tags[k])))
			sep = ","
		}
		w.Write([]byte(`},"values":[`))
		sep = ""
		sort.Sort(s.Points)
		for _, p := range s.Points {
			w.Write([]byte(fmt.Sprintf(`%s[%s,"%s"]`,
				sep,
				strconv.FormatFloat(float64(p.Epoch)/1000000000, 'f', -1, 64),
				p.Values[0]),
			))
			sep = ","
		}
		w.Write([]byte("]}"))
		seriesSep = ","
	}
	w.Write([]byte("]}}"))
	return nil
}
