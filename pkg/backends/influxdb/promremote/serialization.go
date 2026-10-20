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

package promremote

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/promremote/prompb"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/remoteread"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/klauspost/compress/snappy"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

var (
	errDuplicateLabel     = errors.New("prometheus remote-read series contains duplicate labels")
	errInvalidDataSet     = errors.New("invalid Prometheus remote-read dataset")
	errInvalidLabel       = errors.New("prometheus remote-read label is not valid UTF-8")
	errInvalidResponse    = errors.New("invalid Prometheus remote-read response")
	errInvalidSample      = errors.New("invalid Prometheus remote-read sample")
	errInvalidTimestamp   = errors.New("prometheus remote-read timestamp exceeds nanosecond range")
	errUnexpectedResults  = errors.New("prometheus remote-read response must contain exactly one result")
	errUnexpectedTimeStep = errors.New("prometheus remote-read sample timestamp is not millisecond-aligned")
)

const (
	nanosPerMillisecond = int64(time.Millisecond)
	// seriesBytes, labelBytes and sampleBytes are the memory a decoded series takes besides its labels' strings, each
	// label takes besides them, as a tag and its field, and each sample takes in its series' columns
	seriesBytes = int64(unsafe.Sizeof(dataset.Series{})) + int64(unsafe.Sizeof((*dataset.Series)(nil)))
	labelBytes  = int64(unsafe.Sizeof(timeseries.FieldDefinition{})) + mapEntryBytes
	sampleBytes = int64(unsafe.Sizeof(epoch.Epoch(0))+unsafe.Sizeof(float64(0))) + 1
	// mapEntryBytes is what an entry of a series' tags may take, its table as little as 7/16 full once grown
	mapEntryBytes = 48
)

// UnmarshalTimeseries converts a snappy-compressed remote-read response into a DataSet.
func UnmarshalTimeseries(data []byte,
	trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	return UnmarshalTimeseriesReader(bytes.NewReader(data), trq)
}

// UnmarshalTimeseriesReader converts a snappy-compressed remote-read response into a DataSet.
func UnmarshalTimeseriesReader(reader io.Reader,
	trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	if reader == nil || trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	limit := responseDecodeLimit(trq.ParsedQuery)
	decoded, err := readSnappyBlock(reader, limit)
	if err != nil {
		return nil, fmt.Errorf("decode Prometheus remote-read response: %w", err)
	}
	d := &responseDecoder{
		Kind:      errInvalidResponse,
		b:         dataset.NewBuilder(trq, dataset.BuilderOptions{}),
		statement: trq.Statement,
		allowance: remoteread.DecodeAllowance(int64(limit)),
	}
	d.b.SetResult(0, "")
	results, err := d.Series(decoded, d.series)
	if err == nil && results != 1 {
		err = errUnexpectedResults
	}
	if err != nil {
		return nil, fmt.Errorf("unmarshal Prometheus remote-read response: %w", err)
	}
	return d.b.Finish()
}

type responseDecoder struct {
	remoteread.Reader
	b         *dataset.Builder
	statement string
	allowance int64
}

func (d *responseDecoder) series(result int, msg []byte) error {
	if result > 0 {
		return errUnexpectedResults
	}
	// a first pass reads the labels and checks the samples, so the series' memory is charged before any is allocated
	var labels, samples int
	cost := seriesBytes
	for rest := msg; len(rest) > 0; {
		f, next, err := d.Field(rest)
		if err != nil {
			return err
		}
		rest = next
		switch f.Num {
		case remoteread.TimeSeriesLabels:
			if err = d.Expect(f, protowire.BytesType); err != nil {
				return err
			}
			name, value, err := d.Label(f.B)
			if err != nil {
				return err
			}
			if !utf8.Valid(name) || !utf8.Valid(value) {
				return errInvalidLabel
			}
			labels++
			cost += labelBytes + int64(len(name)+len(value))
		case remoteread.TimeSeriesSamples:
			if err = d.Expect(f, protowire.BytesType); err != nil {
				return err
			}
			t, _, err := d.Sample(f.B)
			if err != nil {
				return err
			}
			if t > math.MaxInt64/nanosPerMillisecond || t < math.MinInt64/nanosPerMillisecond {
				return errInvalidTimestamp
			}
			samples++
		}
	}
	if d.allowance -= cost + int64(samples)*sampleBytes; d.allowance < 0 {
		return fmt.Errorf("%w: the series decoded from a response would take too much memory", errBodyTooLarge)
	}
	// the first pass read every field, so none fails to read here
	tags := make(dataset.Tags, labels)
	for rest := msg; len(rest) > 0; {
		f, next, _ := d.Field(rest)
		rest = next
		if f.Num != remoteread.TimeSeriesLabels {
			continue
		}
		name, value, _ := d.Label(f.B)
		if _, ok := tags[string(name)]; ok {
			return errDuplicateLabel
		}
		tags[string(name)] = string(value)
	}
	d.b.StartNewSeries(seriesHeader(tags, d.statement))
	d.b.Grow(samples, samples, 0)
	for rest := msg; len(rest) > 0; {
		f, next, _ := d.Field(rest)
		rest = next
		if f.Num != remoteread.TimeSeriesSamples {
			continue
		}
		t, v, _ := d.Sample(f.B)
		row := d.b.Row()
		row.SetEpoch(epoch.Epoch(t * nanosPerMillisecond))
		row.AddFloat64(v)
		if err := row.Commit(); err != nil {
			return err
		}
	}
	d.b.EndSeries()
	return nil
}

