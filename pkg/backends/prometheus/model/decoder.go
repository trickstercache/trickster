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
	"encoding/json/jsontext"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// UnmarshalTimeseries converts a JSON blob into a Timeseries
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	return stream.BytesUnmarshaler(newDecoder)(data, trq)
}

// UnmarshalTimeseriesReader converts a JSON blob into a Timeseries via io.Reader
func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	if reader == nil {
		return nil, io.ErrUnexpectedEOF
	}
	return stream.ReaderUnmarshaler(newDecoder)(reader, trq)
}

// keys are matched as encoding/json matches struct fields, ignoring case
const (
	keyStatus     = "status"
	keyError      = "error"
	keyErrorType  = "errorType"
	keyWarnings   = "warnings"
	keyData       = "data"
	keyResultType = "resultType"
	keyResult     = "result"
	keyMetric     = "metric"
	keyValues     = "values"
	keyValue      = "value"
	keyHistograms = "histograms"
	keyHistogram  = "histogram"
	labelName     = "__name__"
)

// label names, and values no longer than maxInternedLen, are shared by the series of a response that
// repeat them, up to maxInterned of them
const (
	maxInterned    = 4096
	maxInternedLen = 64
)

// fieldName returns the one of names key matches as encoding/json matches a field, preferring an
// exact match to one that ignores case, or "" for none
func fieldName(key []byte, names ...string) string {
	for _, n := range names {
		if string(key) == n {
			return n
		}
	}
	for _, n := range names {
		if strings.EqualFold(string(key), n) {
			return n
		}
	}
	return ""
}

// what a result held, as its first element shows, when it arrived before its resultType
type resultShape uint8

const (
	shapeNone resultShape = iota
	shapeSeries
	shapeScalar
	shapeOther
)

var (
	fdValue = timeseries.FieldDefinition{Name: "value", DataType: timeseries.String}
	fdHist  = timeseries.FieldDefinition{Name: fieldNameHistogram, DataType: timeseries.String}
)

// decoder builds a DataSet from a Prometheus query response as it is read. Each [time, value] pair
// is read raw and copied into the Builder, so nothing refers to the input once a sample is read.
type decoder struct {
	trq        *timeseries.TimeRangeQuery
	b          *dataset.Builder
	env        Envelope
	resultType ResultType
	typeSeen   bool
	dataSeen   bool
	resultSeen bool
	shape      resultShape
	// whether elements used a matrix's or a vector's fields, checked against a late resultType
	sawMatrix, sawVector bool
	// a scalar's pair, copied, and its element count
	scalar    []byte
	scalarLen int
	// the time of the last vector or scalar point, which becomes the extent
	extent    epoch.Epoch
	hasExtent bool
	el        element
	scratch   []byte
	interned  map[string]string
	marshaler marshaler
	// the series' value fields, shared by every series of their kind in the response
	valueFields, histFields []timeseries.FieldDefinition
}

// element is the state of one series object in a matrix or vector result
type element struct {
	tags   dataset.Tags
	name   string
	metric bool
	// raw element counts of the values and histograms, which decide the series emitted
	valueRows, histRows     int
	valueSeries, histSeries bool
	pendValues, pendHists   pending
	vecValue, vecHist       []byte
	vecValueLen, vecHistLen int
	hasVecValue, hasVecHist bool
	seenBuf                 [5]string
}

// pending holds an element's samples that arrive before its metric names their series
type pending struct {
	epochs []epoch.Epoch
	ends   []int
	text   []byte
}

func (p *pending) add(e epoch.Epoch, text []byte) {
	p.epochs = append(p.epochs, e)
	p.text = append(p.text, text...)
	p.ends = append(p.ends, len(p.text))
}

func (p *pending) reset() {
	p.epochs, p.ends, p.text = p.epochs[:0], p.ends[:0], p.text[:0]
}

func newDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	d := &decoder{trq: trq, b: dataset.NewBuilder(trq, dataset.BuilderOptions{})}
	return stream.NewJSON(d.walk, d.finish), nil
}

