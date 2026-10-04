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

package dataset

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// DuplicatePolicy controls how a Builder treats points in one series that share an epoch.
type DuplicatePolicy byte

const (
	// DuplicatesKeep retains every point, including those sharing an epoch.
	DuplicatesKeep DuplicatePolicy = iota
	// DuplicatesFirstWins retains only the first-received point for each epoch.
	DuplicatesFirstWins
	// DuplicatesLastWins retains only the last-received point for each epoch.
	DuplicatesLastWins
	// DuplicatesError fails the build when a series receives two points for one epoch.
	DuplicatesError
)

var (
	// ErrDuplicateEpoch indicates a series received more than one point for an epoch.
	ErrDuplicateEpoch = fmt.Errorf("%w: duplicate epoch in series", timeseries.ErrInvalidBody)
	// ErrInvalidRow indicates a row or point that does not fit the Builder's fields.
	ErrInvalidRow = fmt.Errorf("%w: row does not match the dataset fields", timeseries.ErrInvalidBody)
	// ErrBuilderFinished indicates a Builder was used after Finish.
	ErrBuilderFinished = errors.New("dataset builder already finished")
)

// BuilderOptions configures a Builder.
type BuilderOptions struct {
	// Fields describes the timestamp, tag and value fields of each row in row mode.
	Fields timeseries.SeriesFields
	// SeriesName is the header name of each series created in row mode.
	SeriesName string
	// NameSeries, when set, names each series created in row mode from its tags, in place of SeriesName.
	NameSeries func(tags Tags) string
	// QueryStatement is the header query statement of each series created in row mode.
	QueryStatement string
	// Duplicates controls how points sharing an epoch within a series are handled.
	Duplicates DuplicatePolicy
	// SortSeries sorts each result's series by their tags when the build finishes.
	SortSeries bool
	// TagString converts a raw tag value, valid only during the call, to its Tags entry;
	// rows whose converted tags match share a series. When nil, raw bytes are used as-is.
	TagString func(fd timeseries.FieldDefinition, raw []byte) string
}

// Builder assembles a DataSet in a single pass from rows or points in any order, holding every
// series' rows by column in a few shared slabs. A Builder is not safe for concurrent use.
type Builder struct {
	trq      *timeseries.TimeRangeQuery
	opts     BuilderOptions
	log      *ColumnLog
	results  []*resultBuild
	result   *resultBuild
	current  *seriesBuild
	row      RowBuilder
	finished bool
}

// RowBuilder accumulates one row or point for a Builder; it is reused by each
// call to Builder.Row and is only valid until the next call.
type RowBuilder struct {
	b        *Builder
	epoch    epoch.Epoch
	hasEpoch bool
	invalid  bool
	tagBuf   []byte
	tagOff   []int
	key      []byte
}

type resultBuild struct {
	r      *Result
	series []*seriesBuild
	lookup map[string]*seriesBuild
	index  *seriesIndex[*seriesBuild]
	// the series of the last row looked up by its tags
	last *seriesBuild
}

type seriesBuild struct {
	s  *Series
	id int
	// the row key the series was first looked up by, and the series the row after its last one had
	key  string
	next *seriesBuild
}

// NewBuilder returns a Builder for the provided query and options.
func NewBuilder(trq *timeseries.TimeRangeQuery, opts BuilderOptions) *Builder {
	b := &Builder{trq: trq, opts: opts, log: NewColumnLog(opts.Duplicates)}
	b.row.b = b
	b.row.tagOff = make([]int, 2*len(opts.Fields.Tags))
	b.row.reset()
	return b
}

// Grow reserves room for rows more rows holding cells values and dataBytes bytes of bytes values,
// as a decoder that knows its input's size can estimate them.
func (b *Builder) Grow(rows, cells, dataBytes int) {
	b.log.Grow(rows, cells, dataBytes)
}

// SetResult ends any open series and directs subsequent rows and series to the
// result with the provided statement ID and name, creating it if needed.
func (b *Builder) SetResult(statementID int, name string) {
	if b.finished {
		return
	}
	b.current = nil
	for _, rb := range b.results {
		if rb.r.StatementID == statementID && rb.r.Name == name {
			b.result = rb
			return
		}
	}
	b.result = &resultBuild{
		r:      &Result{StatementID: statementID, Name: name, SeriesList: SeriesList{}},
		lookup: make(map[string]*seriesBuild),
		index:  newSeriesIndex(0, builtHeader),
	}
	b.results = append(b.results, b.result)
}

// StartSeries switches the Builder to series mode: rows and points go to the series
// with an identical header, created if needed, until EndSeries or the next StartSeries.
func (b *Builder) StartSeries(h SeriesHeader) {
	if b.finished {
		return
	}
	b.current = b.seriesFor(b.currentResult(), h, false)
}

// StartNewSeries is StartSeries, but opens a series of its own even beside one of an identical header,
// as a response that lists one series twice holds two.
func (b *Builder) StartNewSeries(h SeriesHeader) {
	if b.finished {
		return
	}
	rb := b.currentResult()
	b.current = b.newSeries(rb, h, h.CalculateHashWithQueryStatement(h.QueryStatement), false)
}

