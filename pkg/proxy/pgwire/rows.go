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

package pgwire

import (
	"cmp"
	"errors"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	rowValue      = "row"
	sequenceValue = "sequence"
)

var errGroupColumn = errors.New("a group column cannot be identified in the result")

type rowSink struct {
	// models a delta fetch's rows as a DataSet: a point holds a DataRow body as sent, and its series
	// are the rows' group column values
	plan    *sqlanalyzer.QueryPlan
	builder *dataset.Builder
	header  []byte
	time    int
	groups  []int
	decoder *timeAxisDecoder
	// sequenced records each row's arrival, which orders a bucket's rows when the statement orders
	// by more than the bucket
	sequenced bool
	rows      int
	bytes     int
}

func newRowSink(plan *sqlanalyzer.QueryPlan) *rowSink {
	return &rowSink{plan: plan, sequenced: len(plan.Ordering) > 1}
}

func (k *rowSink) describe(s *session, body []byte) error {
	var description pgproto3.RowDescription
	if err := description.Decode(body); err != nil {
		return nativedelta.Unmergeable(err)
	}
	k.time = -1
	k.groups = make([]int, len(k.plan.GroupColumns))
	for i := range k.groups {
		k.groups[i] = -1
	}
	for i := range description.Fields {
		name := string(description.Fields[i].Name)
		if name == k.plan.OutputColumn {
			if k.time >= 0 {
				return nativedelta.Unmergeable(errTimeColumn)
			}
			k.time = i
		}
		for j, group := range k.plan.GroupColumns {
			if name == group {
				if k.groups[j] >= 0 {
					return nativedelta.Unmergeable(errGroupColumn)
				}
				k.groups[j] = i
			}
		}
	}
	if k.time < 0 || description.Fields[k.time].Format != textFormat {
		return nativedelta.Unmergeable(errTimeColumn)
	}
	for _, column := range k.groups {
		if column < 0 {
			return nativedelta.Unmergeable(errGroupColumn)
		}
	}
	engine := s.server.config.Engine
	kind, ok := engine.TimeAxis(description.Fields[k.time].DataTypeOID)
	if !ok {
		return nativedelta.Unmergeable(errTimeColumn)
	}
	decoder, err := newTimeAxisDecoder(kind, k.plan.OutputUnit, engine.TimeSemantics(), s.tracker.setting)
	if err != nil {
		return nativedelta.Unmergeable(err)
	}
	k.decoder, k.header = decoder, body
	fields := timeseries.SeriesFields{
		Timestamp: timeseries.FieldDefinition{Name: k.plan.OutputColumn, Role: timeseries.RoleTimestamp},
		Tags:      make(timeseries.FieldDefinitions, len(k.plan.GroupColumns)),
		Values:    timeseries.FieldDefinitions{{Name: rowValue}},
	}
	for i, group := range k.plan.GroupColumns {
		fields.Tags[i] = timeseries.FieldDefinition{Name: group, Role: timeseries.RoleTag}
	}
	if k.sequenced {
		fields.Values = append(fields.Values, timeseries.FieldDefinition{Name: sequenceValue})
	}
	k.builder = dataset.NewBuilder(nil, dataset.BuilderOptions{
		Fields: fields, Duplicates: dataset.DuplicatesError,
	})
	return nil
}

func (k *rowSink) row(body []byte) error {
	// body is valid only during the call, so the row is copied
	if k.builder == nil {
		return errResultRow
	}
	bucket, err := bucketTime(body, k.time, k.decoder, k.plan.Step, k.plan.Phase)
	if err != nil {
		return err
	}
	r := k.builder.Row()
	r.SetEpoch(epoch.Epoch(bucket))
	for i, column := range k.groups {
		value, err := rowColumn(body, column)
		if err != nil {
			return err
		}
		// a NULL group is left unset, so it stays apart from an empty one
		if value != nil {
			r.SetTag(i, value)
		}
	}
	r.AddBytes(body)
	if k.sequenced {
		r.AddInt(int64(k.rows))
	}
	if err := r.Commit(); err != nil {
		return errors.Join(errResultRow, err)
	}
	k.rows++
	k.bytes += len(body)
	return nil
}

func (k *rowSink) finish() (*nativedelta.Delta, error) {
	if k.builder == nil {
		return nil, errResultRow
	}
	ds, err := k.builder.Finish()
	if err != nil {
		return nil, errors.Join(errResultRow, err)
	}
	return &nativedelta.Delta{Header: k.header, DS: ds}, nil
}

func bySequence(a, b dataset.Row) int {
	// the origin's order within a bucket, recorded as each row arrived; a cached row's sequence
	// has come back from the cache encoding as an int64 too
	return cmp.Compare(sequenceOf(a), sequenceOf(b))
}

func sequenceOf(r dataset.Row) int64 {
	if len(r.Point.Values) < 2 {
		return 0
	}
	n, _ := dataset.IntValue(r.Point.Values[1])
	return n
}

func writeDelta(w *frameWriter, d *nativedelta.Delta, plan *sqlanalyzer.QueryPlan) {
	// delta rows as a whole response to one Query: buckets in the plan's order, and a bucket's rows in
	// the origin's order when the plan orders by more than the bucket
	order := dataset.RowOrder{Descending: descending(plan)}
	if len(plan.Ordering) > 1 {
		order.Compare = bySequence
	}
	if d.Header != nil {
		w.frame(msgRowDescription, d.Header)
	}
	var rows int64
	for _, r := range d.DS.Results {
		for row := range r.Rows(order) {
			body, _ := dataset.BytesValue(row.Point.Values[0])
			w.frame(msgDataRow, body)
			rows++
		}
	}
	writeSelectComplete(w, rows)
}

// frames a CommandComplete tagged with the rows a SELECT returned, and ReadyForQuery
func writeSelectComplete(w *frameWriter, rows int64) {
	var tag [len(selectTagPrefix) + 20]byte
	w.commandComplete(strconv.AppendInt(append(tag[:0], selectTagPrefix...), rows, 10))
	w.frame(msgReadyForQuery, readyIdle)
}