func (d *decoder) walk(dec *jsontext.Decoder) error {
	// a null document decodes as an empty one
	if dec.PeekKind() == jsontext.KindNull {
		_, err := dec.ReadToken()
		return err
	}
	return stream.ObjectBytes(dec, func(key []byte) error {
		switch fieldName(key, keyStatus, keyError, keyErrorType, keyWarnings, keyData) {
		case keyStatus:
			return stream.Decode(dec, &d.env.Status)
		case keyError:
			return stream.Decode(dec, &d.env.Error)
		case keyErrorType:
			return stream.Decode(dec, &d.env.ErrorType)
		case keyWarnings:
			return stream.Decode(dec, &d.env.Warnings)
		case keyData:
			if d.dataSeen {
				return timeseries.ErrInvalidBody
			}
			d.dataSeen = true
			if dec.PeekKind() == jsontext.KindNull {
				_, err := dec.ReadToken()
				return err
			}
			return stream.ObjectBytes(dec, func(key []byte) error {
				switch fieldName(key, keyResultType, keyResult) {
				case keyResultType:
					if d.typeSeen {
						return timeseries.ErrInvalidBody
					}
					d.typeSeen = true
					return stream.Decode(dec, &d.resultType)
				case keyResult:
					return d.readResult(dec)
				}
				return stream.Skip(dec)
			})
		}
		return stream.Skip(dec)
	})
}

func (d *decoder) readResult(dec *jsontext.Decoder) error {
	if d.resultSeen {
		return timeseries.ErrInvalidBody
	}
	d.resultSeen = true
	switch {
	case d.resultType == Scalar:
		return d.readScalar(dec)
	case d.typeSeen && d.resultType != Matrix && d.resultType != Vector:
		// a result of another type is never decoded
		return stream.Skip(dec)
	}
	switch dec.PeekKind() {
	case jsontext.KindNull:
		_, err := dec.ReadToken()
		return err
	case jsontext.KindBeginArray:
	default:
		if d.typeSeen {
			return timeseries.ErrInvalidBody
		}
		d.shape = shapeOther
		return stream.Skip(dec)
	}
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	switch dec.PeekKind() {
	case jsontext.KindBeginObject:
		d.shape = shapeSeries
	case jsontext.KindEndArray:
		_, err := dec.ReadToken()
		return err
	case jsontext.KindNumber:
		if !d.typeSeen {
			return d.readScalarElements(dec)
		}
		return timeseries.ErrInvalidBody
	default:
		if d.typeSeen {
			return timeseries.ErrInvalidBody
		}
		d.shape = shapeOther
		for dec.PeekKind() != jsontext.KindEndArray {
			if err := stream.Skip(dec); err != nil {
				return err
			}
		}
		_, err := dec.ReadToken()
		return err
	}
	for {
		switch dec.PeekKind() {
		case jsontext.KindEndArray:
			_, err := dec.ReadToken()
			return err
		case jsontext.KindBeginObject:
			if err := d.readElement(dec); err != nil {
				return err
			}
		default:
			if _, err := dec.ReadToken(); err != nil {
				return err
			}
			return timeseries.ErrInvalidBody
		}
	}
}

// readScalar reads a scalar result, a [time, "value"] pair that may be null or of any length
func (d *decoder) readScalar(dec *jsontext.Decoder) error {
	switch dec.PeekKind() {
	case jsontext.KindNull:
		_, err := dec.ReadToken()
		return err
	case jsontext.KindBeginArray:
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		return d.readScalarElements(dec)
	}
	return timeseries.ErrInvalidBody
}

// readScalarElements reads a scalar's elements after its opening bracket, copying the first two
func (d *decoder) readScalarElements(dec *jsontext.Decoder) error {
	d.shape = shapeScalar
	d.scalar = append(d.scalar[:0], '[')
	for dec.PeekKind() != jsontext.KindEndArray {
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		if d.scalarLen++; d.scalarLen <= 2 {
			if d.scalarLen == 2 {
				d.scalar = append(d.scalar, ',')
			}
			d.scalar = append(d.scalar, raw...)
		}
	}
	d.scalar = append(d.scalar, ']')
	_, err := dec.ReadToken()
	return err
}

func (d *decoder) readElement(dec *jsontext.Decoder) error {
	el := &d.el
	el.tags, el.name, el.metric = nil, "", false
	el.valueRows, el.histRows, el.valueSeries, el.histSeries = 0, 0, false, false
	el.pendValues.reset()
	el.pendHists.reset()
	el.hasVecValue, el.hasVecHist, el.vecValueLen, el.vecHistLen = false, false, 0, 0
	seen := el.seenBuf[:0]
	err := stream.ObjectBytes(dec, func(k []byte) error {
		key := fieldName(k, keyMetric, keyValues, keyHistograms, keyValue, keyHistogram)
		switch key {
		case keyMetric:
		case keyValues, keyHistograms:
			// each result type's fields are read, and the other's ignored
			if d.resultType == Vector {
				return stream.Skip(dec)
			}
		case keyValue, keyHistogram:
			if d.resultType == Matrix {
				return stream.Skip(dec)
			}
		default:
			return stream.Skip(dec)
		}
		if slices.Contains(seen, key) {
			// a repeated field would add its samples twice
			return timeseries.ErrInvalidBody
		}
		seen = append(seen, key)
		switch key {
		case keyMetric:
			return d.readMetric(dec)
		case keyValues, keyHistograms:
			d.sawMatrix = true
			return d.readSamples(dec, key == keyHistograms)
		}
		d.sawVector = true
		return d.readVectorSample(dec, key == keyHistogram)
	})
	if err != nil {
		return err
	}
	return d.endElement()
}