// EndSeries returns the Builder to row mode, where rows are grouped into series by their tags.
func (b *Builder) EndSeries() {
	b.current = nil
}

// Row returns the Builder's reusable RowBuilder, reset for a new row.
func (b *Builder) Row() *RowBuilder {
	b.row.reset()
	return &b.row
}

// AppendPoint appends p to the series opened by StartSeries.
func (b *Builder) AppendPoint(p Point) error {
	if b.finished {
		return ErrBuilderFinished
	}
	b.row.reset()
	if b.current == nil {
		return ErrInvalidRow
	}
	for _, v := range p.Values {
		b.log.AddValue(v)
	}
	return b.log.Commit(b.current.id, p.Epoch)
}

// Finish sorts only the series that arrived out of order, applies the duplicate
// policy, and returns the DataSet. The Builder cannot be used afterward.
func (b *Builder) Finish() (*DataSet, error) {
	if b.finished {
		return nil, ErrBuilderFinished
	}
	b.currentResult()
	// every series ends with every value field, those added after it was created holding nulls
	for _, rb := range b.results {
		for _, sb := range rb.series {
			b.widen(sb)
		}
	}
	b.finished = true
	b.current = nil
	segs, err := b.log.Finish()
	if err != nil {
		return nil, err
	}
	ds := &DataSet{TimeRangeQuery: b.trq, Results: make(Results, len(b.results))}
	if b.trq != nil {
		ds.ExtentList = timeseries.ExtentList{b.trq.Extent}
	}
	for i, rb := range b.results {
		rb.r.SeriesList = make(SeriesList, len(rb.series))
		for j, sb := range rb.series {
			if segs[sb.id].Len() > 0 {
				// each series' one Segment is cut from a shared list, capped so an append reallocates
				sb.s.segs = segs[sb.id : sb.id+1 : sb.id+1]
			}
			sb.s.Header.CalculateSize()
			rb.r.SeriesList[j] = sb.s
		}
		if b.opts.SortSeries {
			rb.r.SeriesList.SortByTags()
		}
		ds.Results[i] = rb.r
	}
	return ds, nil
}

// SetEpoch sets the row's timestamp.
func (r *RowBuilder) SetEpoch(e epoch.Epoch) {
	r.epoch = e
	r.hasEpoch = true
}

// SetTag sets the raw value of tag field i, per BuilderOptions.Fields.Tags; it
// is copied. Unset tags are omitted from the series' Tags.
func (r *RowBuilder) SetTag(i int, raw []byte) {
	if i < 0 || 2*i >= len(r.tagOff) {
		r.invalid = true
		return
	}
	r.tagOff[2*i] = len(r.tagBuf)
	r.tagBuf = append(r.tagBuf, raw...)
	r.tagOff[2*i+1] = len(r.tagBuf)
}

// AddValue appends the next value, per BuilderOptions.Fields.Values or the open series'
// ValueFieldsList, by its Go type; the typed adders avoid boxing it.
func (r *RowBuilder) AddValue(v any) {
	r.b.log.AddValue(v)
}

// AddNull appends a null as the next value.
func (r *RowBuilder) AddNull() {
	r.b.log.AddNull()
}

// AddBool appends a bool as the next value.
func (r *RowBuilder) AddBool(v bool) {
	r.b.log.AddBool(v)
}

// AddInt64 appends an int64 as the next value.
func (r *RowBuilder) AddInt64(v int64) {
	r.b.log.AddInt64(v)
}

// AddUint64 appends a uint64 as the next value.
func (r *RowBuilder) AddUint64(v uint64) {
	r.b.log.AddUint64(v)
}

// AddFloat64 appends a float64 as the next value.
func (r *RowBuilder) AddFloat64(v float64) {
	r.b.log.AddFloat64(v)
}

// AddString appends a copy of raw as the next value, as text.
func (r *RowBuilder) AddString(raw []byte) {
	r.b.log.AddString(raw)
}

// AddBytes appends a copy of raw as the next value, as bytes; nil appends a null.
func (r *RowBuilder) AddBytes(raw []byte) {
	r.b.log.AddBytes(raw)
}

// AddNumber appends a copy of raw, a number's literal text, as the next value.
func (r *RowBuilder) AddNumber(raw []byte) {
	r.b.log.AddNumber(raw)
}

// Commit adds the row to its series: the open series in series mode, or else the
// series matching the row's tags, which is created on first use.
func (r *RowBuilder) Commit() error {
	b := r.b
	if b.finished {
		return ErrBuilderFinished
	}
	if !r.hasEpoch || r.invalid {
		r.reset()
		return ErrInvalidRow
	}
	sb := b.current
	if sb != nil && slices.ContainsFunc(r.tagOff, func(o int) bool { return o >= 0 }) {
		r.reset()
		return ErrInvalidRow
	}
	if sb == nil {
		// a row that can't fit its series is refused before the series is created for it
		if n := len(b.opts.Fields.Values); n > 0 && b.log.Staged() != n {
			r.reset()
			return ErrInvalidRow
		}
		sb = r.series()
		b.widen(sb)
	}
	err := b.log.Commit(sb.id, r.epoch)
	r.reset()
	return err
}

