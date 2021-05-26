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

// Package prometheus is a rudimentary Prometheus HTTP APIv1 output simulator,
// intended for use with unit testing that would normally require a running Prometheus server.
// mockster/prometheus outputs repeatable, Prometheus-formatted data, synthetically generated from query and timestamp.
// It does not validate queries and does not produce output that accurately depicts data shapes expected of the query.
// They will probably look really ugly on an actual graph
// mockster/prometheus currently only supports matrix responses to a query_range request
package prometheus

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

const (
	lpRepeatableRandom = "repeatable_random"
	lpUsageCurve       = "usage_curve"
	secondsPerDay      = 86400
)

const (
	mdSeriesCount  = "series_count"
	mdLatency      = "latency_ms"
	mdRangeLatency = "range_latency_ms"
	mdMaxVal       = "max_value"
	mdMinVal       = "min_value"
	mdSeriesID     = "series_id"
	mdStatusCode   = "status_code"
	mdInvalidBody  = "invalid_response_body"
	mdLinePattern  = "line_pattern"
)

// Modifiers represents a collection of modifiers for the simulator's behavior provided by the user
type Modifiers struct {
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
	// LinePattern indicates the pattern/shape of the resulting timeseries
	LinePattern string

	rawString string
	seriesID  int
	seedFunc  func(d *Modifiers, seriesIndex int, querySeed int64, t time.Time) int
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

	d := getModifiers(query)
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
		d1 := &Modifiers{rawString: d.rawString}
		d1.addLabel(fmt.Sprintf(`"%s":"%d"`, mdSeriesID, i))
		series = append(series, fmt.Sprintf(`{"metric":{%s},"value":[%d,"%d"]}`, d1.rawString, t.Unix(), d.seedFunc(d, i, queryVal, t)))
	}
	return fmt.Sprintf(`{"status":"%s","data":{"resultType":"vector","result":[`, status) + strings.Join(series, ",") + "]}}", d.StatusCode, nil
}

// GetTimeSeriesData returns a simulated Matrix Envelope with repeatable results
func GetTimeSeriesData(query string, start time.Time, end time.Time, step time.Duration) (string, int, error) {

	d := getModifiers(query)

	if d.Latency > 0 {
		time.Sleep(d.Latency)
	}

	if d.InvalidResponseBody > 0 {
		return "foo", d.StatusCode, nil
	}

	status := "success"
	seriesLen := int(end.Sub(start) / step)
	start = end.Add(time.Duration(-seriesLen) * step)
	queryVal := getQueryVal(query)

	var b strings.Builder
	b.Grow(d.SeriesCount * seriesLen * 18)
	sep1 := ","
	fmt.Fprintf(&b, `{"status":"%s","data":{"resultType":"matrix","result":[`, status)
	for i := 0; d.SeriesCount > i; i++ {
		sep2 := ","
		if i == d.SeriesCount-1 {
			sep1 = ""
		}
		d1 := &Modifiers{rawString: d.rawString}
		d1.addLabel(fmt.Sprintf(`"%s":"%d"`, mdSeriesID, i))
		fmt.Fprintf(&b, `{"metric":{%s},"values":[`, d1.rawString)
		for j := 0; j <= seriesLen; j++ {
			if j == seriesLen {
				sep2 = ""
			}
			t := start.Add(time.Duration(j) * step)
			fmt.Fprintf(&b, `[%d,"%d"]%s`, t.Unix(), d.seedFunc(d, i, queryVal, t), sep2)
		}
		fmt.Fprintf(&b, `]}%s`, sep1)
	}
	b.WriteString("]}}")

	return b.String(), d.StatusCode, nil
}

func getModifiers(query string) *Modifiers {

	var err error
	var i int64

	d := &Modifiers{
		InvalidResponseBody: 0,
		Latency:             0,
		RangeLatency:        0,
		MaxValue:            100,
		MinValue:            0,
		SeriesCount:         1,
		seriesID:            0,
		StatusCode:          200,
		LinePattern:         lpRepeatableRandom,
		seedFunc:            repeatableRandomVal,
	}

	provided := []string{}

	start := strings.Index(query, "{")
	if start > -1 {
		start++
		end := strings.LastIndex(query, "}")
		if end > start {
			mods := strings.Split(query[start:end], ",")
			for _, mod := range mods {
				parts := strings.SplitN(mod, "=", 2)
				if len(parts) != 2 {
					provided = append(provided, fmt.Sprintf(`"%s":""`, mod))
					continue
				}
				parts[1] = strings.Replace(parts[1], `"`, ``, -1)
				i, err = strconv.ParseInt(parts[1], 10, 64)
				if err != nil {
					switch parts[0] {
					case mdLinePattern:
						d.LinePattern = parts[1]
					}
				} else {
					switch parts[0] {
					case mdSeriesCount:
						d.SeriesCount = int(i)
					case mdLatency:
						d.Latency = time.Duration(i) * time.Millisecond
					case mdRangeLatency:
						d.RangeLatency = time.Duration(i) * time.Millisecond
					case mdMaxVal:
						d.MaxValue = int(i)
					case mdMinVal:
						d.MinValue = int(i)
					case mdSeriesID:
						d.seriesID = int(i)
					case mdStatusCode:
						d.StatusCode = int(i)
					case mdInvalidBody:
						d.InvalidResponseBody = int(i)
					}
				}
				provided = append(provided, fmt.Sprintf(`"%s":"%s"`, parts[0], parts[1]))
			}
		}
	}

	// this determines, based on the provided LinePattern, what value generator function to call
	// if the LinePattern is not provided, or the pattern name is not registered below in seedFuncs
	// then the repeatable random value generator func will be used
	if lp, ok := seedFuncs[d.LinePattern]; ok {
		d.seedFunc = lp
	}

	d.rawString = strings.Join(provided, ",")
	return d
}

var seedFuncs = map[string]func(d *Modifiers, seriesIndex int, querySeed int64, t time.Time) int{
	lpRepeatableRandom: repeatableRandomVal,
	lpUsageCurve:       usageCurveVal,
}

func repeatableRandomVal(d *Modifiers, seriesIndex int, querySeed int64, t time.Time) int {
	if d.RangeLatency > 0 {
		time.Sleep(d.RangeLatency)
	}
	rand.Seed(querySeed + int64(seriesIndex) + t.Unix())
	return d.MinValue + rand.Intn(d.MaxValue-d.MinValue)
}

func usageCurveVal(d *Modifiers, seriesIndex int, querySeed int64, t time.Time) int {
	if d.RangeLatency > 0 {
		time.Sleep(d.RangeLatency)
	}

	// Scale the max randomly if it is not index 0
	max := d.MaxValue
	if seriesIndex != 0 {
		rand.Seed(int64(seriesIndex) + querySeed)
		scale := rand.Float32()*.5 + .5
		max = int(float32(max-d.MinValue)*scale) + d.MinValue
	}
	_, offset := t.Zone()
	seconds := (t.Unix() + int64(offset)) % secondsPerDay
	A := float64(max-d.MinValue) / 2 // Amplitude
	B := math.Pi * 2 / secondsPerDay // Period
	C := 4.0 * 3600.0                // Phase shift back to 8pm
	D := A + float64(d.MinValue)     // Vertical shift
	return int(A*math.Cos(B*(float64(seconds)+C)) + D)
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

func (d *Modifiers) addLabel(in string) {
	if len(d.rawString) == 0 {
		d.rawString = in
		return
	}
	d.rawString += "," + in
}