func (d *decoder) readMetric(dec *jsontext.Decoder) error {
	el := &d.el
	if dec.PeekKind() == jsontext.KindNull {
		_, err := dec.ReadToken()
		el.metric = true
		if err != nil {
			return err
		}
		return d.flushPending()
	}
	tags := dataset.Tags{}
	err := stream.ObjectBytes(dec, func(key []byte) error {
		name := d.intern(key, true)
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		switch raw.Kind() {
		case jsontext.KindString:
			d.scratch = stream.AppendString(d.scratch[:0], raw)
			tags[name] = d.intern(d.scratch, len(d.scratch) <= maxInternedLen)
		case jsontext.KindNull:
			tags[name] = ""
		default:
			return timeseries.ErrInvalidBody
		}
		return nil
	})
	if err != nil {
		return err
	}
	el.tags, el.name, el.metric = tags, tags[labelName], true
	return d.flushPending()
}

// intern returns b as a string, shared with the response's earlier labels when share is set
func (d *decoder) intern(b []byte, share bool) string {
	if s, ok := d.interned[string(b)]; ok {
		return s
	}
	s := string(b)
	if share && len(d.interned) < maxInterned {
		if d.interned == nil {
			d.interned = make(map[string]string)
		}
		d.interned[s] = s
	}
	return s
}

// flushPending adds the samples read before the element's metric, values first
func (d *decoder) flushPending() error {
	el := &d.el
	if el.valueRows > 0 && !el.valueSeries {
		d.startSeries(false)
		el.valueSeries = true
		if err := d.flush(&el.pendValues); err != nil {
			return err
		}
	}
	if el.histRows > 0 && !el.histSeries {
		d.startSeries(true)
		el.histSeries = true
		return d.flush(&el.pendHists)
	}
	return nil
}

// readSamples reads a matrix element's values or histograms, adding each valid sample to its
// series as it is read, or holding it until the element's metric arrives
func (d *decoder) readSamples(dec *jsontext.Decoder, hist bool) error {
	if dec.PeekKind() == jsontext.KindNull {
		_, err := dec.ReadToken()
		return err
	}
	el := &d.el
	return stream.Array(dec, func() error {
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		switch raw.Kind() {
		case jsontext.KindBeginArray, jsontext.KindNull:
		default:
			return timeseries.ErrInvalidBody
		}
		rows, started := &el.valueRows, &el.valueSeries
		if hist {
			rows, started = &el.histRows, &el.histSeries
		}
		*rows++
		if el.metric && !*started {
			d.startSeries(hist)
			*started = true
		}
		e, text, ok, err := d.sample(raw, hist)
		if err != nil || !ok || e <= 0 {
			return err
		}
		if !el.metric {
			if hist {
				el.pendHists.add(e, text)
			} else {
				el.pendValues.add(e, text)
			}
			return nil
		}
		return d.addRow(e, text)
	})
}

// readVectorSample copies a vector element's value or histogram pair, which endElement adds
func (d *decoder) readVectorSample(dec *jsontext.Decoder, hist bool) error {
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	switch raw.Kind() {
	case jsontext.KindBeginArray, jsontext.KindNull:
	default:
		return timeseries.ErrInvalidBody
	}
	el := &d.el
	n := arrayLen(raw)
	if hist {
		el.vecHist, el.vecHistLen, el.hasVecHist = append(el.vecHist[:0], raw...), n, true
	} else {
		el.vecValue, el.vecValueLen, el.hasVecValue = append(el.vecValue[:0], raw...), n, true
	}
	return nil
}

// endElement emits the element's series as the Prometheus model always has: its values' series
// unless it holds only histograms, then its histograms' series
func (d *decoder) endElement() error {
	el := &d.el
	if el.hasVecValue || el.hasVecHist {
		return d.endVectorElement()
	}
	if !el.valueSeries && (el.valueRows > 0 || el.histRows == 0) {
		d.startSeries(false)
		el.valueSeries = true
		if err := d.flush(&el.pendValues); err != nil {
			return err
		}
	}
	if !el.histSeries && el.histRows > 0 {
		d.startSeries(true)
		el.histSeries = true
		if err := d.flush(&el.pendHists); err != nil {
			return err
		}
	}
	d.b.EndSeries()
	return nil
}