// reset discards any values the row staged and not committed
func (r *RowBuilder) reset() {
	if !r.b.finished {
		r.b.log.Rollback()
	}
	r.hasEpoch = false
	r.invalid = false
	r.tagBuf = r.tagBuf[:0]
	for i := range r.tagOff {
		r.tagOff[i] = -1
	}
}

func (r *RowBuilder) series() *seriesBuild {
	// each tag as its length plus one and its bytes, or 0 when it's unset
	r.key = r.key[:0]
	for i := 0; i < len(r.tagOff); i += 2 {
		start, end := r.tagOff[i], r.tagOff[i+1]
		if start < 0 {
			r.key = append(r.key, 0)
			continue
		}
		r.key = binary.AppendUvarint(r.key, uint64(end-start)+1) // #nosec G115 -- a length is never negative
		r.key = append(r.key, r.tagBuf[start:end]...)
	}
	rb := r.b.currentResult()
	// rows repeat their series' order, a series' rows in a run or each time's rows in turn, so the series
	// that followed the last row's before is tried before the lookup
	if last := rb.last; last != nil && last.next != nil && last.next.key == string(r.key) {
		rb.last = last.next
		return last.next
	}
	sb, ok := rb.lookup[string(r.key)]
	if !ok {
		sb = r.newSeries(rb)
	}
	if rb.last != nil {
		rb.last.next = sb
	}
	rb.last = sb
	return sb
}

// newSeries returns the series for a row whose tags weren't looked up before: one of an equivalent
// header, or else a new one
func (r *RowBuilder) newSeries(rb *resultBuild) *seriesBuild {
	opts := &r.b.opts
	tags := make(Tags, len(opts.Fields.Tags))
	for i, fd := range opts.Fields.Tags {
		start, end := r.tagOff[2*i], r.tagOff[2*i+1]
		if start < 0 {
			continue
		}
		raw := r.tagBuf[start:end]
		if opts.TagString != nil {
			tags[fd.Name] = opts.TagString(fd, raw)
		} else {
			tags[fd.Name] = string(raw)
		}
	}
	name := opts.SeriesName
	if opts.NameSeries != nil {
		name = opts.NameSeries(tags)
	}
	// a new raw encoding can still name an existing series, as "a" and "\u0061" do in JSON
	sb := r.b.seriesFor(rb, SeriesHeader{
		Name:                name,
		Tags:                tags,
		TimestampField:      opts.Fields.Timestamp,
		TagFieldsList:       opts.Fields.Tags,
		ValueFieldsList:     opts.Fields.Values,
		UntrackedFieldsList: opts.Fields.Untracked,
		QueryStatement:      opts.QueryStatement,
	}, true)
	key := string(r.key)
	rb.lookup[key] = sb
	if sb.key == "" {
		sb.key = key
	}
	return sb
}

func (b *Builder) seriesFor(rb *resultBuild, h SeriesHeader, cloneFields bool) *seriesBuild {
	// series are matched the way merges match them, so no two can later merge as one
	hash := h.CalculateHashWithQueryStatement(h.QueryStatement)
	if sb, ok := rb.index.find(hash, &h); ok {
		return sb
	}
	return b.newSeries(rb, h, hash, cloneFields)
}

func (b *Builder) newSeries(rb *resultBuild, h SeriesHeader, hash Hash, cloneFields bool) *seriesBuild {
	if cloneFields {
		h.TagFieldsList = slices.Clone(h.TagFieldsList)
		h.ValueFieldsList = slices.Clone(h.ValueFieldsList)
		h.UntrackedFieldsList = slices.Clone(h.UntrackedFieldsList)
	}
	// a series that declares no value fields takes its width from its first row
	cols := len(h.ValueFieldsList)
	if cols == 0 {
		cols = -1
	}
	sb := &seriesBuild{s: &Series{Header: h}, id: b.log.AddSeries(cols)}
	rb.index.add(hash, sb)
	rb.series = append(rb.series, sb)
	return sb
}

// AddValueField adds a value field to the rows built from now on, in row mode. A series created before
// it is widened to it, holding a null in it on each earlier row.
func (b *Builder) AddValueField(fd timeseries.FieldDefinition) {
	if b.finished {
		return
	}
	// clipped, so the caller's array is never written past its length
	b.opts.Fields.Values = append(slices.Clip(b.opts.Fields.Values), fd)
}

// widen grows a row-mode series to the value fields added since it was created
func (b *Builder) widen(sb *seriesBuild) {
	n := len(b.opts.Fields.Values)
	h := &sb.s.Header
	if len(h.ValueFieldsList) >= n || b.log.Cols(sb.id) < 0 {
		return
	}
	h.ValueFieldsList = append(h.ValueFieldsList, b.opts.Fields.Values[len(h.ValueFieldsList):n]...)
	b.log.Widen(sb.id, n)
}

func builtHeader(sb *seriesBuild) *SeriesHeader {
	return &sb.s.Header
}

func (b *Builder) currentResult() *resultBuild {
	if b.result == nil {
		b.SetResult(0, "")
	}
	return b.result
}
