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

package mysql

import (
	"encoding/binary"
	"errors"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
)

const rowValue = "row"

var errRowBlob = errors.New("invalid MySQL delta row")

type rowSink struct {
	// models a delta fetch's rows as a DataSet: a point holds a row's column values as sent, and its
	// series are the rows' group column values
	h          *protocolHandler
	plan       *sqlanalyzer.QueryPlan
	fields     []*querypb.Field
	builder    *dataset.Builder
	time       int
	groups     []int
	comparator *groupComparator
	scratch    []byte
}

func (h *protocolHandler) newRowSink(plan *sqlanalyzer.QueryPlan, fields []*querypb.Field) (*rowSink, error) {
	timeIndex, groups, err := resultIndexes(fields, plan)
	if err != nil {
		return nil, nativedelta.Unmergeable(err)
	}
	comparator, err := h.newGroupComparator(fields, groups)
	if err != nil {
		return nil, nativedelta.Unmergeable(err)
	}
	tags := make(timeseries.FieldDefinitions, len(groups))
	for i, index := range groups {
		tags[i] = timeseries.FieldDefinition{Name: fields[index].Name, Role: timeseries.RoleTag}
	}
	return &rowSink{
		h: h, plan: plan, fields: fields, time: timeIndex, groups: groups, comparator: comparator,
		builder: dataset.NewBuilder(nil, dataset.BuilderOptions{
			Fields: timeseries.SeriesFields{
				Timestamp: timeseries.FieldDefinition{Name: plan.OutputColumn, Role: timeseries.RoleTimestamp},
				Tags:      tags,
				Values:    timeseries.FieldDefinitions{{Name: rowValue}},
			},
			Duplicates: dataset.DuplicatesError,
		}),
	}, nil
}

func (k *rowSink) row(row []sqltypes.Value) error {
	if len(row) != len(k.fields) {
		return nativedelta.Unmergeable(errRowBlob)
	}
	if err := k.comparator.validateRow(row); err != nil {
		return nativedelta.Unmergeable(err)
	}
	at, err := k.h.resultEpoch(row[k.time], k.plan.OutputUnit)
	if err != nil {
		return nativedelta.Unmergeable(err)
	}
	r := k.builder.Row()
	r.SetEpoch(epoch.Epoch(at))
	for i, index := range k.groups {
		// a NULL group is left unset, so it stays apart from an empty one
		if value := row[index]; !value.IsNull() {
			r.SetTag(i, value.Raw())
		}
	}
	k.scratch = appendRowBlob(k.scratch[:0], row)
	r.AddBytes(k.scratch)
	if err := r.Commit(); err != nil {
		return nativedelta.Unmergeable(err)
	}
	return nil
}

func (k *rowSink) finish(statusFlags uint16) (*nativedelta.Delta, error) {
	ds, err := k.builder.Finish()
	if err != nil {
		return nil, nativedelta.Unmergeable(err)
	}
	header, err := resultCodec{}.Marshal(&sqltypes.Result{Fields: k.fields, StatusFlags: statusFlags})
	if err != nil {
		return nil, err
	}
	return &nativedelta.Delta{Header: header, DS: ds}, nil
}

func appendRowBlob(out []byte, row []sqltypes.Value) []byte {
	// each column is its byte length plus one, then its bytes; zero marks a NULL
	for _, value := range row {
		if value.IsNull() {
			out = append(out, 0)
			continue
		}
		raw := value.Raw()
		out = binary.AppendUvarint(out, uint64(len(raw))+1)
		out = append(out, raw...)
	}
	return out
}

func decodeRowBlob(row []sqltypes.Value, blob []byte, fields []*querypb.Field) error {
	// fills row, one value per field, with values that refer to blob's bytes, which the cached rows
	// never modify
	for i := range row {
		size, n := binary.Uvarint(blob)
		// #nosec G115 -- a length is never negative
		if n <= 0 || size > uint64(len(blob)-n)+1 {
			return errRowBlob
		}
		blob = blob[n:]
		if size == 0 {
			row[i] = sqltypes.NULL
			continue
		}
		row[i] = sqltypes.MakeTrusted(fields[i].Type, blob[:size-1:size-1])
		blob = blob[size-1:]
	}
	if len(blob) != 0 {
		return errRowBlob
	}
	return nil
}

func sameResultHeader(a, b []byte) bool {
	// parts merge when their columns are named, typed and collated alike
	left, errLeft := resultCodec{}.Unmarshal(a)
	right, errRight := resultCodec{}.Unmarshal(b)
	return errLeft == nil && errRight == nil && compatibleFields(left.Fields, right.Fields)
}

func (h *protocolHandler) deltaResult(d *nativedelta.Delta, plan *sqlanalyzer.QueryPlan) (*sqltypes.Result, error) {
	// the rows in time order, and within a bucket by their group columns under MySQL's rules
	meta, err := resultCodec{}.Unmarshal(d.Header)
	if err != nil {
		return nil, err
	}
	_, groups, err := resultIndexes(meta.Fields, plan)
	if err != nil {
		return nil, err
	}
	comparator, err := h.newGroupComparator(meta.Fields, groups)
	if err != nil {
		return nil, err
	}
	out := cloneResultMetadata(meta)
	width := len(meta.Fields)
	rows := d.Rows()
	out.Rows = make([]sqltypes.Row, 0, rows)
	// every row's values share one allocation
	values := make([]sqltypes.Value, rows*width)
	var compareErr error
	sortBucket := func(from int) {
		slices.SortStableFunc(out.Rows[from:], func(a, b sqltypes.Row) int {
			order, err := comparator.compare(a, b)
			if err != nil && compareErr == nil {
				compareErr = err
			}
			return order
		})
	}
	for _, r := range d.DS.Results {
		bucket, at := len(out.Rows), epoch.Epoch(0)
		for row := range r.Rows(dataset.RowOrder{}) {
			if len(out.Rows) > bucket && row.Point.Epoch != at {
				sortBucket(bucket)
				bucket = len(out.Rows)
			}
			at = row.Point.Epoch
			blob, _ := dataset.BytesValue(row.Point.Values[0])
			next := len(out.Rows) * width
			decoded := values[next : next+width : next+width]
			if err := decodeRowBlob(decoded, blob, meta.Fields); err != nil {
				return nil, err
			}
			out.Rows = append(out.Rows, decoded)
		}
		sortBucket(bucket)
	}
	return out, compareErr
}
