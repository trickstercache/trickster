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

// Package promsim is a rudimentary Prometheus HTTP APIv1 output simulator,
// intended for use with unit testing that would normally require a running Prometheus server.
// PromSim outputs repeatable, promethues-formatted data, synthetically generated from query and timestamp.
// It does not validate queries and does not produce output that accurately depicts data shapes expected of the query.
// They will probably look really ugly on an actual graph
// PromSim currently only supports matrix responses to a query_range request
package promsim

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Directives represents a collection of modifiers for the simulator's behavior provided by the user
type Directives struct {
	// RawString returns the raw directives found in the provided query
	RawString string
	// SeriesCount defines how many series to return
	SeriesCount int
	// Latency introduces a static delay in responding to each request
	Latency time.Duration
	// RangeLatency introduces an additional delay as a multiple of the number of timestamps in the series
	RangeLatency time.Duration
	// MaxValue limits the maximum value of any data in the query result
	MaxValue int64
	// MinValue limits the minimum value of any data in the query result
	// Not currently implemented
	MinValue int64
	// StatusCode indicates the desired return status code, to simulate errors
	StatusCode int
	// InvalidResponseBody when > 0 causes the server to respond with a payload that cannot be unmarshaled
	// useful for causing and testing unmarshling failure cases
	InvalidResponseBody int

	seriesID int
}

// Result is a simplified version of a Prometheus timeseries response
type Result struct {
	Metric string
	Values string
}

// GetInstantData returns a simulated Vector Envelope with repeatable results
func GetInstantData(query string, t time.Time) (string, int, error) {

	if t.IsZero() {
		t = time.Now()
	}

	d := getDirectives(query)
	if d.Latency > 0 {
		time.Sleep(d.Latency)
	}

	if d.InvalidResponseBody > 0 {
		return "foo", d.StatusCode, nil
	}

	status := "success"
	series := make([]string, 0, d.SeriesCount)
	queryVal := getQueryVal(query)

	for i := 0; d.SeriesCount > i; i++ {
		m := getDirectives(d.RawString + fmt.Sprintf(`,"series_id":"%d"`, i))
		series = append(series, fmt.Sprintf(`{"metric":{%s},"value":[%d,"%d"]}`, m.RawString, t.Unix(), calculateValue(queryVal, i, t, d)))
	}
	return fmt.Sprintf(`{"status":"%s","data":{"resultType":"vector","result":[`, status) + strings.Join(series, ",") + "]}}", d.StatusCode, nil
}

// GetTimeSeriesData returns a simulated Matrix Envelope with repeatable results
func GetTimeSeriesData(query string, start time.Time, end time.Time, step time.Duration) (string, int, error) {

	d := getDirectives(query)

	if d.Latency > 0 {
		time.Sleep(d.Latency)
	}

	if d.InvalidResponseBody > 0 {
		return "foo", d.StatusCode, nil
	}

	status := "success"
	seriesLen := int(end.Sub(start) / step)
	start = end.Add(time.Duration(-seriesLen) * step)
	series := make([]string, 0, d.SeriesCount)
	queryVal := getQueryVal(query)

	for i := 0; d.SeriesCount > i; i++ {
		m := getDirectives(d.RawString + fmt.Sprintf(`,"series_id":"%d"`, i))
		v := make([]string, 0, seriesLen)
		for j := 0; j <= seriesLen; j++ {
			t := start.Add(time.Duration(j) * step)
			v = append(v, fmt.Sprintf(`[%d,"%d"]`, t.Unix(), calculateValue(queryVal, i, t, d)))
		}
		series = append(series, fmt.Sprintf(`{"metric":{%s},"values":[%s]}`, m.RawString, strings.Join(v, ",")))
	}

	out := fmt.Sprintf(`{"status":"%s","data":{"resultType":"matrix","result":[`, status) + strings.Join(series, ",") + "]}}"

	return out, d.StatusCode, nil
}

func getDirectives(query string) *Directives {

	var err error
	var k string
	var i int64

	d := &Directives{
		InvalidResponseBody: 0,
		Latency:             0,
		RangeLatency:        0,
		MaxValue:            100,
		MinValue:            0,
		SeriesCount:         1,
		seriesID:            0,
		StatusCode:          200,
	}

	extras := []string{}

	start := strings.Index(query, "{")
	if start > -1 {
		start++
		end := strings.LastIndex(query, "}")
		if end > start {
			dirs := strings.Split(query[start:end], ",")
			for _, dir := range dirs {

				parts := strings.SplitN(dir, "=", 2)
				if len(parts) == 2 {
					i, err = strconv.ParseInt(parts[1], 10, 64)
					if err != nil {
						extras = append(extras, fmt.Sprintf(`"%s":"%s"`, parts[0], parts[1]))
						continue
					}
					k = parts[0]
				} else {
					extras = append(extras, fmt.Sprintf(`"%s":""`, dir))
					continue
				}

				switch k {
				case "series_count":
					d.SeriesCount = int(i)
				case "latency_ms":
					d.Latency = time.Duration(i) * time.Millisecond
				case "range_latency_ms":
					d.RangeLatency = time.Duration(i) * time.Millisecond
				case "max_val":
					d.MaxValue = i
				case "min_val":
					d.MinValue = i
				case "series_id":
					d.seriesID = int(i)
				case "status_code":
					d.StatusCode = int(i)
				case "invalid_response_body":
					d.InvalidResponseBody = int(i)
				default:
					extras = append(extras, fmt.Sprintf(`"%s":"%s"`, parts[0], parts[1]))
				}
			}
		}
	}

	sep := ""
	if len(extras) > 0 {
		sep = ","
	}

	d.RawString = fmt.Sprintf(`"invalid_response_body":"%d","latency_ms":"%d","max_value":"%d","min_value":"%d","range_latency_ms":"%d","series_count":"%d","series_id":"%d","status_code":"%d"%s%s`,
		d.InvalidResponseBody, d.Latency/1000000, d.MaxValue, d.MinValue, d.RangeLatency/1000000, d.SeriesCount, d.seriesID, d.StatusCode, sep, strings.Join(extras, ","))

	return d
}

func calculateValue(queryVal int64, seriesIndex int, t time.Time, d *Directives) int64 {

	if d.RangeLatency > 0 {
		time.Sleep(d.RangeLatency)
	}

	v := ((((queryVal+int64(seriesIndex))*t.Unix() + queryVal) / queryVal * 427879) % 23455379) % d.MaxValue

	return v

}

func getQueryVal(query string) int64 {
	l := len(query)
	var v int64
	for i := 0; i < l; i++ {
		v += int64(query[i])
	}
	return v
}
