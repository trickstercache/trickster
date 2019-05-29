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
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// Directives represents a collection of modifiers for the simulator's behavior provided by the user
type Directives struct {
	// SeriesCount defines how many series to return
	SeriesCount int
	// Latency introduces a static delay in responding to each request
	Latency time.Duration
	// RangeLatency introduces an additional delay as a multiple of the number of timestamps in the series
	RangeLatency time.Duration
	// MaxValue limits the maximum value of any data in the query result
	MaxValue int
	// MinValue limits the minimum value of any data in the query result
	MinValue int
	// StatusCode indicates the desired return status code, to simulate errors
	StatusCode int
	// InvalidResponseBody when > 0 causes the server to respond with a payload that cannot be unmarshaled
	// useful for causing and testing unmarshling failure cases
	InvalidResponseBody int

	rawString string
	seriesID  int
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
		d1 := &Directives{rawString: d.rawString}
		d1.addLabel(fmt.Sprintf(`"series_id":"%d"`, i))
		series = append(series, fmt.Sprintf(`{"metric":{%s},"value":[%d,"%d"]}`, d1.rawString, t.Unix(), seededVal(d, i, queryVal, t)))
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
		d1 := &Directives{rawString: d.rawString}
		d1.addLabel(fmt.Sprintf(`"series_id":"%d"`, i))
		v := make([]string, 0, seriesLen)
		for j := 0; j <= seriesLen; j++ {
			t := start.Add(time.Duration(j) * step)
			v = append(v, fmt.Sprintf(`[%d,"%d"]`, t.Unix(), seededVal(d, i, queryVal, t)))
		}
		series = append(series, fmt.Sprintf(`{"metric":{%s},"values":[%s]}`, d1.rawString, strings.Join(v, ",")))
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

	provided := []string{}

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
						provided = append(provided, fmt.Sprintf(`"%s":"%s"`, parts[0], parts[1]))
						continue
					}
					k = parts[0]
				} else {
					provided = append(provided, fmt.Sprintf(`"%s":""`, dir))
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
					d.MaxValue = int(i)
				case "min_val":
					d.MinValue = int(i)
				case "series_id":
					d.seriesID = int(i)
				case "status_code":
					d.StatusCode = int(i)
				case "invalid_response_body":
					d.InvalidResponseBody = int(i)
				}
				provided = append(provided, fmt.Sprintf(`"%s":"%s"`, parts[0], parts[1]))
			}
		}
	}
	d.rawString = strings.Join(provided, ",")
	return d
}

func seededVal(d *Directives, seriesIndex int, querySeed int64, t time.Time) int {
	if d.RangeLatency > 0 {
		time.Sleep(d.RangeLatency)
	}
	rand.Seed(querySeed + int64(seriesIndex) + t.Unix())
	return d.MinValue + rand.Intn(d.MaxValue-d.MinValue)
}

// Calculates a number for the Query Value
func getQueryVal(query string) int64 {
	l := len(query)
	var v int64
	for i := 0; i < l; i++ {
		v += int64(query[i])
	}
	v = v * v * v
	return v
}

func (d *Directives) addLabel(in string) {
	if len(d.rawString) == 0 {
		d.rawString = in
		return
	}
	d.rawString += "," + in
}
