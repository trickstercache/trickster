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
	"fmt"
	"io"
	"strconv"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// NewModeler returns a collection of modeling functions for prometheus interoperability
func NewModeler() *timeseries.Modeler {
	return &timeseries.Modeler{
		WireUnmarshalerReader: UnmarshalTimeseriesReader,
		WireMarshaler:         MarshalTimeseries,
		WireMarshalWriter:     MarshalTimeseriesWriter,
		WireUnmarshaler:       UnmarshalTimeseries,
		CacheMarshaler:        dataset.MarshalDataSet,
		CacheUnmarshaler:      dataset.UnmarshalDataSet,
		WireMarshalReadsParts: true,
	}
}

const fieldNameHistogram = "histogram"

// MarshalTimeseries converts a Timeseries into a JSON blob
func MarshalTimeseries(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	err := MarshalTimeseriesWriter(ts, rlo, status, buf)
	return buf.Bytes(), err
}

// MarshalTimeseriesWriter converts a Timeseries into a JSON blob via an io.Writer
func MarshalTimeseriesWriter(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int, w io.Writer) error {
	return MarshalTSOrVectorWriter(ts, rlo, status, w, false)
}

// seriesGroup collects value and histogram series that share the same metric tags.
type seriesGroup struct {
	tagsJSON string
	valueSer *dataset.Series
	histSer  *dataset.Series
}

// MarshalTSOrVectorWriter writes matrix and vector outputs to the provided io.Writer
func MarshalTSOrVectorWriter(ts timeseries.Timeseries, _ *timeseries.RequestOptions,
	status int, w io.Writer, isVector bool,
) error {
	if w == nil {
		return errors.ErrNilWriter
	}

	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		return timeseries.ErrUnknownFormat
	}
	// With Prometheus we presume only one Result per DataSet
	if len(ds.Results) != 1 {
		return timeseries.ErrUnknownFormat
	}

	if ds.Status == "" {
		ds.Status = statusSuccess
	}
	if isVector && ds.SourceResultType == string(Scalar) {
		return marshalScalarWriter(ds, status, w)
	}

	e := Envelope{ds.Status, ds.Error, ds.ErrorType, ds.Warnings}
	startResponse(w, status)
	jw := tbytes.NewChunkWriter(w)
	jw.Buf = e.appendStart(jw.Buf)

	resultType := Matrix
	if isVector {
		resultType = Vector
	}
	jw.Buf = append(jw.Buf, `,"data":{"resultType":"`...)
	jw.Buf = append(jw.Buf, resultType...)
	jw.Buf = append(jw.Buf, `","result":[`...)

	// series are grouped by tags, so a result object can hold both values and histograms for a mixed
	// metric; the capacity past the output is scratch space for the grouping
	groups := groupSeriesByTags(ds.Results[0].SeriesList, jw.Buf[len(jw.Buf):])

	var seriesSep bool
	for _, g := range groups {
		if (g.valueSer == nil || g.valueSer.PointCount() == 0) &&
			(g.histSer == nil || g.histSer.PointCount() == 0) {
			continue
		}
		if jw.Err() != nil {
			// the client is gone, and the rest would be written nowhere
			break
		}
		if seriesSep {
			jw.Buf = append(jw.Buf, `,{"metric":`...)
		} else {
			jw.Buf = append(jw.Buf, `{"metric":`...)
			seriesSep = true
		}
		jw.Buf = append(jw.Buf, g.tagsJSON...)

		if isVector {
			appendVectorGroup(&jw, g)
		} else {
			appendMatrixGroup(&jw, g)
		}
		jw.Buf = append(jw.Buf, '}')
	}
	jw.Buf = append(jw.Buf, "]}}"...)
	return jw.Close()
}

func marshalScalarWriter(ds *dataset.DataSet, status int, w io.Writer) error {
	e := Envelope{ds.Status, ds.Error, ds.ErrorType, ds.Warnings}
	startResponse(w, status)
	jw := tbytes.NewChunkWriter(w)
	jw.Buf = e.appendStart(jw.Buf)
	jw.Buf = append(jw.Buf, `,"data":{"resultType":"scalar","result":`...)
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			seg, ok := firstRow(series)
			if !ok || seg.NumCols() == 0 {
				continue
			}
			jw.Buf = append(jw.Buf, '[')
			jw.Buf = appendEpochSeconds(jw.Buf, seg.Epoch(0))
			jw.Buf = append(jw.Buf, ',')
			var value string
			if seg.KindAt(0, 0) == dataset.KindString {
				value = seg.Text(0, 0)
			}
			jw.Buf = strconv.AppendQuote(jw.Buf, value)
			jw.Buf = append(jw.Buf, `]}}`...)
			return jw.Close()
		}
	}
	jw.Buf = append(jw.Buf, `[]}}`...)
	return jw.Close()
}