func (d *decoder) endVectorElement() error {
	el := &d.el
	hasHist := el.vecHistLen == 2
	if el.vecValueLen >= 1 || !hasHist {
		d.startSeries(false)
		if el.vecValueLen == 2 {
			if err := d.addVectorPoint(el.vecValue, false); err != nil {
				return err
			}
		}
	}
	if hasHist {
		d.startSeries(true)
		if err := d.addVectorPoint(el.vecHist, true); err != nil {
			return err
		}
	}
	d.b.EndSeries()
	return nil
}

// addVectorPoint adds a vector's or scalar's point, which also sets the extent; it isn't dropped
// for its time, as a matrix's samples are
func (d *decoder) addVectorPoint(raw []byte, hist bool) error {
	e, text, ok, err := d.sample(raw, hist)
	if err != nil || !ok {
		return err
	}
	d.extent, d.hasExtent = e, true
	return d.addRow(e, text)
}

func (d *decoder) startSeries(hist bool) {
	fields := &d.valueFields
	fd := fdValue
	if hist {
		fields, fd = &d.histFields, fdHist
	}
	if *fields == nil {
		// capped, so an append to one series' fields never reaches another's
		*fields = []timeseries.FieldDefinition{fd}[:1:1]
	}
	d.b.StartSeries(dataset.SeriesHeader{
		Name: d.el.name, Tags: d.el.tags, QueryStatement: d.trq.Statement, ValueFieldsList: *fields,
	})
}

func (d *decoder) flush(p *pending) error {
	start := 0
	for i, e := range p.epochs {
		if err := d.addRow(e, p.text[start:p.ends[i]]); err != nil {
			return err
		}
		start = p.ends[i]
	}
	return nil
}

func (d *decoder) addRow(e epoch.Epoch, text []byte) error {
	r := d.b.Row()
	r.SetEpoch(e)
	r.AddString(text)
	return r.Commit()
}

// sample returns the time and text of a [time, value] pair, or ok false to drop any other shape; a
// histogram's value is written as encoding/json writes it once decoded
func (d *decoder) sample(raw []byte, hist bool) (epoch.Epoch, []byte, bool, error) {
	ts, v, n := pairOf(raw)
	if n != 2 || len(ts) == 0 || !isNumberStart(ts[0]) {
		return 0, nil, false, nil
	}
	e, err := sampleTime(ts)
	if err != nil {
		return 0, nil, false, err
	}
	if hist {
		d.scratch, err = d.marshaler.append(d.scratch[:0], v)
		return e, d.scratch, err == nil, err
	}
	if v[0] != '"' {
		return 0, nil, false, nil
	}
	d.scratch = stream.AppendString(d.scratch[:0], v)
	return e, d.scratch, true, nil
}

// sampleTime parses a sample's time in seconds exactly, or, for a precision finer than a
// nanosecond, as the float the Prometheus model always read it as
func sampleTime(ts []byte) (epoch.Epoch, error) {
	if e, err := epoch.ParseDecimal(ts, timeseries.DateTimeUnixSecs); err == nil {
		return e, nil
	}
	f, err := strconv.ParseFloat(string(ts), 64)
	if err != nil {
		return 0, timeseries.ErrInvalidBody
	}
	return epoch.Epoch(f * 1e9), nil
}

func (d *decoder) finish() (timeseries.Timeseries, error) {
	switch d.resultType {
	case Matrix, Vector:
		if d.shape == shapeScalar || d.shape == shapeOther ||
			(d.resultType == Matrix && d.sawVector) || (d.resultType == Vector && d.sawMatrix) {
			return nil, timeseries.ErrInvalidBody
		}
	case Scalar:
		if d.shape == shapeSeries || d.shape == shapeOther {
			return nil, timeseries.ErrInvalidBody
		}
		// a scalar always has its one series, with its point when the pair is valid
		d.el = element{}
		d.startSeries(false)
		if d.scalarLen == 2 {
			if err := d.addVectorPoint(d.scalar, false); err != nil {
				return nil, err
			}
		}
		d.b.EndSeries()
	}
	ds, err := d.b.Finish()
	if err != nil {
		return nil, err
	}
	ds.SourceResultType = string(d.resultType)
	ds.Status, ds.Error, ds.ErrorType, ds.Warnings = d.env.Status, d.env.Error, d.env.ErrorType, d.env.Warnings
	ds.ValueOperations = prometheusValueOperations
	switch d.resultType {
	case Matrix:
	case Vector, Scalar:
		if d.hasExtent {
			t := time.Unix(0, int64(d.extent))
			ds.ExtentList = timeseries.ExtentList{{Start: t, End: t}}
		}
	default:
		// only matrices, vectors and scalars hold series
		ds.Results = nil
	}
	return ds, nil
}

func isNumberStart(c byte) bool {
	return c == '-' || (c >= '0' && c <= '9')
}
