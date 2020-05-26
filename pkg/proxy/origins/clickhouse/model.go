/*
 * Copyright 2020 Comcast Cable Communications Management, LLC
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

package clickhouse

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tricksterproxy/trickster/pkg/sort/times"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

const (
	millisPerSecond     = int64(time.Second / time.Millisecond)
	nanosPerMillisecond = int64(time.Millisecond / time.Nanosecond)
)

type FieldDefinition struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type ResponseValue map[string]interface{}

type Point struct {
	Timestamp time.Time
	Values    ResponseValue
}

// Response is the JSON response document structure for ClickHouse query results
type Response struct {
	Meta    []FieldDefinition `json:"meta"`
	RawData []ResponseValue   `json:"data"`
	Rows    int               `json:"rows"`
}

// ResultsEnvelope is the ClickHouse document structure optimized for time series manipulation
type ResultsEnvelope struct {
	Meta         []FieldDefinition     `json:"meta"`
	Data         []Point               `json:"data"`
	StepDuration time.Duration         `json:"step,omitempty"`
	ExtentList   timeseries.ExtentList `json:"extents,omitempty"`

	timestamps map[time.Time]bool // tracks unique timestamps in the matrix data
	tsList     times.Times
	isSorted   bool
	isCounted  bool
}

type fromTimeFunc func(interface{}) (time.Time, error)
type toTimeFunc func(time.Time) interface{}

var toMsString toTimeFunc = func(t time.Time) interface{} {
	return strconv.FormatInt(t.UnixNano()/nanosPerMillisecond, 10)
}

var fromMsString fromTimeFunc = func(v interface{}) (time.Time, error) {
	msInt, err := strconv.ParseInt(v.(string), 10, 64)
	if err == nil {
		return time.Unix(msInt/millisPerSecond, (msInt%millisPerSecond)*nanosPerMillisecond), nil
	}
	return time.Time{}, err
}

var fromSec fromTimeFunc = func(v interface{}) (time.Time, error) {
	return time.Unix(int64(v.(float64)), 0), nil
}
var toSec toTimeFunc = func(t time.Time) interface{} {
	return t.Unix()
}

const chLayout = "2006-01-02 15:04:05"

var fromDateString fromTimeFunc = func(v interface{}) (time.Time, error) {
	return time.Parse(chLayout, v.(string))
}

var toDateString toTimeFunc = func(t time.Time) interface{} {
	return t.Format(chLayout)
}

// Converts a Timeseries into a JSON blob
func (c *Client) MarshalTimeseries(ts timeseries.Timeseries) ([]byte, error) {
	return json.Marshal(ts.(*ResultsEnvelope))
}

// Converts a JSON blob into a Timeseries
func (c *Client) UnmarshalTimeseries(data []byte) (timeseries.Timeseries, error) {
	re := &ResultsEnvelope{}
	err := json.Unmarshal(data, re)
	return re, err
}

func (re ResultsEnvelope) MarshalJSON() ([]byte, error) {
	if len(re.Meta) == 0 {
		return nil, fmt.Errorf("no metadata in ResultsEnvelope")
	}
	tsField := re.Meta[0].Name
	tsType := re.Meta[0].Type
	var ttf toTimeFunc
	if strings.HasPrefix(tsType, "DateTime") {
		ttf = toDateString
	} else if strings.HasSuffix(tsType, "t64") {
		ttf = toMsString
	} else if strings.HasSuffix(tsType, "t32") {
		ttf = toSec
	} else {
		return nil, fmt.Errorf("unrecognized timestamp type")
	}
	rsp := &Response{
		Meta:    re.Meta,
		RawData: make([]ResponseValue, 0, len(re.Data)),
		Rows:    re.ValueCount(),
	}
	for _, p := range re.Data {
		rv := ResponseValue{tsField: ttf(p.Timestamp)}
		for k, v := range p.Values {
			rv[k] = v
		}
		rsp.RawData = append(rsp.RawData, rv)
	}
	return json.Marshal(rsp)
}

func (re ResultsEnvelope) SeriesCount() int {
	return 1
}

func (re *ResultsEnvelope) UnmarshalJSON(b []byte) error {
	response := Response{}
	if err := json.Unmarshal(b, &response); err != nil {
		return err
	}
	re.Meta = response.Meta
	re.Data = make([]Point, 0, len(response.RawData))

	if len(response.RawData) == 0 {
		return nil // No data points, we're done
	}

	tsField := response.Meta[0].Name
	tsType := response.Meta[0].Type

	var ftf fromTimeFunc
	if strings.HasPrefix(tsType, "DateTime") {
		ftf = fromDateString
	} else if strings.HasSuffix(tsType, "t64") {
		ftf = fromMsString
	} else if strings.HasSuffix(tsType, "t32") {
		ftf = fromSec
	} else {
		return fmt.Errorf("timestamp field not of recognized type")
	}

	for _, v := range response.RawData {
		tv, ok := v[tsField]
		if !ok {
			return fmt.Errorf("missing timestamp field in response data")
		}
		ts, err := ftf(tv)
		if err != nil {
			return fmt.Errorf("timestamp field does not parse to date")
		}
		delete(v, tsField)
		re.Data = append(re.Data, Point{Timestamp: ts, Values: v})
	}
	re.Sort()
	return nil
}
