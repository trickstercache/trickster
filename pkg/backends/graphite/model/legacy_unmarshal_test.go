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
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// wire JSON shape: [{"target": ..., "tags": {...}, "datapoints": [[v, ts], ...]}]
type legacyWireSeries struct {
	Target     string                `json:"target"`
	Tags       map[string]string     `json:"tags"`
	Datapoints []legacyWireDatapoint `json:"datapoints"`
}

type legacyWireDatapoint struct {
	val  float64
	ts   int64
	null bool
}

func (d *legacyWireDatapoint) UnmarshalJSON(b []byte) error {
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

func legacySpacingViolation(trq *timeseries.TimeRangeQuery, predicted time.Duration, target string) error {
	if predicted > 0 {
		if n, ok := trq.ParsedQuery.(StepAmbiguityNoter); ok && n != nil {
			n.NoteAmbiguousStep(target, predicted)
		}
	}
	return timeseries.ErrInvalidBody
}

// legacyUnmarshalTimeseriesReader is the decoder the stream decoder replaced, its conformance oracle
func legacyUnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
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
		series, err = legacyUnmarshalJSON(trimmed, trq)
	} else {
		series, err = legacyUnmarshalRaw(trimmed, trq)
	}
	if err != nil {
		return nil, err
	}
	ds.Results[0].SeriesList = series
	return ds, nil
}

func legacyUnmarshalJSON(body []byte, trq *timeseries.TimeRangeQuery) ([]*dataset.Series, error) {
	predicted := trq.Step
	var wire []legacyWireSeries
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
					return nil, legacySpacingViolation(trq, predicted, ws.Target)
				}
			} else if i > 1 && dp.ts != ws.Datapoints[0].ts+int64(i)*stepSecs {
				return nil, legacySpacingViolation(trq, predicted, ws.Target)
			}
			pts[i] = dataset.Point{Epoch: epoch.FromSecs(dp.ts), Values: vals[i : i+1 : i+1]}
			if !dp.null {
				vals[i] = dp.val
			}
		}
		s, err := legacyNewSeries(ws.Target, ws.Tags, pts, trq)
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
func legacyUnmarshalRaw(body []byte, trq *timeseries.TimeRangeQuery) ([]*dataset.Series, error) {
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
				pts[i] = legacyNewPoint(epoch.FromSecs(start+int64(i)*step), f)
			}
		}
		s, err := legacyNewSeries(head, map[string]string{"name": head}, pts, trq)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func legacyNewPoint(e epoch.Epoch, v *float64) dataset.Point {
	p := dataset.Point{Epoch: e, Values: []any{nil}}
	if v != nil {
		p.Values[0] = *v
	}
	return p
}

// builds a Series and verifies the observed step against the TimeRangeQuery's
// predicted step; a query with no step yet adopts the observed one
func legacyNewSeries(name string, tags map[string]string, pts dataset.Points,
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
	return dataset.NewSeries(sh, pts), nil
}
