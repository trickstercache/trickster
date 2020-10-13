/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"strings"
	"sync"
	"time"

	"github.com/tricksterproxy/trickster/pkg/timeseries"
	"github.com/tricksterproxy/trickster/pkg/timeseries/dataset"
)

// WFDocument the Wire Format Document for the timeseries
type WFDocument struct {
	Results []WFResult `json:"results"`
	Err     string     `json:"error,omitempty"`
}

// WFResult is the Result section of the WFD
type WFResult struct {
	StatementID int        `json:"statement_id"`
	SeriesList  []WFSeries `json:"series,omitempty"`
	Err         string     `json:"error,omitempty"`
}

// WFSeries is the Series section of the WFR
type WFSeries struct {
	Name    string            `json:"name,omitempty"`
	Tags    map[string]string `json:"tags,omitempty"`
	Columns []string          `json:"columns,omitempty"`
	Values  [][]interface{}   `json:"values,omitempty"`
	Partial bool              `json:"partial,omitempty"`
}

var epochMultipliers = map[byte]int64{
	1: 1,             // nanoseconds
	2: 1000,          // microseconds
	3: 1000000,       // milliseconds
	4: 1000000000,    // seconds
	5: 60000000000,   // minutes
	6: 3600000000000, // hours
}

var marshalers = map[byte]dataset.Marshaler{
	0: marshalTimeseriesJSON,
	1: marshalTimeseriesJSONPretty,
	2: marshalTimeseriesCSV,
}

// NewModeler returns a collection of modeling functions for influxdb interoperability
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

// MarshalTimeseries converts a Timeseries into a JSON blob
func MarshalTimeseries(ts timeseries.Timeseries, rlo *timeseries.RequestOptions) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	err := MarshalTimeseriesWriter(ts, rlo, buf)
	return buf.Bytes(), err
}

// MarshalTimeseriesWriter converts a Timeseries into a JSON blob via an io.Writer
func MarshalTimeseriesWriter(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, w io.Writer) error {
	if ts == nil {
		return timeseries.ErrUnknownFormat
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return timeseries.ErrUnknownFormat
	}
	var of byte
	if rlo != nil {
		of = rlo.OutputFormat
	}
	marshaler, ok := marshalers[of]
	if !ok {
		return timeseries.ErrUnknownFormat
	}
	return marshaler(ds, rlo, w)
}

func writeRFC3339Time(w io.Writer, epoch dataset.Epoch, m int64) {
	t := time.Unix(0, int64(epoch))
	w.Write([]byte(`"` + t.Format(time.RFC3339Nano) + `"`))
}

func writeEpochTime(w io.Writer, epoch dataset.Epoch, m int64) {
	w.Write([]byte(strconv.FormatInt(int64(epoch)/m, 10)))
}

func writeValue(w io.Writer, v interface{}, nilVal string) {
	if v == nil {
		w.Write([]byte(nilVal))
	}
	switch t := v.(type) {
	case string:
		w.Write([]byte(`"` + t + `"`))
	case bool:
		w.Write([]byte(strconv.FormatBool(t)))
	case int64:
		w.Write([]byte(strconv.FormatInt(t, 10)))
	case int:
		w.Write([]byte(strconv.Itoa(t)))
	case float64:
		w.Write([]byte(strconv.FormatFloat(t, 'f', -1, 64)))
	case float32:
		w.Write([]byte(strconv.FormatFloat(float64(t), 'f', -1, 64)))
	}
}

func writeCSVValue(w io.Writer, v interface{}, nilVal string) {
	if v == nil {
		w.Write([]byte(nilVal))
	}
	switch t := v.(type) {
	case string:
		if strings.Contains(t, `"`) {
			t = `"` + strings.Replace(t, `"`, `\"`, -1) + `"`
		} else if strings.Contains(t, " ") || strings.Contains(t, ",") {
			t = `"` + t + `"`
		}
		w.Write([]byte(t))
	case bool:
		w.Write([]byte(strconv.FormatBool(t)))
	case int64:
		w.Write([]byte(strconv.FormatInt(t, 10)))
	case int:
		w.Write([]byte(strconv.Itoa(t)))
	case float64:
		w.Write([]byte(strconv.FormatFloat(t, 'f', -1, 64)))
	case float32:
		w.Write([]byte(strconv.FormatFloat(float64(t), 'f', -1, 64)))
	}
}

