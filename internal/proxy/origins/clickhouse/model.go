/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package clickhouse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/pkg/sort/times"
)

const (
	millisPerSecond     = int64(time.Second / time.Millisecond)
	nanosPerMillisecond = int64(time.Millisecond / time.Nanosecond)
)

func msToTime(ms string) (time.Time, error) {
	msInt, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(msInt/millisPerSecond,
		(msInt%millisPerSecond)*nanosPerMillisecond), nil
}

// FieldDefinition ...
type FieldDefinition struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// ResponseValue ...
type ResponseValue map[string]interface{}

// DataSet ...
type DataSet struct {
	Metric map[string]interface{}
	Points []Point
}

// Points ...
type Points []Point

// Point ...
type Point struct {
	Timestamp time.Time
	Value     float64
}

// Response is the JSON responose document structure for ClickHouse query results
type Response struct {
	Meta         []FieldDefinition     `json:"meta"`
	RawData      []ResponseValue       `json:"data"`
	Rows         int                   `json:"rows"`
	Order        []string              `json:"-"`
	StepDuration time.Duration         `json:"step,omitempty"`
	ExtentList   timeseries.ExtentList `json:"extents,omitempty"`
}

// ResultsEnvelope is the ClickHouse document structure optimized for time series manipulation
type ResultsEnvelope struct {
	Meta         []FieldDefinition            `json:"meta"`
	Data         map[string]*DataSet          `json:"data"`
	StepDuration time.Duration                `json:"step,omitempty"`
	ExtentList   timeseries.ExtentList        `json:"extents,omitempty"`
	Serializers  map[string]func(interface{}) `json:"-"`
	SeriesOrder  []string                     `json:"series_order,omitempty"`

	timestamps map[time.Time]bool // tracks unique timestamps in the matrix data
	tslist     times.Times
	isSorted   bool // tracks if the matrix data is currently sorted
	isCounted  bool // tracks if timestamps slice is up-to-date

}

// MarshalTimeseries converts a Timeseries into a JSON blob
func (c *Client) MarshalTimeseries(ts timeseries.Timeseries) ([]byte, error) {
	// Marshal the Envelope back to a json object for Cache Storage
	return json.Marshal(ts.(*ResultsEnvelope))
}

// UnmarshalTimeseries converts a JSON blob into a Timeseries
func (c *Client) UnmarshalTimeseries(data []byte) (timeseries.Timeseries, error) {
	re := &ResultsEnvelope{}
	err := json.Unmarshal(data, re)
	return re, err
}

// Parts ...
func (rv ResponseValue) Parts(timeKey, valKey string) (string, time.Time, float64, ResponseValue) {

	if len(rv) < 3 {
		return noParts()
	}

	labels := make([]string, 0, len(rv)-2)
	var t time.Time
	var val float64
	var err error

	meta := make(ResponseValue)

	for k, v := range rv {
		switch k {
		case timeKey:
			t, err = msToTime(v.(string))
			if err != nil {
				return noParts()
			}
		case valKey:
			if av, ok := v.(float64); ok {
				val = av
				continue
			}
			val, err = strconv.ParseFloat(v.(string), 64)
			if err != nil {
				return noParts()
			}
		default:
			meta[k] = v
			labels = append(labels, fmt.Sprintf("%s=%v", k, v))
		}
	}
	sort.Strings(labels)
	return fmt.Sprintf("{%s}", strings.Join(labels, ";")), t, val, meta
}

func noParts() (string, time.Time, float64, ResponseValue) {
	return "{}", time.Time{}, 0.0, ResponseValue{}
}