func seriesHeader(tags dataset.Tags, statement string) dataset.SeriesHeader {
	tagNames := tags.Keys()
	tagFields := make(timeseries.FieldDefinitions, len(tagNames))
	for i, name := range tagNames {
		tagFields[i] = timeseries.FieldDefinition{
			Name:     name,
			DataType: timeseries.String,
			Role:     timeseries.RoleTag,
		}
	}
	return dataset.SeriesHeader{
		Name:            tags["__name__"],
		Tags:            tags,
		TagFieldsList:   tagFields,
		QueryStatement:  statement,
		TimestampField:  timeseries.FieldDefinition{Name: "timestamp", DataType: timeseries.DateTimeUnixMilli, Role: timeseries.RoleTimestamp},
		ValueFieldsList: timeseries.FieldDefinitions{{Name: "value", DataType: timeseries.Float64, Role: timeseries.RoleValue}},
	}
}

// MarshalTimeseries converts a DataSet into a snappy-compressed remote-read response.
func MarshalTimeseries(ts timeseries.Timeseries,
	_ *timeseries.RequestOptions, _ int,
) ([]byte, error) {
	response, err := toWireResponse(ts)
	if err != nil {
		return nil, err
	}
	encoded, err := proto.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("marshal Prometheus remote-read response: %w", err)
	}
	return snappy.Encode(nil, encoded), nil
}

// MarshalTimeseriesWriter writes a snappy-compressed remote-read response.
func MarshalTimeseriesWriter(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, status int, writer io.Writer,
) error {
	if writer == nil {
		return errInvalidDataSet
	}
	body, err := MarshalTimeseries(ts, rlo, status)
	if err != nil {
		return err
	}
	if responseWriter, ok := writer.(http.ResponseWriter); ok {
		responseWriter.Header().Set(headers.NameContentType, ContentType)
		responseWriter.Header().Set(headers.NameContentEncoding, ContentEncoding)
	}
	_, err = writer.Write(body)
	return err
}

func toWireResponse(ts timeseries.Timeseries) (*prompb.ReadResponse, error) {
	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil || len(ds.Results) != 1 || ds.Results[0] == nil {
		return nil, errInvalidDataSet
	}
	queryResult := &prompb.QueryResult{}
	if ds.Results[0].SeriesList != nil {
		queryResult.Timeseries = make([]*prompb.TimeSeries, 0, len(ds.Results[0].SeriesList))
	}
	for _, series := range ds.Results[0].SeriesList {
		if series == nil {
			continue
		}
		wireSeries, err := toWireSeries(series)
		if err != nil {
			return nil, err
		}
		queryResult.Timeseries = append(queryResult.Timeseries, wireSeries)
	}
	return &prompb.ReadResponse{Results: []*prompb.QueryResult{queryResult}}, nil
}

func toWireSeries(series *dataset.Series) (*prompb.TimeSeries, error) {
	labels := make([]*prompb.Label, 0, len(series.Header.Tags))
	for _, name := range series.Header.Tags.Keys() {
		labels = append(labels, &prompb.Label{Name: name, Value: series.Header.Tags[name]})
	}
	samples := make([]*prompb.Sample, 0, series.PointCount())
	segs := series.Segments()
	for k := range segs {
		seg := &segs[k]
		for i, e := range seg.Epochs() {
			if seg.NumCols() != 1 || seg.KindAt(0, i) != dataset.KindFloat64 {
				return nil, errInvalidSample
			}
			if int64(e)%nanosPerMillisecond != 0 {
				return nil, errUnexpectedTimeStep
			}
			samples = append(samples, &prompb.Sample{
				Timestamp: int64(e) / nanosPerMillisecond,
				Value:     seg.Float64(0, i),
			})
		}
	}
	return &prompb.TimeSeries{Labels: labels, Samples: samples}, nil
}