func marshalTimeseriesJSON(ds *dataset.DataSet, rlo *timeseries.RequestOptions, w io.Writer) error {
	if ds == nil {
		return nil
	}

	dw, multiplier := getDateWriter(rlo)

	w.Write([]byte(`{"results":[`))
	lr := len(ds.Results)
	for i := range ds.Results {
		w.Write([]byte(
			fmt.Sprintf(`{"statement_id":%d,"series":[`,
				ds.Results[i].StatementID)))
		ls := len(ds.Results[i].SeriesList)
		for si, s := range ds.Results[i].SeriesList {
			if s == nil {
				continue
			}
			if s.Header.Tags == nil {
				s.Header.Tags = make(dataset.Tags)
			}
			w.Write([]byte(
				fmt.Sprintf(`{"name":"%s","tags":%s,`, s.Header.Name,
					s.Header.Tags.JSON())))
			fl := len(s.Header.FieldsList)
			l := fl + 1
			cols := make([]string, l)
			var j int
			for _, f := range s.Header.FieldsList {
				if j == s.Header.TimestampIndex {
					cols[j] = "time"
					j++
				}
				cols[j] = f.Name
				j++
			}
			w.Write([]byte(
				`"columns":["` + strings.Join(cols, `","`) + `"],"values":[`,
			))
			lp := len(s.Points) - 1
			for j := range s.Points {
				w.Write([]byte("["))
				lv := len(s.Points[j].Values)
				for n, v := range s.Points[j].Values {
					if n == s.Header.TimestampIndex {
						dw(w, s.Points[j].Epoch, multiplier)
						if n < lv {
							w.Write([]byte(","))
						}
						n++
					}
					writeValue(w, v, "null")
					if n < lv {
						w.Write([]byte(","))
					}
				}
				w.Write([]byte("]"))
				if j < lp {
					w.Write([]byte(","))
				}
			}
			w.Write([]byte("]}"))
			if si < ls-1 {
				w.Write([]byte(","))
			}
		}
		w.Write([]byte("]}"))
		if i < lr-1 {
			w.Write([]byte(","))
		}
	}
	w.Write([]byte("]}"))
	return nil
}

