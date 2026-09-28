/*
 * Copyright 2026 The Trickster Authors
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

// Package model preserves GreptimeDB's typed HTTP SQL result envelope.
package model

import (
	"bytes"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// Schema lives outside the series so empty and cropped results retain it.
// Data and extent operations still use the common DataSet implementation.
type dataSet struct {
	*dataset.DataSet
	fields  timeseries.FieldDefinitions
	invalid bool
}

var _ timeseries.Timeseries = (*dataSet)(nil)

func NewModeler() *timeseries.Modeler {
	return timeseries.NewModeler(UnmarshalTimeseries, UnmarshalTimeseriesReader,
		MarshalTimeseries, MarshalTimeseriesWriter, unmarshalCache, marshalCache)
}

func (d *dataSet) Clone() timeseries.Timeseries {
	return &dataSet{DataSet: d.DataSet.Clone().(*dataset.DataSet), fields: d.fields.Clone(), invalid: d.invalid}
}

func (d *dataSet) CroppedClone(extent timeseries.Extent) timeseries.Timeseries {
	clone := d.Clone().(*dataSet)
	clone.CropToRange(extent)
	return clone
}

func (d *dataSet) Merge(sortPoints bool, inputs ...timeseries.Timeseries) {
	parts := make([]timeseries.Timeseries, 0, len(inputs))
	for _, input := range inputs {
		part, ok := input.(*dataSet)
		if !ok || part == nil || part.invalid || !slices.Equal(d.fields, part.fields) {
			d.invalid = true
			return
		}
		parts = append(parts, part.DataSet)
	}
	d.DataSet.Merge(sortPoints, parts...)
}

func (d *dataSet) Size() int64 {
	size := d.DataSet.Size()
	for _, field := range d.fields {
		size += int64(field.Size())
	}
	return size
}

var cacheVersion = []byte{'G', 'S', 'Q', 'L', 1}

func marshalCache(ts timeseries.Timeseries, options *timeseries.RequestOptions, status int) ([]byte, error) {
	d, ok := ts.(*dataSet)
	if !ok || d == nil || d.invalid || d.DataSet == nil {
		return nil, timeseries.ErrUnknownFormat
	}
	fields, err := d.fields.MarshalMsg(slices.Clone(cacheVersion))
	if err != nil {
		return nil, err
	}
	body, err := dataset.MarshalDataSet(d.DataSet, options, status)
	if err != nil {
		return nil, err
	}
	return append(fields, body...), nil
}

func unmarshalCache(body []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	if !bytes.HasPrefix(body, cacheVersion) {
		return nil, timeseries.ErrUnknownFormat
	}
	d := &dataSet{DataSet: &dataset.DataSet{}}
	rest, err := d.fields.UnmarshalMsg(body[len(cacheVersion):])
	if err != nil || len(d.fields) == 0 {
		return nil, timeseries.ErrInvalidBody
	}
	rest, err = d.DataSet.UnmarshalMsg(rest)
	if err != nil || len(rest) != 0 {
		return nil, timeseries.ErrInvalidBody
	}
	if trq != nil {
		d.TimeRangeQuery = trq
	} else if d.TimeRangeQuery != nil {
		d.TimeRangeQuery.Step = time.Duration(d.TimeRangeQuery.StepNS)
		d.TimeRangeQuery.PolicyStep = time.Duration(d.TimeRangeQuery.PolicyStepNS)
	}
	return d, nil
}