// each series' tags are rendered into scratch to find its group, and copied out only for a
// new group
func groupSeriesByTags(seriesList []*dataset.Series, scratch []byte) []seriesGroup {
	idx := make(map[string]int, len(seriesList))
	groups := make([]seriesGroup, 0, len(seriesList))
	for _, s := range seriesList {
		if s == nil || s.PointCount() == 0 {
			continue
		}
		scratch = s.Header.Tags.AppendJSON(scratch[:0])
		gi, exists := idx[string(scratch)]
		if !exists {
			gi = len(groups)
			tj := string(scratch)
			idx[tj] = gi
			groups = append(groups, seriesGroup{tagsJSON: tj})
		}
		isHist := len(s.Header.ValueFieldsList) > 0 &&
			s.Header.ValueFieldsList[0].Name == fieldNameHistogram
		if isHist {
			groups[gi].histSer = s
		} else {
			groups[gi].valueSer = s
		}
	}
	return groups
}

// appends e as the seconds Prometheus writes, as strconv.FormatFloat(e/1e9, 'f', -1) renders them;
// a whole second, as a step-aligned point always is, needs no float formatting
func appendEpochSeconds(dst []byte, e epoch.Epoch) []byte {
	if e%1e9 == 0 {
		// e/1e9 is exact in float64 here, as e is a whole number of seconds times 2^9 * 5^9
		return strconv.AppendInt(dst, int64(e/1e9), 10)
	}
	return strconv.AppendFloat(dst, float64(e)/1e9, 'f', -1, 64)
}

// firstRow returns the Segment holding a series' first row, and false for a nil or empty series
func firstRow(s *dataset.Series) (*dataset.Segment, bool) {
	if s == nil {
		return nil, false
	}
	segs := s.Segments()
	for i := range segs {
		if segs[i].Len() > 0 {
			return &segs[i], true
		}
	}
	return nil, false
}

// appendSampleText appends a sample's text; a value of another kind, which a Prometheus decode never
// produces, is written as fmt formats it
func appendSampleText(dst []byte, seg *dataset.Segment, i int) []byte {
	if seg.NumCols() == 0 {
		return dst
	}
	if seg.KindAt(0, i).IsBytes() {
		return append(dst, seg.Bytes(0, i)...)
	}
	return fmt.Append(dst, seg.Value(0, i))
}

func appendVectorGroup(jw *tbytes.ChunkWriter, g seriesGroup) {
	if seg, ok := firstRow(g.histSer); ok {
		jw.Buf = append(jw.Buf, `,"histogram":[`...)
		jw.Buf = appendEpochSeconds(jw.Buf, seg.Epoch(0))
		jw.Buf = append(jw.Buf, ',')
		jw.Buf = appendSampleText(jw.Buf, seg, 0)
		jw.Buf = append(jw.Buf, ']')
	} else if seg, ok := firstRow(g.valueSer); ok {
		jw.Buf = append(jw.Buf, `,"value":[`...)
		jw.Buf = appendEpochSeconds(jw.Buf, seg.Epoch(0))
		jw.Buf = append(jw.Buf, `,"`...)
		jw.Buf = appendSampleText(jw.Buf, seg, 0)
		jw.Buf = append(jw.Buf, `"]`...)
	}
	jw.FlushIfFull()
}

func appendMatrixGroup(jw *tbytes.ChunkWriter, g seriesGroup) {
	if g.valueSer != nil && g.valueSer.PointCount() > 0 {
		appendPointsArray(jw, g.valueSer, `,"values":[`, `,"`, `"]`)
	}
	if g.histSer != nil && g.histSer.PointCount() > 0 {
		appendPointsArray(jw, g.histSer, `,"histograms":[`, `,`, `]`)
	}
}

func appendPointsArray(jw *tbytes.ChunkWriter, s *dataset.Series, header, valPrefix, valSuffix string) {
	// the rows, read across the series' Segments, may be a cached dataset's, which a marshal only
	// reads, so any sort is of a copy
	segs := s.Segments().Sorted()
	jw.Buf = append(jw.Buf, header...)
	first := true
	for k := range segs {
		seg := &segs[k]
		for i, e := range seg.Epochs() {
			if !first {
				jw.Buf = append(jw.Buf, ',')
			}
			first = false
			jw.Buf = append(jw.Buf, '[')
			jw.Buf = appendEpochSeconds(jw.Buf, e)
			jw.Buf = append(jw.Buf, valPrefix...)
			jw.Buf = appendSampleText(jw.Buf, seg, i)
			jw.Buf = append(jw.Buf, valSuffix...)
			jw.FlushIfFull()
		}
	}
	jw.Buf = append(jw.Buf, ']')
}