func marshalTimeseriesJSONPretty(ds *dataset.DataSet, rlo *timeseries.RequestOptions, w io.Writer) error {

	if ds == nil {
		return nil
	}

	dw, multiplier := getDateWriter(rlo)

	w.Write([]byte("{\n    \"results\": [\n"))
	lr := len(ds.Results)
	for i := range ds.Results {
		w.Write([]byte(
			fmt.Sprintf("        {\n            \"statement_id\": %d,\n            \"series\": [\n",
				ds.Results[i].StatementID)))
		ls := len(ds.Results[i].SeriesList)
		for si, s := range ds.Results[i].SeriesList {
			if s == nil {
				continue
			}
			if s.Header.Tags == nil {
				s.Header.Tags = make(dataset.Tags)
			}
			w.Write([]byte(
				"                {\n" +
					fmt.Sprintf("                    \"name\": \"%s\",\n", s.Header.Name) +
					"                    \"tags\": {\n"))
			for j, k := range s.Header.Tags.Keys() {
				w.Write([]byte(fmt.Sprintf("                        \"%s\": \"%s\"", k, s.Header.Tags[k])))
				if j < len(s.Header.Tags)-1 {
					w.Write([]byte(","))
				}
				w.Write([]byte("\n"))
			}
			w.Write([]byte("                    },\n                    \"columns\": [\n"))
			for j, v := range s.Header.FieldsList {
				if j == s.Header.TimestampIndex {
					w.Write([]byte("                        \"time\""))
					if j < len(s.Header.FieldsList)-1 {
						w.Write([]byte(","))
					}
					w.Write([]byte("\n"))
					j++
				}
				w.Write([]byte("                        \"" + v.Name + "\""))
				if j < len(s.Header.FieldsList)-1 {
					w.Write([]byte(","))
				}
				w.Write([]byte("\n"))
				j++
			}

			fl := len(s.Header.FieldsList)
			l := fl + 1
			cols := make([]string, l)
			var j int
			for _, f := range s.Header.FieldsList {
				if j == s.Header.TimestampIndex {
					cols[j] = "time"
					j++
				}
				cols[j] = f.Name
				j++
			}
			w.Write([]byte("                    ],\n                    \"values\": [\n"))
			lp := len(s.Points) - 1
			for j := range s.Points {
				w.Write([]byte("                        [\n"))
				lv := len(s.Points[j].Values) - 1
				for n, v := range s.Points[j].Values {
					if n == s.Header.TimestampIndex {
						w.Write([]byte("                            "))
						dw(w, s.Points[j].Epoch, multiplier)
						if n < lv {
							w.Write([]byte(","))
						}
						w.Write([]byte("\n"))
						n++
					}
					w.Write([]byte("                            "))
					writeValue(w, v, "null")
					if n < lv {
						w.Write([]byte(","))
					}
					w.Write([]byte("\n"))
				}
				w.Write([]byte("                        ]"))
				if j < lp {
					w.Write([]byte(","))
				}
				w.Write([]byte("\n"))
			}
			w.Write([]byte("                    ]\n                }"))
			if si < ls-1 {
				w.Write([]byte(","))
			}
			w.Write([]byte("\n"))
		}
		w.Write([]byte("            ]"))
		if i < lr-1 {
			w.Write([]byte(","))
		}
		w.Write([]byte("\n"))
	}
	w.Write([]byte("        }\n    ]\n}\n"))
	return nil
}

func marshalTimeseriesCSV(ds *dataset.DataSet, rlo *timeseries.RequestOptions, w io.Writer) error {
	var headerWritten bool
	dw, multiplier := getDateWriter(rlo)
	if ds == nil {
		return nil
	}
	for _, s := range ds.Results[0].SeriesList {
		if s == nil {
			continue
		}
		if !headerWritten {
			l := len(s.Header.FieldsList) + 1
			cols := make([]string, l)
			var j int
			for _, f := range s.Header.FieldsList {
				if j == s.Header.TimestampIndex {
					cols[j] = "time"
					j++
				}
				cols[j] = f.Name
				j++
			}
			w.Write([]byte("name,tags," + strings.Join(cols, ",") + "\n"))
			headerWritten = true
		}
		for _, p := range s.Points {
			w.Write([]byte(s.Header.Name + ",\"" + s.Header.Tags.StringsWithSep("=", ",") + "\","))
			lv := len(p.Values)
			for n, v := range p.Values {
				if n == s.Header.TimestampIndex {
					dw(w, p.Epoch, multiplier)
					if n < lv-1 {
						w.Write([]byte(","))
					}
					n++
				}
				writeCSVValue(w, v, "")
				if n < lv-1 {
					w.Write([]byte{','})
				}
			}
			w.Write([]byte("\n"))
		}
	}
	return nil
}

type dateWriter func(io.Writer, dataset.Epoch, int64)

