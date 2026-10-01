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
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
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

// UnmarshalTimeseries converts a graphite-web render response (format=json,
// or format=raw) into a DataSet
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return stream.BytesUnmarshaler(newDecoder)(data, trq)
}

// UnmarshalTimeseriesReader converts a render response into a DataSet via an
// io.Reader, as it's read
func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return stream.ReaderUnmarshaler(newDecoder)(reader, trq)
}

func newDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	d := &decoder{trq: trq, predicted: trq.Step, step: trq.Step, b: dataset.NewBuilder(trq, dataset.BuilderOptions{})}
	return &sniffer{d: d}, nil
}

// the members of a JSON series
const (
	memberTarget     = "target"
	memberTags       = "tags"
	memberDatapoints = "datapoints"
	tagName          = "name"
	rawNone          = "None"
)

var jsonNull = []byte("null")

// decoder builds a DataSet from a render response, a series at a time. A JSON response's step checks
// fail only once the whole body has decoded, as they did when it decoded before they ran.
type decoder struct {
	trq *timeseries.TimeRangeQuery
	b   *dataset.Builder
	// the step the query predicted, and the one the series so far agree on
	predicted, step time.Duration
	series          int
	// the first failed step check, with the noter call it makes
	failed error
	note   func()
	// a JSON series' members, and its datapoints' times and values
	target  string
	tags    map[string]string
	times   []int64
	values  []float64
	nulls   []bool
	scratch []byte
	name    []byte
	fields  [][]byte
}

// pick returns the decoder of the body's format: JSON for an array, and raw otherwise
func (d *decoder) pick(first byte) stream.Decoder {
	if first == '[' {
		return stream.NewJSON(d.walk, d.finishJSON)
	}
	return stream.NewLines(d.rawLine, d.finish).SetMaxLineBytes(math.MaxInt)
}

// empty returns the DataSet of a body of white space alone, which raw sends for no series
func (d *decoder) empty() (timeseries.Timeseries, error) {
	return &dataset.DataSet{
		TimeRangeQuery: d.trq, ExtentList: timeseries.ExtentList{d.trq.Extent}, Results: []*dataset.Result{{}},
	}, nil
}

func (d *decoder) walk(dec *jsontext.Decoder) error {
	return stream.Array(dec, func() error { return d.jsonSeries(dec) })
}

// jsonSeries reads one series of a JSON response as encoding/json read it into a struct: members
// by name ignoring case, the last of a name winning, but tags, whose objects merge
func (d *decoder) jsonSeries(dec *jsontext.Decoder) error {
	d.target, d.tags = "", nil
	d.times, d.values, d.nulls = d.times[:0], d.values[:0], d.nulls[:0]
	switch dec.PeekKind() {
	case 'n':
		if _, err := dec.ReadValue(); err != nil {
			return err
		}
	case '{':
		err := stream.ObjectBytes(dec, func(key []byte) error {
			member := stream.FieldName(key, memberTarget, memberTags, memberDatapoints)
			value, err := dec.ReadValue()
			if err != nil {
				return err
			}
			switch member {
			case memberTarget:
				return d.jsonTarget(value)
			case memberTags:
				return d.jsonTags(value)
			case memberDatapoints:
				return d.jsonDatapoints(value)
			}
			return nil
		})
		if err != nil {
			return err
		}
	default:
		if _, err := dec.ReadValue(); err != nil {
			return err
		}
		return timeseries.ErrInvalidBody
	}
	if d.failed != nil {
		return nil
	}
	return d.addSeries(d.target, d.tags, true)
}

func (d *decoder) jsonTarget(value []byte) error {
	switch value[0] {
	case 'n':
		return nil
	case '"':
		d.target = string(stream.StringText(value, &d.scratch))
		return nil
	}
	return timeseries.ErrInvalidBody
}

func (d *decoder) jsonTags(value []byte) error {
	switch value[0] {
	case 'n':
		d.tags = nil
		return nil
	case '{':
	default:
		return timeseries.ErrInvalidBody
	}
	if d.tags == nil {
		d.tags = map[string]string{}
	}
	members := stream.ObjectMembers(value)
	for name, v, ok := members.Next(); ok; name, v, ok = members.Next() {
		key := string(stream.StringText(name, &d.name))
		switch v[0] {
		case 'n':
			d.tags[key] = ""
		case '"':
			d.tags[key] = string(stream.StringText(v, &d.scratch))
		default:
			return timeseries.ErrInvalidBody
		}
	}
	return nil
}

// jsonDatapoints reads a series' datapoints, [value, timestamp] pairs of numbers, a null value its
// absence; a pair holding a string, array or object fails, as it did when decoded
func (d *decoder) jsonDatapoints(value []byte) error {
	d.times, d.values, d.nulls = d.times[:0], d.values[:0], d.nulls[:0]
	switch value[0] {
	case 'n':
		return nil
	case '[':
	default:
		return timeseries.ErrInvalidBody
	}
	// the value is valid JSON, so an element ends at its first ']' unless it holds more structure
	p := skipJSONSpace(value[1:])
	for p[0] != ']' {
		end := bytes.IndexByte(p, ']')
		if p[0] != '[' || end < 0 || bytes.ContainsAny(p[1:end], `[{"`) {
			return timeseries.ErrInvalidBody
		}
		v, ts, null, err := parseDatapoint(p[:end+1])
		if err != nil {
			return err
		}
		d.times, d.values, d.nulls = append(d.times, ts), append(d.values, v), append(d.nulls, null)
		if p = skipJSONSpace(p[end+1:]); p[0] == ',' {
			p = skipJSONSpace(p[1:])
		}
	}
	return nil
}

