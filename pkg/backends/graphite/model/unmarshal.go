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

// ErrStepMismatch is returned when a response's timestamps disagree with the
// TimeRangeQuery's predicted step; such a response must not be cached under its key
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

// StepAmbiguityNoter is implemented by a TimeRangeQuery.ParsedQuery that
// wants to know when a JSON response could not confirm the predicted step
type StepAmbiguityNoter interface {
	NoteAmbiguousStep(seriesName string, predicted time.Duration)
}

// ErrStepAmbiguous is returned for a predicted-step JSON fetch whose
// response carries too little data to verify the prediction
var ErrStepAmbiguous = errors.New("graphite: response cannot verify the predicted step")

// StepAmbiguousError carries the series that failed verification
type StepAmbiguousError struct {
	Target string
	Points int
}

func (e *StepAmbiguousError) Error() string {
	return fmt.Sprintf("%s: %q returned %d points", ErrStepAmbiguous.Error(), e.Target, e.Points)
}

func (e *StepAmbiguousError) Is(target error) bool { return target == ErrStepAmbiguous }

// StepMismatchNoter is implemented by a TimeRangeQuery.ParsedQuery that wants
// to be told when a response contradicted the predicted step
type StepMismatchNoter interface {
	NoteStepMismatch(target string, predicted, observed time.Duration)
}

// wire JSON shape: [{"target": ..., "tags": {...}, "datapoints": [[v, ts], ...]}]
type wireSeries struct {
	Target     string            `json:"target"`
	Tags       map[string]string `json:"tags"`
	Datapoints []wireDatapoint   `json:"datapoints"`
}

type wireDatapoint struct {
	val  float64
	ts   int64
	null bool
}

func (d *wireDatapoint) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) < 2 || b[0] != '[' || b[len(b)-1] != ']' {
		return timeseries.ErrInvalidBody
	}
	inner := b[1 : len(b)-1]
	comma := bytes.IndexByte(inner, ',')
	if comma < 0 || bytes.IndexByte(inner[comma+1:], ',') >= 0 {
		return timeseries.ErrInvalidBody
	}
	vs := string(bytes.TrimSpace(inner[:comma]))
	if vs == "null" {
		d.null = true
	} else {
		f, err := strconv.ParseFloat(vs, 64)
		if err != nil {
			return err
		}
		d.val = f
	}
	// graphite-web emits timestamps as integers, but a float here would
	// have decoded before this custom decoder existed, so it still does
	ts, err := strconv.ParseFloat(string(bytes.TrimSpace(inner[comma+1:])), 64)
	if err != nil {
		return err
	}
	d.ts = int64(ts)
	return nil
}

func spacingViolation(trq *timeseries.TimeRangeQuery, predicted time.Duration, target string) error {
	if predicted > 0 {
		if n, ok := trq.ParsedQuery.(StepAmbiguityNoter); ok && n != nil {
			n.NoteAmbiguousStep(target, predicted)
		}
	}
	return timeseries.ErrInvalidBody
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
	predicted := trq.Step
	var wire []wireSeries
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, err
	}
	out := make([]*dataset.Series, 0, len(wire))
	for _, ws := range wire {
		pts := make(dataset.Points, len(ws.Datapoints))
		vals := make([]any, len(ws.Datapoints))
		var stepSecs int64
		for i, dp := range ws.Datapoints {
			if i == 1 {
				stepSecs = dp.ts - ws.Datapoints[0].ts
				if stepSecs <= 0 {
					return nil, spacingViolation(trq, predicted, ws.Target)
				}
			} else if i > 1 && dp.ts != ws.Datapoints[0].ts+int64(i)*stepSecs {
				return nil, spacingViolation(trq, predicted, ws.Target)
			}
			pts[i] = dataset.Point{Epoch: epoch.FromSecs(dp.ts), Values: vals[i : i+1 : i+1], Size: 24}
			if !dp.null {
				vals[i] = dp.val
			}
		}
		s, err := newSeries(ws.Target, ws.Tags, pts, trq)
		if err != nil {
			return nil, err
		}
		if len(pts) < 2 && predicted > 0 {
			if n, ok := trq.ParsedQuery.(StepAmbiguityNoter); ok && n != nil {
				n.NoteAmbiguousStep(ws.Target, trq.Step)
			}
			return nil, &StepAmbiguousError{Target: ws.Target, Points: len(pts)}
		}
		out = append(out, s)
	}
	if len(out) == 0 && predicted > 0 {
		if n, ok := trq.ParsedQuery.(StepAmbiguityNoter); ok && n != nil {
			n.NoteAmbiguousStep("", trq.Step)
		}
		return nil, &StepAmbiguousError{Target: "", Points: 0}
	}
	return out, nil
}

// parses format=raw: <target>,<start>,<end>,<step>|v,v,None
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

// StepField is the timestamp field definition of a series; its DefaultValue carries
// the native step in seconds so a series with fewer than two points renders correctly
func StepField(step time.Duration) timeseries.FieldDefinition {
	fd := timeseries.FieldDefinition{Name: TimestampFieldName, DataType: timeseries.Int64, Role: timeseries.RoleTimestamp}
	if step > 0 {
		fd.DefaultValue = strconv.FormatInt(int64(step/time.Second), 10)
	}
	return fd
}

// reads the native step recorded by StepField (0 if none)
func seriesStep(sh *dataset.SeriesHeader) int64 {
	if sh.TimestampField.DefaultValue == "" {
		return 0
	}
	n, _ := strconv.ParseInt(sh.TimestampField.DefaultValue, 10, 64)
	return n
}

func newPoint(e epoch.Epoch, v *float64) dataset.Point {
	p := dataset.Point{Epoch: e, Values: []any{nil}, Size: 24}
	if v != nil {
		p.Values[0] = *v
	}
	return p
}

// builds a Series and verifies the observed step against the TimeRangeQuery's
// predicted step; a query with no step yet adopts the observed one
func newSeries(name string, tags map[string]string, pts dataset.Points,
	trq *timeseries.TimeRangeQuery,
) (*dataset.Series, error) {
	step := trq.Step
	if len(pts) >= 2 {
		observed := time.Duration(pts[1].Epoch-pts[0].Epoch) * time.Nanosecond
		if observed <= 0 {
			return nil, timeseries.ErrInvalidBody
		}
		switch {
		case trq.Step == 0:
			trq.Step = observed
		case observed != trq.Step:
			if n, ok := trq.ParsedQuery.(StepMismatchNoter); ok && n != nil {
				n.NoteStepMismatch(name, trq.Step, observed)
			}
			return nil, &StepMismatchError{Predicted: trq.Step, Observed: observed, Target: name}
		}
		step = observed
	}
	if tags == nil {
		tags = map[string]string{"name": name}
	}
	sh := dataset.SeriesHeader{
		Name:            name,
		Tags:            dataset.Tags(tags),
		QueryStatement:  trq.Statement,
		TimestampField:  StepField(step),
		ValueFieldsList: timeseries.FieldDefinitions{{Name: ValueFieldName, DataType: timeseries.Float64, Role: timeseries.RoleValue, OutputPosition: 1}},
	}
	sh.CalculateSize()
	return &dataset.Series{Header: sh, Points: pts, PointSize: int64(len(pts)) * 24}, nil
}
