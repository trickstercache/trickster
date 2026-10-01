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
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"sync"
	"unsafe"

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
	left, err := resultCodec{}.Unmarshal(a)
	if err != nil {
		return false
	}
	if bytes.Equal(a, b) {
		return true
	}
	right, err := resultCodec{}.Unmarshal(b)
	return err == nil && compatibleFields(left.Fields, right.Fields)
}

// a hit's rendered rows and their values, which are reused once the client has been sent them
type renderBuffers struct {
	values []sqltypes.Value
	rows   []sqltypes.Row
}

// buffers that grew past this are dropped rather than kept for reuse
const maxPooledRender = 1 << 20

var renders = sync.Pool{New: func() any { return &renderBuffers{} }}

func getRenderBuffers() *renderBuffers {
	return renders.Get().(*renderBuffers)
}

func (b *renderBuffers) release() {
	// clears the values, which refer to cached rows, and keeps the buffers for reuse; neither the result
	// they rendered nor its rows may be used after it
	if b == nil {
		return
	}
	clear(b.values)
	clear(b.rows[:cap(b.rows)])
	if cap(b.values)*int(unsafe.Sizeof(sqltypes.Value{}))+cap(b.rows)*int(unsafe.Sizeof(sqltypes.Row{})) >
		maxPooledRender {
		return
	}
	b.values, b.rows = b.values[:0], b.rows[:0]
	renders.Put(b)
}

func (h *protocolHandler) renderDelta(d *nativedelta.Delta, plan *sqlanalyzer.QueryPlan, b *renderBuffers,
) (*sqltypes.Result, error) {
	// the rows in time order, and within a bucket by their group columns under MySQL's rules, in b
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
	// every row's values share one slab
	b.values = slices.Grow(b.values[:0], rows*width)[:rows*width]
	b.rows = slices.Grow(b.rows[:0], rows)
	values := b.values
	out.Rows = b.rows
	for _, r := range d.DS.Results {
		if len(groups) > 0 {
			if r, err = seriesInGroupOrder(r, comparator, meta.Fields); err != nil {
				return nil, err
			}
		}
		// the series are in group order, which is how the rows come out within each bucket
		for row := range r.Rows(dataset.RowOrder{}) {
			blob := rowBlob(row.Seg, row.Index)
			next := len(out.Rows) * width
			decoded := values[next : next+width : next+width]
			if err := decodeRowBlob(decoded, blob, meta.Fields); err != nil {
				return nil, err
			}
			out.Rows = append(out.Rows, decoded)
		}
	}
	b.rows = out.Rows
	return out, nil
}

// rowBlob returns a row's packed values, or nil for a null row
func rowBlob(seg *dataset.Segment, i int) []byte {
	if seg.NumCols() == 0 || !seg.KindAt(0, i).IsBytes() {
		return nil
	}
	return seg.Bytes(0, i)
}

// r, or a copy with its series in group order; a series' rows share group values and a bucket holds
// one row per series, so this orders each bucket as sorting its rows would
func seriesInGroupOrder(r *dataset.Result, comparator *groupComparator, fields []*querypb.Field,
) (*dataset.Result, error) {
	list := r.SeriesList
	if len(list) < 2 {
		return r, nil
	}
	width := len(fields)
	// each series' group values, from its first row; a series without rows yields none and goes last
	firsts := make([]sqltypes.Value, len(list)*width)
	order := make([]int, 0, len(list))
	for i, s := range list {
		if s == nil || s.PointCount() == 0 {
			continue
		}
		var blob []byte
		if segs := s.Segments(); len(segs) > 0 {
			seg := &segs[0]
			for k := 1; seg.Len() == 0 && k < len(segs); k++ {
				seg = &segs[k]
			}
			blob = rowBlob(seg, 0)
		}
		if err := decodeRowBlob(firsts[i*width:(i+1)*width], blob, fields); err != nil {
			return nil, err
		}
		order = append(order, i)
	}
	var compareErr error
	slices.SortStableFunc(order, func(a, b int) int {
		c, err := comparator.compare(firsts[a*width:(a+1)*width], firsts[b*width:(b+1)*width])
		if err != nil && compareErr == nil {
			compareErr = err
		}
		return c
	})
	if compareErr != nil {
		return nil, compareErr
	}
	if len(order) == len(list) && slices.IsSorted(order) {
		return r, nil
	}
	ordered := &dataset.Result{
		StatementID: r.StatementID, Error: r.Error, Name: r.Name,
		SeriesList: make(dataset.SeriesList, len(order)),
	}
	for i, index := range order {
		ordered.SeriesList[i] = list[index]
	}
	return ordered, nil
}
