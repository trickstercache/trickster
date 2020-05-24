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
	"time"

	"github.com/tricksterproxy/trickster/pkg/sort/times"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

const (
	millisPerSecond     = int64(time.Second / time.Millisecond)
	nanosPerMillisecond = int64(time.Millisecond / time.Nanosecond)
)

var year2100Seconds int64

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

type timeFunc func(interface{}) (time.Time, error)

var fromMs timeFunc = func(v interface{}) (time.Time, error) {
	msInt := int64(v.(float64))
	return time.Unix(msInt/millisPerSecond, (msInt%millisPerSecond)*nanosPerMillisecond), nil
}

var fromMsString timeFunc = func(v interface{}) (time.Time, error) {
	msInt, err := strconv.ParseInt(v.(string), 10, 64)
	if err == nil {
		return time.Unix(msInt/millisPerSecond, (msInt%millisPerSecond)*nanosPerMillisecond), nil
	}
	return time.Time{}, err
}

var fromSec timeFunc = func(v interface{}) (time.Time, error) {
	return time.Unix(int64(v.(float64)), 0), nil
}

func init() {
	utcLoc, _ := time.LoadLocation("UTC")
	year2100Seconds = time.Date(2100, time.January, 1, 0, 0, 0, 0, utcLoc).Unix()
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
	rsp := &Response{
		Meta:    re.Meta,
		RawData: make([]ResponseValue, 0, len(re.Data)),
		Rows:    re.ValueCount(),
	}
	for _, p := range re.Data {
		rv := ResponseValue{tsField: strconv.FormatInt(p.Timestamp.UnixNano()/nanosPerMillisecond, 10)}
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
	// Determine time format based on first value
	var tf timeFunc
	fv := response.RawData[0][tsField]
	switch fv.(type) {
	case float64:
		tvInt64 := int64(fv.(float64))
		if tvInt64 > year2100Seconds {
			tf = fromMs
		} else {
			tf = fromSec
		}
	case string:
		tf = fromMsString
	default:
		return fmt.Errorf("timestamp field not of recognized type")
	}

	for _, v := range response.RawData {
		tv, ok := v[tsField]
		if !ok {
			return fmt.Errorf("missing timestamp field in response data")
		}
		ts, err := tf(tv)
		if err != nil {
			return fmt.Errorf("timestamp field does not parse to date")
		}
		delete(v, tsField)
		re.Data = append(re.Data, Point{Timestamp: ts, Values: v})
	}
	re.Sort()
	return nil
}