func skipJSONSpace(p []byte) []byte {
	for len(p) > 0 {
		switch p[0] {
		case ' ', '\t', '\n', '\r':
			p = p[1:]
			continue
		}
		break
	}
	return p
}

// parseDatapoint reads a JSON datapoint, [value, timestamp], a null value its absence; a timestamp
// graphite-web writes as an integer may have a fraction, which is dropped
func parseDatapoint(b []byte) (float64, int64, bool, error) {
	b = bytes.TrimSpace(b)
	if len(b) < 2 || b[0] != '[' || b[len(b)-1] != ']' {
		return 0, 0, false, timeseries.ErrInvalidBody
	}
	inner := b[1 : len(b)-1]
	comma := bytes.IndexByte(inner, ',')
	if comma < 0 || bytes.IndexByte(inner[comma+1:], ',') >= 0 {
		return 0, 0, false, timeseries.ErrInvalidBody
	}
	var v float64
	null := false
	if vs := bytes.TrimSpace(inner[:comma]); bytes.Equal(vs, jsonNull) {
		null = true
	} else {
		f, err := strconv.ParseFloat(string(vs), 64)
		if err != nil {
			return 0, 0, false, err
		}
		v = f
	}
	ts, err := parseTimestamp(bytes.TrimSpace(inner[comma+1:]))
	return v, ts, null, err
}

// the most digits an integer has that a float64 holds exactly
const exactDigits = 15

// parseTimestamp reads a timestamp as a float, truncated; an integer of few enough digits that a float
// holds it exactly is read directly, to the same value
func parseTimestamp(b []byte) (int64, error) {
	digits := b
	if len(digits) > 0 && digits[0] == '-' {
		digits = digits[1:]
	}
	if n := len(digits); n > 0 && n <= exactDigits {
		var ts int64
		for _, c := range digits {
			if c < '0' || c > '9' {
				ts = -1
				break
			}
			ts = ts*10 + int64(c-'0')
		}
		if ts >= 0 {
			if len(digits) < len(b) {
				ts = -ts
			}
			return ts, nil
		}
	}
	f, err := strconv.ParseFloat(string(b), 64)
	return int64(f), err
}

// rawLine reads a format=raw line, <target>,<start>,<end>,<step>|v,v,None; a failed step check fails
// it at once, as raw's lines were always read in turn
func (d *decoder) rawLine(line []byte) error {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	head, data, ok := bytes.Cut(line, []byte{'|'})
	if !ok {
		return timeseries.ErrInvalidBody
	}
	var nums [3]int64
	for i := 2; i >= 0; i-- {
		j := bytes.LastIndexByte(head, ',')
		if j < 0 {
			return timeseries.ErrInvalidBody
		}
		n, err := strconv.ParseInt(string(head[j+1:]), 10, 64)
		if err != nil {
			return timeseries.ErrInvalidBody
		}
		nums[i] = n
		head = head[:j]
	}
	start, step := nums[0], nums[2]
	if step <= 0 {
		return timeseries.ErrInvalidBody
	}
	d.times, d.values, d.nulls = d.times[:0], d.values[:0], d.nulls[:0]
	if len(data) > 0 {
		d.fields = stream.SplitFields(data, ',', d.fields[:0])
		for i, v := range d.fields {
			var f float64
			null := string(v) == rawNone
			if !null {
				n, err := strconv.ParseFloat(string(v), 64)
				if err != nil {
					return timeseries.ErrInvalidBody
				}
				f = n
			}
			d.times = append(d.times, start+int64(i)*step)
			d.values, d.nulls = append(d.values, f), append(d.nulls, null)
		}
	}
	name := string(head)
	err := d.addSeries(name, map[string]string{tagName: name}, false)
	d.adoptStep()
	if d.failed != nil {
		d.note()
		return d.failed
	}
	return err
}