// MarshalJSON ...
func (re ResultsEnvelope) MarshalJSON() ([]byte, error) {

	if len(re.Meta) < 2 {
		return nil, fmt.Errorf("Must have at least two fields; only have %d", len(re.Meta))
	}

	var mpl, fl int
	for _, v := range re.Data {
		lp := len(v.Points)
		fl += lp
		if mpl < lp {
			mpl = lp
		}
	}

	rsp := &Response{
		Meta:         re.Meta,
		RawData:      make([]ResponseValue, 0, fl),
		Rows:         re.ValueCount(),
		StepDuration: re.StepDuration,
		ExtentList:   re.ExtentList,
	}

	rsp.Order = make([]string, 0, len(re.Meta))
	for _, k := range re.Meta {
		rsp.Order = append(rsp.Order, k.Name)
	}

	// Assume the first item in the meta array is the time, and the second is the value
	timestampFieldName := rsp.Order[0]
	valueFieldName := rsp.Order[1]

	tm := make(map[time.Time][]ResponseValue)
	tl := make(times.Times, 0, mpl)

	l := len(re.Data)

	prepareMarshalledPoints := func(ds *DataSet) {

		var ok bool
		var t []ResponseValue

		for _, p := range ds.Points {

			t, ok = tm[p.Timestamp]
			if !ok {
				tl = append(tl, p.Timestamp)
				t = make([]ResponseValue, 0, l)
			}

			r := ResponseValue{
				timestampFieldName: strconv.FormatInt(p.Timestamp.UnixNano()/int64(time.Millisecond), 10),
				valueFieldName:     strconv.FormatFloat(p.Value, 'f', -1, 64),
			}
			for k2, v2 := range ds.Metric {
				r[k2] = v2
			}

			t = append(t, r)
			tm[p.Timestamp] = t
		}
	}

	for _, key := range re.SeriesOrder {
		if ds, ok := re.Data[key]; ok {
			prepareMarshalledPoints(ds)
		}
	}

	sort.Sort(tl)

	for _, t := range tl {
		rsp.RawData = append(rsp.RawData, tm[t]...)
	}

	bytes, err := json.Marshal(rsp)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}

// MarshalJSON ...
func (rsp *Response) MarshalJSON() ([]byte, error) {

	//var marshalers = map[string]func(interface{}){}

	buf := &bytes.Buffer{}
	buf.WriteString(`{"meta":`)
	meta, _ := json.Marshal(rsp.Meta)
	buf.Write(meta)
	buf.WriteString(`,"data":[`)
	d := make([]string, 0, len(rsp.RawData))
	for _, rd := range rsp.RawData {
		d = append(d, string(rd.ToJSON(rsp.Order)))
	}
	buf.WriteString(strings.Join(d, ",") + "]")
	buf.WriteString(fmt.Sprintf(`,"rows": %d`, rsp.Rows))

	if rsp.ExtentList != nil && len(rsp.ExtentList) > 0 {
		el, _ := json.Marshal(rsp.ExtentList)
		buf.WriteString(fmt.Sprintf(`,"extents": %s`, string(el)))
	}

	buf.WriteString("}")

	b := buf.Bytes()

	return b, nil
}

// ToJSON ...
func (rv ResponseValue) ToJSON(order []string) []byte {
	buf := &bytes.Buffer{}
	buf.WriteString("{")
	lines := make([]string, 0, len(rv))
	for _, k := range order {
		if v, ok := rv[k]; ok {

			// cleanup here
			j, err := json.Marshal(v)
			if err != nil {
				continue
			}
			lines = append(lines, fmt.Sprintf(`"%s":%s`, k, string(j)))
		}
	}
	buf.WriteString(strings.Join(lines, ",") + "}")
	return buf.Bytes()
}

// UnmarshalJSON ...
func (re *ResultsEnvelope) UnmarshalJSON(b []byte) error {

	response := Response{}
	err := json.Unmarshal(b, &response)
	if err != nil {
		return err
	}

	if len(response.Meta) < 2 {
		return fmt.Errorf("Must have at least two fields; only have %d", len(response.Meta))
	}

	re.Meta = response.Meta
	re.ExtentList = response.ExtentList
	re.StepDuration = response.StepDuration
	re.SeriesOrder = make([]string, 0)

	// Assume the first item in the meta array is the time field, and the second is the value field
	timestampFieldName := response.Meta[0].Name
	valueFieldName := response.Meta[1].Name

	registeredMetrics := make(map[string]bool)

	re.Data = make(map[string]*DataSet)
	l := len(response.RawData)
	for _, v := range response.RawData {
		metric, ts, val, meta := v.Parts(timestampFieldName, valueFieldName)
		if _, ok := registeredMetrics[metric]; !ok {
			registeredMetrics[metric] = true
			re.SeriesOrder = append(re.SeriesOrder, metric)
		}
		if !ts.IsZero() {
			a, ok := re.Data[metric]
			if !ok {
				a = &DataSet{Metric: meta, Points: make([]Point, 0, l)}
			}
			a.Points = append(a.Points, Point{Timestamp: ts, Value: val})
			re.Data[metric] = a
		}
	}

	return nil
}

// Len returns the length of a slice of time series data points
func (p Points) Len() int {
	return len(p)
}

// Less returns true if i comes before j
func (p Points) Less(i, j int) bool {
	return p[i].Timestamp.Before(p[j].Timestamp)
}

// Swap modifies a slice of time series data points by swapping the values in indexes i and j
func (p Points) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}
