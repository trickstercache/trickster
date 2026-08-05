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
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/promremote/prompb"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/proto"
)

var (
	errDuplicateLabel     = errors.New("prometheus remote-read series contains duplicate labels")
	errInvalidDataSet     = errors.New("invalid Prometheus remote-read dataset")
	errInvalidSample      = errors.New("invalid Prometheus remote-read sample")
	errInvalidTimestamp   = errors.New("prometheus remote-read timestamp exceeds nanosecond range")
	errUnexpectedResults  = errors.New("prometheus remote-read response must contain exactly one result")
	errUnexpectedTimeStep = errors.New("prometheus remote-read sample timestamp is not millisecond-aligned")
)

const nanosPerMillisecond = int64(time.Millisecond)

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
	decoded, err := readSnappyBlock(reader, responseDecodeLimit(trq.ParsedQuery))
	if err != nil {
		return nil, fmt.Errorf("decode Prometheus remote-read response: %w", err)
	}
	response := &prompb.ReadResponse{}
	if err := proto.Unmarshal(decoded, response); err != nil {
		return nil, fmt.Errorf("unmarshal Prometheus remote-read response: %w", err)
	}
	if len(response.Results) != 1 || response.Results[0] == nil {
		return nil, errUnexpectedResults
	}

	result := &dataset.Result{StatementID: 0}
	if response.Results[0].Timeseries != nil {
		result.SeriesList = make(dataset.SeriesList, len(response.Results[0].Timeseries))
	}
	for i, wireSeries := range response.Results[0].Timeseries {
		series, err := fromWireSeries(wireSeries, trq.Statement)
		if err != nil {
			return nil, err
		}
		result.SeriesList[i] = series
	}

	return &dataset.DataSet{
		Results:        dataset.Results{result},
		TimeRangeQuery: trq,
		ExtentList:     timeseries.ExtentList{trq.Extent},
	}, nil
}

func fromWireSeries(wireSeries *prompb.TimeSeries,
	statement string,
) (*dataset.Series, error) {
	if wireSeries == nil {
		return nil, errInvalidDataSet
	}
	tags := make(dataset.Tags, len(wireSeries.Labels))
	for _, label := range wireSeries.Labels {
		if label == nil {
			return nil, errInvalidDataSet
		}
		if _, ok := tags[label.Name]; ok {
			return nil, errDuplicateLabel
		}
		tags[label.Name] = label.Value
	}

	tagNames := tags.Keys()
	tagFields := make(timeseries.FieldDefinitions, len(tagNames))
	for i, name := range tagNames {
		tagFields[i] = timeseries.FieldDefinition{
			Name:     name,
			DataType: timeseries.String,
			Role:     timeseries.RoleTag,
		}
	}
	header := dataset.SeriesHeader{
		Name:            tags["__name__"],
		Tags:            tags,
		TagFieldsList:   tagFields,
		QueryStatement:  statement,
		TimestampField:  timeseries.FieldDefinition{Name: "timestamp", DataType: timeseries.DateTimeUnixMilli, Role: timeseries.RoleTimestamp},
		ValueFieldsList: timeseries.FieldDefinitions{{Name: "value", DataType: timeseries.Float64, Role: timeseries.RoleValue}},
	}
	header.CalculateSize()

	points := make(dataset.Points, len(wireSeries.Samples))
	var pointSize int64
	for i, sample := range wireSeries.Samples {
		if sample == nil {
			return nil, errInvalidSample
		}
		if sample.Timestamp > math.MaxInt64/nanosPerMillisecond ||
			sample.Timestamp < math.MinInt64/nanosPerMillisecond {
			return nil, errInvalidTimestamp
		}
		points[i] = dataset.Point{
			Epoch:  epoch.Epoch(sample.Timestamp * nanosPerMillisecond),
			Size:   20,
			Values: []any{sample.Value},
		}
		pointSize += int64(points[i].Size)
	}
	slices.SortFunc(points, func(a, b dataset.Point) int {
		return cmpEpoch(a.Epoch, b.Epoch)
	})

	return &dataset.Series{Header: header, Points: points, PointSize: pointSize}, nil
}

func cmpEpoch(a, b epoch.Epoch) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
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
	samples := make([]*prompb.Sample, len(series.Points))
	for i, point := range series.Points {
		if len(point.Values) != 1 {
			return nil, errInvalidSample
		}
		value, ok := point.Values[0].(float64)
		if !ok {
			return nil, errInvalidSample
		}
		if int64(point.Epoch)%nanosPerMillisecond != 0 {
			return nil, errUnexpectedTimeStep
		}
		samples[i] = &prompb.Sample{
			Timestamp: int64(point.Epoch) / nanosPerMillisecond,
			Value:     value,
		}
	}
	return &prompb.TimeSeries{Labels: labels, Samples: samples}, nil
}
