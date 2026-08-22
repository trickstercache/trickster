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
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// ErrStepMismatch is returned when a response's timestamps disagree with
// the step the TimeRangeQuery predicted: the prediction was wrong and the
// response must not be cached under its key (implementation plan item 7.4)
var ErrStepMismatch = errors.New("graphite: response step differs from the predicted step")

// StepMismatchError carries both steps
type StepMismatchError struct {
	Predicted, Observed time.Duration
	Target              string
}

func (e *StepMismatchError) Error() string {
	return fmt.Sprintf("%v: %s predicted %v, observed %v", ErrStepMismatch, e.Target, e.Predicted, e.Observed)
}

func (e *StepMismatchError) Is(target error) bool { return target == ErrStepMismatch }

// wire JSON shape: [{"target": ..., "tags": {...}, "datapoints": [[v, ts], ...]}]
type wireSeries struct {
	Target     string            `json:"target"`
	Tags       map[string]string `json:"tags"`
	Datapoints [][2]*float64     `json:"datapoints"`
}

// UnmarshalTimeseries converts a graphite-web render response (format=json,
// or format=raw) into a DataSet
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return UnmarshalTimeseriesReader(bytes.NewReader(data), trq)
}

// UnmarshalTimeseriesReader converts a render response into a DataSet via an
// io.Reader
func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(body)
	ds := &dataset.DataSet{
		TimeRangeQuery: trq,
		ExtentList:     timeseries.ExtentList{trq.Extent},
		Results:        []*dataset.Result{{}},
	}
	if len(trimmed) == 0 {
		// raw: no series (beyond retention or no such metric)
		return ds, nil
	}
	var series []*dataset.Series
	if trimmed[0] == '[' {
		series, err = unmarshalJSON(trimmed, trq)
	} else {
		series, err = unmarshalRaw(trimmed, trq)
	}
	if err != nil {
		return nil, err
	}
	ds.Results[0].SeriesList = series
	return ds, nil
}

func unmarshalJSON(body []byte, trq *timeseries.TimeRangeQuery) ([]*dataset.Series, error) {
	var wire []wireSeries
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, err
	}
	out := make([]*dataset.Series, 0, len(wire))
	for _, ws := range wire {
		pts := make(dataset.Points, len(ws.Datapoints))
		for i, dp := range ws.Datapoints {
			if dp[1] == nil {
				return nil, timeseries.ErrInvalidBody
			}
			pts[i] = newPoint(epoch.FromSecs(int64(*dp[1])), dp[0])
		}
		s, err := newSeries(ws.Target, ws.Tags, pts, trq)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// unmarshalRaw parses format=raw: <target>,<start>,<end>,<step>|v,v,None
func unmarshalRaw(body []byte, trq *timeseries.TimeRangeQuery) ([]*dataset.Series, error) {
	var out []*dataset.Series
	for line := range strings.SplitSeq(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		head, data, ok := strings.Cut(line, "|")
		if !ok {
			return nil, timeseries.ErrInvalidBody
		}
		var nums [3]int64
		for i := 2; i >= 0; i-- {
			j := strings.LastIndexByte(head, ',')
			if j < 0 {
				return nil, timeseries.ErrInvalidBody
			}
			n, err := strconv.ParseInt(head[j+1:], 10, 64)
			if err != nil {
				return nil, timeseries.ErrInvalidBody
			}
			nums[i] = n
			head = head[:j]
		}
		start, step := nums[0], nums[2]
		if step <= 0 {
			return nil, timeseries.ErrInvalidBody
		}
		var pts dataset.Points
		if data != "" {
			vals := strings.Split(data, ",")
			pts = make(dataset.Points, len(vals))
			for i, v := range vals {
				var f *float64
				if v != "None" {
					n, err := strconv.ParseFloat(v, 64)
					if err != nil {
						return nil, timeseries.ErrInvalidBody
					}
					f = &n
				}
				pts[i] = newPoint(epoch.FromSecs(start+int64(i)*step), f)
			}
		}
		s, err := newSeries(head, map[string]string{"name": head}, pts, trq)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func newPoint(e epoch.Epoch, v *float64) dataset.Point {
	p := dataset.Point{Epoch: e, Values: []any{nil}, Size: 24}
	if v != nil {
		p.Values[0] = *v
	}
	return p
}

// newSeries builds a Series and verifies the observed step against the
// TimeRangeQuery's predicted step; when the query has no step yet, the
// observed one is adopted
func newSeries(name string, tags map[string]string, pts dataset.Points,
	trq *timeseries.TimeRangeQuery,
) (*dataset.Series, error) {
	if len(pts) >= 2 {
		observed := time.Duration(pts[1].Epoch-pts[0].Epoch) * time.Nanosecond
		if observed <= 0 {
			return nil, timeseries.ErrInvalidBody
		}
		switch {
		case trq.Step == 0:
			trq.Step = observed
		case observed != trq.Step:
			return nil, &StepMismatchError{Predicted: trq.Step, Observed: observed, Target: name}
		}
	}
	if tags == nil {
		tags = map[string]string{"name": name}
	}
	sh := dataset.SeriesHeader{
		Name:            name,
		Tags:            dataset.Tags(tags),
		QueryStatement:  trq.Statement,
		TimestampField:  timeseries.FieldDefinition{Name: TimestampFieldName, DataType: timeseries.Int64, Role: timeseries.RoleTimestamp},
		ValueFieldsList: timeseries.FieldDefinitions{{Name: ValueFieldName, DataType: timeseries.Float64, Role: timeseries.RoleValue, OutputPosition: 1}},
	}
	sh.CalculateSize()
	return &dataset.Series{Header: sh, Points: pts, PointSize: int64(len(pts)) * 24}, nil
}