func getDateWriter(rlo *timeseries.RequestOptions) (dateWriter, int64) {
	var dw dateWriter
	var tf byte
	var multiplier int64
	if rlo != nil {
		tf = rlo.TimeFormat
	}
	switch tf {
	case 0:
		dw = writeRFC3339Time
	default:
		if m, ok := epochMultipliers[tf]; ok {
			multiplier = m
		} else {
			multiplier = 1
		}
		dw = writeEpochTime
	}
	return dw, multiplier
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
	wfd := &WFDocument{}
	d := json.NewDecoder(reader)
	err := d.Decode(wfd)
	if err != nil {
		return nil, err
	}
	ds := &dataset.DataSet{
		Error:          wfd.Err,
		TimeRangeQuery: trq,
		ExtentList:     timeseries.ExtentList{trq.Extent},
	}
	if wfd.Results == nil {
		return nil, timeseries.ErrInvalidBody
	}
	ds.Results = make([]*dataset.Result, len(wfd.Results))
	for i := range wfd.Results {
		ds.Results[i] = &dataset.Result{
			StatementID: wfd.Results[i].StatementID,
			Error:       wfd.Results[i].Err,
		}
		if wfd.Results[i].SeriesList == nil {
			continue
		}
		ds.Results[i].SeriesList = make([]*dataset.Series, len(wfd.Results[i].SeriesList))
		for j := range wfd.Results[i].SeriesList {
			sh := dataset.SeriesHeader{
				Name:           wfd.Results[i].SeriesList[j].Name,
				Tags:           dataset.Tags(wfd.Results[i].SeriesList[j].Tags),
				QueryStatement: trq.Statement,
			}
			if wfd.Results[i].SeriesList[j].Columns == nil ||
				len(wfd.Results[i].SeriesList[j].Columns) < 2 {
				return nil, timeseries.ErrInvalidBody
			}
			var timeFound bool
			cl := len(wfd.Results[i].SeriesList[j].Columns)
			fdl := cl - 1 // -1 excludes time column from list for DataSet format
			sh.FieldsList = make([]timeseries.FieldDefinition, fdl)
			var fdi int
			for ci, cn := range wfd.Results[i].SeriesList[j].Columns {
				if cn == "time" {
					timeFound = true
					sh.TimestampIndex = ci
					continue
				}
				sh.FieldsList[fdi] = timeseries.FieldDefinition{Name: cn}
				fdi++
			}
			if !timeFound || wfd.Results[i].SeriesList[j].Values == nil {
				return nil, timeseries.ErrInvalidBody
			}
			sh.CalculateSize()
			pts := make(dataset.Points, 0, len(wfd.Results[i].SeriesList[j].Values))
			var sz int64
			var mtx sync.Mutex
			var wg sync.WaitGroup
			var ume error
			for vi, v := range wfd.Results[i].SeriesList[j].Values {
				wg.Add(1)
				go func(vals []interface{}, idx int) {
					pt, cols, err := pointFromValues(vals, sh.TimestampIndex)
					if err != nil {
						ume = err
						return
					}
					if pt.Epoch == 0 {
						return
					}
					if idx == 0 {
						for x := range cols {
							sh.FieldsList[x].DataType = cols[x]
						}
					}
					mtx.Lock()
					pts = append(pts, pt)
					sz += int64(pt.Size)
					wfd.Results[i].SeriesList[j].Values[idx] = nil
					mtx.Unlock()
					wg.Done()
				}(v, vi)
				if ume != nil {
					break
				}
			}
			wg.Wait()
			sort.Sort(pts)
			if ume != nil {
				return nil, err
			}
			s := &dataset.Series{
				Header:    sh,
				Points:    pts,
				PointSize: sz,
			}
			ds.Results[i].SeriesList[j] = s
			wfd.Results[i].SeriesList[j].Values = nil
		}
	}
	return ds, nil
}

func pointFromValues(v []interface{}, tsIndex int) (dataset.Point,
	[]timeseries.FieldDataType, error) {
	p := dataset.Point{}
	ns, ok := v[tsIndex].(int64)
	if !ok {
		fns, ok := v[tsIndex].(float64)
		if !ok {
			return p, nil, timeseries.ErrInvalidTimeFormat
		}
		ns = int64(fns)
	}
	p.Values = append(v[:tsIndex], v[tsIndex+1:]...)
	p.Epoch = dataset.Epoch(ns)
	p.Size = 12
	fdts := make([]timeseries.FieldDataType, len(p.Values))
	for x := range p.Values {
		if p.Values[x] == nil {
			continue
		}
		switch t := p.Values[x].(type) {
		case string:
			fdts[x] = timeseries.String
			p.Size += len(t)
		case bool:
			fdts[x] = timeseries.Bool
			p.Size++
		case int64, int:
			fdts[x] = timeseries.Int64
			p.Size += 8
		case float64, float32:
			fdts[x] = timeseries.Float64
			p.Size += 8
		default:
			return p, nil, timeseries.ErrInvalidTimeFormat
		}
	}
	return p, fdts, nil
}
