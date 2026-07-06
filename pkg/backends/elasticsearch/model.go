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

package elasticsearch

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

const (
	esStatusSuccess = "success"
	fieldBucketJSON = "bucket"
)

// NewModeler returns Elasticsearch modeling functions.
func NewModeler() *timeseries.Modeler {
	return &timeseries.Modeler{
		WireUnmarshalerReader: UnmarshalTimeseriesReader,
		WireMarshaler:         MarshalTimeseries,
		WireMarshalWriter:     MarshalTimeseriesWriter,
		WireUnmarshaler:       UnmarshalTimeseries,
		CacheMarshaler:        dataset.MarshalDataSet,
		CacheUnmarshaler:      dataset.UnmarshalDataSet,
	}
}

// UnmarshalTimeseries converts an Elasticsearch response into a DataSet.
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	return UnmarshalTimeseriesReader(bytes.NewReader(data), trq)
}

// UnmarshalTimeseriesReader converts an Elasticsearch response into a DataSet.
func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	if reader == nil {
		return nil, io.ErrUnexpectedEOF
	}
	plan, _ := trq.ParsedQuery.(*RequestPlan)
	if plan == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	switch plan.Kind {
	case requestKindMSearch:
		return unmarshalMSearchResponse(reader, trq, plan)
	default:
		return unmarshalSearchResponse(reader, trq, plan)
	}
}

func unmarshalSearchResponse(reader io.Reader, trq *timeseries.TimeRangeQuery,
	plan *RequestPlan,
) (timeseries.Timeseries, error) {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(reader).Decode(&body); err != nil {
		return nil, err
	}
	ds := newDataSet(trq)
	result, err := resultFromSearchBody(body, trq, plan.Searches[0], 0)
	if err != nil {
		return nil, err
	}
	ds.Results = []*dataset.Result{result}
	return ds, nil
}

func unmarshalMSearchResponse(reader io.Reader, trq *timeseries.TimeRangeQuery,
	plan *RequestPlan,
) (timeseries.Timeseries, error) {
	var body struct {
		Responses []json.RawMessage `json:"responses"`
	}
	if err := json.NewDecoder(reader).Decode(&body); err != nil {
		return nil, err
	}
	ds := newDataSet(trq)
	ds.Results = make([]*dataset.Result, 0, len(body.Responses))
	for i, raw := range body.Responses {
		if i >= len(plan.Searches) {
			break
		}
		var item map[string]json.RawMessage
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		result, err := resultFromSearchBody(item, trq, plan.Searches[i], i)
		if err != nil {
			return nil, err
		}
		ds.Results = append(ds.Results, result)
	}
	return ds, nil
}

func newDataSet(trq *timeseries.TimeRangeQuery) *dataset.DataSet {
	return &dataset.DataSet{
		Status:         esStatusSuccess,
		TimeRangeQuery: trq,
		ExtentList:     timeseries.ExtentList{trq.Extent},
	}
}

func resultFromSearchBody(body map[string]json.RawMessage, trq *timeseries.TimeRangeQuery,
	sp *SearchPlan, idx int,
) (*dataset.Result, error) {
	aggBody, err := aggregationResponse(body, sp.DateHistogramName)
	if err != nil {
		return nil, err
	}
	var agg struct {
		Buckets []json.RawMessage `json:"buckets"`
	}
	if err := json.Unmarshal(aggBody, &agg); err != nil {
		return nil, err
	}
	points := make(dataset.Points, 0, len(agg.Buckets))
	for _, bucket := range agg.Buckets {
		pt, err := pointFromBucket(bucket)
		if err != nil {
			return nil, err
		}
		points = append(points, pt)
	}
	slices.SortFunc(points, func(a, b dataset.Point) int {
		return cmp.Compare(a.Epoch, b.Epoch)
	})
	sh := dataset.SeriesHeader{
		Name: sp.DateHistogramName,
		Tags: dataset.Tags{
			"aggregation": sp.DateHistogramName,
		},
		TimestampField: timeseries.FieldDefinition{
			Name:     sp.TimestampField,
			DataType: timeseries.DateTimeUnixMilli,
			Role:     timeseries.RoleTimestamp,
		},
		ValueFieldsList: []timeseries.FieldDefinition{
			{Name: fieldBucketJSON, DataType: timeseries.String, Role: timeseries.RoleValue},
		},
		QueryStatement: trq.Statement,
	}
	sh.CalculateSize()
	return &dataset.Result{
		StatementID: idx,
		Name:        sp.DateHistogramName,
		SeriesList: []*dataset.Series{
			{
				Header:    sh,
				Points:    points,
				PointSize: points.Size(),
			},
		},
	}, nil
}

