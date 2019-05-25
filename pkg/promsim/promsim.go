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
	MinValue int64
}

// Result is a simplified version of a Prometheus timeseries response
type Result struct {
	Metric string
	Values string
}

// GetTimeSeriesData returns a simulated Matrix Envelope with repeatable results
func GetTimeSeriesData(query string, start time.Time, end time.Time, step time.Duration) (string, error) {

	d := getDirectives(query)
	if d.Latency > 0 {
		time.Sleep(d.Latency)
	}
	status := "success"
	seriesLen := int(end.Sub(start) / step)
	start = end.Add(time.Duration(-seriesLen) * step)
	series := make([]string, 0, d.SeriesCount)
	queryVal := getQueryVal(query)

	for i := 0; d.SeriesCount > i; i++ {
		m := d.RawString + fmt.Sprintf(`,"series_id":"%d"`, i)
		v := make([]string, 0, seriesLen)
		for j := 0; j < seriesLen; j++ {
			t := start.Add(time.Duration(j) * step)
			v = append(v, fmt.Sprintf(`[%d,"%d"]`, t.Unix(), calculateValue(queryVal, i, t, d)))
		}
		series = append(series, fmt.Sprintf(`{"metric":{%s},"values":%s}`, m, "["+strings.Join(v, ",")+"]"))
	}
	return fmt.Sprintf(`{"status":"%s","data":{"resultType":"matrix","result":[`, status) + strings.Join(series, ",") + "]}}", nil
}

func getDirectives(query string) *Directives {

	var err error
	var k string
	var i int64

	d := &Directives{
		SeriesCount:  1,
		Latency:      0,
		RangeLatency: 0,
		MaxValue:     100,
		MinValue:     0,
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

	d.RawString = fmt.Sprintf(`"latency_ms":"%d","max_value":"%d","min_value":"%d","range_latency_ms":"%d","series_count":"%d"%s%s`,
		d.SeriesCount, d.Latency/1000000, d.RangeLatency/1000000, d.MaxValue, d.MinValue, sep, strings.Join(extras, ","))

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