// addSeries checks the series the decoder holds against the step and adds it; a JSON series is also
// checked for its spacing, and for enough datapoints to show its step
func (d *decoder) addSeries(name string, tags map[string]string, isJSON bool) error {
	n := len(d.times)
	if isJSON && n > 1 {
		stepSecs := d.times[1] - d.times[0]
		for i := 1; i < n; i++ {
			if stepSecs <= 0 || d.times[i] != d.times[0]+int64(i)*stepSecs {
				d.fail(timeseries.ErrInvalidBody, func() {
					if d.predicted > 0 {
						d.noteAmbiguous(name, d.predicted)
					}
				})
				return nil
			}
		}
	}
	step := d.step
	if n >= 2 {
		observed := time.Duration(epoch.FromSecs(d.times[1])-epoch.FromSecs(d.times[0])) * time.Nanosecond
		switch {
		case observed <= 0:
			d.fail(timeseries.ErrInvalidBody, func() {})
			return nil
		case d.step == 0:
			d.step = observed
		case observed != d.step:
			predicted := d.step
			d.fail(&StepMismatchError{Predicted: predicted, Observed: observed, Target: name}, func() {
				if n, ok := d.trq.ParsedQuery.(StepMismatchNoter); ok && n != nil {
					n.NoteStepMismatch(name, predicted, observed)
				}
			})
			return nil
		}
		step = observed
	}
	if isJSON && n < 2 && d.predicted > 0 {
		current := d.step
		d.fail(&StepAmbiguousError{Target: name, Points: n}, func() { d.noteAmbiguous(name, current) })
		return nil
	}
	if tags == nil {
		tags = map[string]string{tagName: name}
	}
	d.b.StartNewSeries(dataset.SeriesHeader{
		Name:           name,
		Tags:           dataset.Tags(tags),
		QueryStatement: d.trq.Statement,
		TimestampField: StepField(step),
		ValueFieldsList: timeseries.FieldDefinitions{{
			Name: ValueFieldName, DataType: timeseries.Float64, Role: timeseries.RoleValue, OutputPosition: 1,
		}},
	})
	d.series++
	for i, ts := range d.times {
		rb := d.b.Row()
		rb.SetEpoch(epoch.FromSecs(ts))
		if d.nulls[i] {
			rb.AddNull()
		} else {
			rb.AddFloat64(d.values[i])
		}
		if err := rb.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// fail records the first failed step check and the noter call it makes
func (d *decoder) fail(err error, note func()) {
	if d.failed == nil {
		d.failed, d.note = err, note
	}
}

func (d *decoder) noteAmbiguous(name string, step time.Duration) {
	if n, ok := d.trq.ParsedQuery.(StepAmbiguityNoter); ok && n != nil {
		n.NoteAmbiguousStep(name, step)
	}
}

// finishJSON applies the step checks of a body that decoded whole, a response of no series failing
// a predicted step it can't confirm
func (d *decoder) finishJSON() (timeseries.Timeseries, error) {
	if d.failed == nil && d.series == 0 && d.predicted > 0 {
		current := d.step
		d.fail(&StepAmbiguousError{}, func() { d.noteAmbiguous("", current) })
	}
	d.adoptStep()
	if d.failed != nil {
		d.note()
		return nil, d.failed
	}
	return d.finish()
}

// adoptStep gives the query the step its series agreed on, writing it only when it had none, as the
// query is shared by the concurrent decodes of its extents
func (d *decoder) adoptStep() {
	if d.trq.Step != d.step {
		d.trq.Step = d.step
	}
}

func (d *decoder) finish() (timeseries.Timeseries, error) {
	return d.b.Finish()
}

// sniffer gives the body, past its leading white space, to the decoder of the format its first other
// byte tells; a body of white space alone is an empty response
type sniffer struct {
	d    *decoder
	dec  stream.Decoder
	part []byte
}

func (s *sniffer) Write(p []byte) (int, error) {
	if s.dec != nil {
		return s.dec.Write(p)
	}
	buf := p
	if len(s.part) > 0 {
		s.part = append(s.part, p...)
		buf = s.part
	}
	for i := 0; i < len(buf); {
		if c := buf[i]; c < utf8.RuneSelf {
			if !asciiSpace(c) {
				return len(p), s.start(buf[i:])
			}
			i++
			continue
		}
		// a multi-byte space may arrive in pieces
		if !utf8.FullRune(buf[i:]) {
			s.part = append(s.part[:0], buf[i:]...)
			return len(p), nil
		}
		r, size := utf8.DecodeRune(buf[i:])
		if !unicode.IsSpace(r) {
			return len(p), s.start(buf[i:])
		}
		i += size
	}
	s.part = s.part[:0]
	return len(p), nil
}

func (s *sniffer) start(rest []byte) error {
	s.dec = s.d.pick(rest[0])
	_, err := s.dec.Write(rest)
	return err
}

func (s *sniffer) ReadFrom(r io.Reader) (int64, error) {
	var buf [sniffBytes]byte
	var total int64
	for s.dec == nil {
		n, err := r.Read(buf[:])
		total += int64(n)
		if n > 0 {
			if _, werr := s.Write(buf[:n]); werr != nil {
				return total, werr
			}
		}
		if errors.Is(err, io.EOF) {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
	n, err := s.dec.ReadFrom(r)
	return total + n, err
}

func (s *sniffer) Finish() (timeseries.Timeseries, error) {
	if s.dec == nil {
		// any bytes held are an unfinished character, which isn't white space
		if len(s.part) > 0 {
			s.dec = s.d.pick(s.part[0])
			if _, err := s.dec.Write(s.part); err != nil {
				return nil, err
			}
			return s.dec.Finish()
		}
		return s.d.empty()
	}
	return s.dec.Finish()
}

// the most bytes the sniffer reads before it knows the body's format
const sniffBytes = 512

func asciiSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
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