func aggregationResponse(body map[string]json.RawMessage, name string) (json.RawMessage, error) {
	var aggs map[string]json.RawMessage
	if raw, ok := body[aggKeyAggregations]; ok {
		if err := json.Unmarshal(raw, &aggs); err != nil {
			return nil, err
		}
	} else if raw, ok := body[aggKeyAggs]; ok {
		if err := json.Unmarshal(raw, &aggs); err != nil {
			return nil, err
		}
	}
	if aggs == nil {
		return nil, timeseries.ErrInvalidBody
	}
	raw, ok := aggs[name]
	if !ok {
		return nil, timeseries.ErrInvalidBody
	}
	return raw, nil
}

func pointFromBucket(raw json.RawMessage) (dataset.Point, error) {
	var bucket map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&bucket); err != nil {
		return dataset.Point{}, err
	}
	t, ok := bucketTime(bucket)
	if !ok {
		return dataset.Point{}, timeseries.ErrInvalidBody
	}
	s := string(raw)
	return dataset.Point{
		Epoch:  epoch.Epoch(t.UnixNano()),
		Size:   len(s) + 32,
		Values: []any{s},
	}, nil
}

func bucketTime(bucket map[string]any) (time.Time, bool) {
	if v, ok := bucket["key"]; ok {
		switch x := v.(type) {
		case json.Number:
			i, err := strconv.ParseInt(x.String(), 10, 64)
			return time.UnixMilli(i), err == nil
		case float64:
			return time.UnixMilli(int64(x)), true
		}
	}
	if v, ok := bucket["key_as_string"].(string); ok {
		t, err := time.Parse(time.RFC3339Nano, v)
		return t, err == nil
	}
	return time.Time{}, false
}

// MarshalTimeseries marshals a DataSet to Elasticsearch response JSON.
func MarshalTimeseries(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	err := MarshalTimeseriesWriter(ts, rlo, status, buf)
	return buf.Bytes(), err
}

// MarshalTimeseriesWriter marshals a DataSet to Elasticsearch response JSON.
func MarshalTimeseriesWriter(ts timeseries.Timeseries, rlo *timeseries.RequestOptions,
	status int, w io.Writer,
) error {
	if w == nil {
		return io.ErrClosedPipe
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil || rlo == nil {
		return timeseries.ErrUnknownFormat
	}
	plan, _ := rlo.ProviderRequest.(*RequestPlan)
	if plan == nil {
		return timeseries.ErrUnknownFormat
	}
	if hw, ok := w.(http.ResponseWriter); ok && hw != nil {
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	}
	switch plan.Kind {
	case requestKindMSearch:
		return marshalMSearchResponse(ds, plan, status, w)
	default:
		return marshalSearchResponse(ds, plan.Searches[0], 0, status, w)
	}
}

func marshalMSearchResponse(ds *dataset.DataSet, plan *RequestPlan, status int, w io.Writer) error {
	responses := make([]json.RawMessage, 0, len(plan.Searches))
	for i, sp := range plan.Searches {
		b, err := marshalSearchResponseBytes(ds, sp, i, status, true)
		if err != nil {
			return err
		}
		responses = append(responses, b)
	}
	return json.NewEncoder(w).Encode(map[string]any{"responses": responses})
}

func marshalSearchResponse(ds *dataset.DataSet, sp *SearchPlan, statementID, status int, w io.Writer) error {
	b, err := marshalSearchResponseBytes(ds, sp, statementID, status, false)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func marshalSearchResponseBytes(ds *dataset.DataSet, sp *SearchPlan, statementID,
	status int, includeStatus bool,
) ([]byte, error) {
	buckets, err := bucketsForStatement(ds, statementID)
	if err != nil {
		return nil, err
	}
	if status == 0 {
		status = http.StatusOK
	}
	out := map[string]any{
		"took":      0,
		"timed_out": false,
		"hits": map[string]any{
			"total": map[string]any{"value": 0, "relation": "eq"},
			"hits":  []any{},
		},
		"aggregations": map[string]any{
			sp.DateHistogramName: map[string]any{
				"buckets": buckets,
			},
		},
	}
	if includeStatus || status != http.StatusOK {
		out["status"] = status
	}
	return json.Marshal(out)
}

func bucketsForStatement(ds *dataset.DataSet, statementID int) ([]json.RawMessage, error) {
	for _, r := range ds.Results {
		if r == nil || r.StatementID != statementID {
			continue
		}
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			pts := s.Points.Clone()
			slices.SortFunc(pts, func(a, b dataset.Point) int {
				return cmp.Compare(a.Epoch, b.Epoch)
			})
			out := make([]json.RawMessage, 0, len(pts))
			for _, p := range pts {
				if len(p.Values) == 0 {
					continue
				}
				raw, ok := p.Values[0].(string)
				if !ok {
					continue
				}
				out = append(out, json.RawMessage(raw))
			}
			return out, nil
		}
	}
	return []json.RawMessage{}, nil
}
