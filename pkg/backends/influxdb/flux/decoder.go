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

package flux

import (
	"bytes"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// dataStartRow is the index in a Flux CSV matrix at which the header rows have
// ended and the data rows have started
const dataStartRow = 4

const (
	annotationDatatype = "#datatype"
	annotationGroup    = "#group"
	annotationDefault  = "#default"
	// the column a row's result name is read from
	resultNameColumn = 1
	// separate a tag's name from its value, and one tag from the next, in a series' name; neither is
	// likely to be in a tag
	tagValueDelimiter = `/°|³\`
	tagDelimiter      = `/✓¿⨉\`
	logKeyRows        = "rows"
)

// what a table's next record is, as its annotations and header come first
const (
	stageStart = iota
	stageGroup
	stageDefault
	stageHeader
	stageData
)

// UnmarshalTimeseries converts a Flux CSV into a Timeseries
func UnmarshalTimeseries(data []byte,
	trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	return stream.BytesUnmarshaler(newDecoder)(data, trq)
}

// UnmarshalTimeseriesReader converts a Flux CSV into a Timeseries via io.Reader
func UnmarshalTimeseriesReader(reader io.Reader,
	trq *timeseries.TimeRangeQuery,
) (timeseries.Timeseries, error) {
	return stream.ReaderUnmarshaler(newDecoder)(reader, trq)
}

// decoder builds a DataSet from annotated Flux CSV. Each table starts with its #datatype, #group and
// #default annotations and a header, which lay out its columns; a series is a result and tag values.
type decoder struct {
	b     *dataset.Builder
	stage int
	// the table's annotation rows, copied, until its header completes them
	annotations [][]string
	width       int
	fields      timeseries.SeriesFields
	// the positions of the cells that tell a row's series, and the current series' cells, joined, and
	// where each ends, which a row that continues the series repeats
	keyCells []int
	key      []byte
	keyEnds  []int
	results  bool
	result   string
	interned stream.Interner
	// rows dropped for their times, which are logged once
	dropped int
	dropErr error
}

func newDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	d := &decoder{b: dataset.NewBuilder(trq, dataset.BuilderOptions{})}
	// a table's records are as wide as its columns, and each table has its own
	return stream.NewCSV(d.record, d.finish).SetFieldsPerRecord(-1), nil
}

func (d *decoder) record(fields [][]byte) error {
	if string(fields[0]) == annotationDatatype {
		// a new table, so the last one must have had its header
		if d.stage != stageStart && d.stage != stageData {
			return timeseries.ErrInvalidBody
		}
		d.annotations = append(d.annotations[:0], copyRecord(fields))
		d.stage, d.width = stageGroup, len(fields)
		return nil
	}
	switch d.stage {
	case stageStart:
		return timeseries.ErrInvalidBody
	case stageGroup, stageDefault:
		want := annotationGroup
		if d.stage == stageDefault {
			want = annotationDefault
		}
		if string(fields[0]) != want {
			return timeseries.ErrInvalidBody
		}
		d.annotations = append(d.annotations, copyRecord(fields))
		d.stage++
		return nil
	case stageHeader:
		return d.header(fields)
	}
	return d.row(fields)
}

func copyRecord(fields [][]byte) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = string(f)
	}
	return out
}

// header lays out the table's columns from its annotations and header, each field shared by its series
func (d *decoder) header(fields [][]byte) error {
	sf, err := buildFieldDefinitions(append(d.annotations, copyRecord(fields)), nil)
	if err != nil {
		return err
	}
	if sf.Timestamp.OutputPosition < 0 {
		return timeseries.ErrInvalidBody
	}
	sf.Tags, sf.Values, sf.Untracked = slices.Clip(sf.Tags), slices.Clip(sf.Values), slices.Clip(sf.Untracked)
	d.fields, d.stage = sf, stageData
	d.keyCells = append(d.keyCells[:0], resultNameColumn)
	for _, fd := range sf.Tags {
		d.keyCells = append(d.keyCells, fd.OutputPosition)
	}
	// the table's first row starts a series, whatever the last table's was
	d.keyEnds = d.keyEnds[:0]
	return nil
}

// sameSeries reports whether a row's result and tag cells are the current series'
func (d *decoder) sameSeries(fields [][]byte) bool {
	if len(d.keyEnds) == 0 {
		return false
	}
	start := 0
	for i, pos := range d.keyCells {
		end := d.keyEnds[i]
		if !bytes.Equal(fields[pos], d.key[start:end]) {
			return false
		}
		start = end
	}
	return true
}

// row adds a data row to its series; a row that isn't as wide as its table is skipped
func (d *decoder) row(fields [][]byte) error {
	if len(fields) != d.width {
		return nil
	}
	// a series is known by its result and tag values, which a table's rows usually share
	if !d.sameSeries(fields) {
		d.startSeries(fields)
		d.key, d.keyEnds = d.key[:0], d.keyEnds[:0]
		for _, pos := range d.keyCells {
			d.key = append(d.key, fields[pos]...)
			d.keyEnds = append(d.keyEnds, len(d.key))
		}
	}
	tfd := &d.fields.Timestamp
	e, err := parseTimeField(fields[tfd.OutputPosition], *tfd)
	if err != nil {
		d.dropped++
		if d.dropErr == nil {
			d.dropErr = err
		}
		return nil
	}
	r := d.b.Row()
	r.SetEpoch(e)
	for i := range d.fields.Values {
		addValue(r, fields[d.fields.Values[i].OutputPosition], d.fields.Values[i].DataType)
	}
	return r.Commit()
}

// startSeries starts the series of a row's result and tags, named for them
func (d *decoder) startSeries(fields [][]byte) {
	if result := fields[resultNameColumn]; !d.results || string(result) != d.result {
		d.result = d.interned.Short(result)
		d.b.SetResult(0, d.result)
		d.results = true
	}
	tags := make(dataset.Tags, len(d.fields.Tags))
	var name strings.Builder
	name.WriteString(d.result)
	name.WriteByte('.')
	named := false
	for _, fd := range d.fields.Tags {
		v := fields[fd.OutputPosition]
		if len(v) == 0 {
			continue
		}
		tags[fd.Name] = d.interned.Short(v)
		if fd.Name == "" {
			continue
		}
		if named {
			name.WriteString(tagDelimiter)
		}
		named = true
		name.WriteString(fd.Name)
		name.WriteString(tagValueDelimiter)
		name.Write(v)
	}
	d.b.StartSeries(dataset.SeriesHeader{
		Name:                name.String(),
		Tags:                tags,
		TimestampField:      d.fields.Timestamp,
		TagFieldsList:       d.fields.Tags,
		ValueFieldsList:     d.fields.Values,
		UntrackedFieldsList: d.fields.Untracked,
	})
}

// addValue adds a cell parsed by its column's type; an empty cell or one that doesn't parse is null
func addValue(r *dataset.RowBuilder, cell []byte, dt timeseries.FieldDataType) {
	if len(cell) == 0 {
		r.AddNull()
		return
	}
	switch dt {
	case timeseries.Int64:
		if v, err := strconv.ParseInt(string(cell), 10, 64); err == nil {
			r.AddInt64(v)
			return
		}
	case timeseries.Uint64:
		if v, err := strconv.ParseUint(string(cell), 10, 64); err == nil {
			r.AddUint64(v)
			return
		}
	case timeseries.Float64:
		if v, err := strconv.ParseFloat(string(cell), 64); err == nil {
			r.AddFloat64(v)
			return
		}
	case timeseries.Bool:
		if v, err := strconv.ParseBool(string(cell)); err == nil {
			r.AddBool(v)
			return
		}
	case timeseries.String, timeseries.DateTimeRFC3339, timeseries.DateTimeRFC3339Nano:
		r.AddString(cell)
		return
	}
	r.AddNull()
}

func (d *decoder) finish() (timeseries.Timeseries, error) {
	// a body without a whole table has no header, which the data stage follows
	if d.stage != stageData {
		return nil, timeseries.ErrInvalidBody
	}
	if d.dropped > 0 {
		logger.Error("failed to parse timestamp", logging.Pairs{keys.Error: d.dropErr, logKeyRows: d.dropped})
	}
	ds, err := d.b.Finish()
	if err != nil {
		return nil, err
	}
	if !d.results {
		// the Builder always has a result, which tables without rows don't
		ds.Results = dataset.Results{}
	}
	return ds, nil
}

// buildFieldDefinitions is the FieldParserFunc passed to the Parser
func buildFieldDefinitions(rows [][]string,
	_ *timeseries.TimeRangeQuery,
) (timeseries.SeriesFields, error) {
	l := len(rows[dataStartRow-1])
	if l < dataStartRow {
		return timeseries.SeriesFields{}, timeseries.ErrInvalidBody
	}
	// the first 4 rows must have an identical # of cells
	for i := range dataStartRow - 1 {
		if len(rows[i]) != l {
			return timeseries.SeriesFields{}, timeseries.ErrInvalidBody
		}
	}
	var j, k, u int
	outTags := make(timeseries.FieldDefinitions, l)
	outVals := make(timeseries.FieldDefinitions, l)
	outUntracked := make(timeseries.FieldDefinitions, l)
	tfd := timeseries.FieldDefinition{OutputPosition: -1}
	for i := range l {
		if i == 0 {
			// empty column on flux CSVs
			outUntracked[u] = timeseries.FieldDefinition{}
			u++
			continue
		}
		fd := loadFieldDef(rows[3][i], rows[0][i], rows[1][i], rows[2][i], i)
		switch fd.Role {
		case timeseries.RoleTag: // add to tags
			outTags[j] = fd
			j++
		case timeseries.RoleTimestamp: // set as timestamp
			tfd = fd
		case timeseries.RoleUntracked: // skip untracked field
			outUntracked[u] = fd
			u++
		case timeseries.RoleValue: // add to values
			outVals[k] = fd
			k++
		}
	}
	return timeseries.SeriesFields{
		Timestamp: tfd, Tags: outTags[:j],
		Values: outVals[:k], Untracked: outUntracked[:u], ResultNameCol: 1,
	}, nil
}

// loadFieldDef returns a field definition from the name, datatype and group.
func loadFieldDef(n, d, g, v string, pos int) timeseries.FieldDefinition {
	fd := timeseries.FieldDefinition{
		Name:           n,
		DataType:       typeToFieldDataType(d),
		SDataType:      d,
		DefaultValue:   v,
		OutputPosition: pos,
		Role:           timeseries.RoleValue,
	}
	switch {
	case n == stopColumnName || n == startColumnName || n == tableColumnName:
		fd.Role = timeseries.RoleUntracked // is an untracked field
	case n == timeColumnName || n == timeAltColumnName:
		fd.Role = timeseries.RoleTimestamp // is a timestamp field
	case n == resultColumnName || g == sTrue:
		fd.Role = timeseries.RoleTag // is a key/tag field
	}
	return fd
}

// typeToFieldDataType is the DataTypeParserFunc passed to the Parser
func typeToFieldDataType(input string) timeseries.FieldDataType {
	switch input {
	case TypeString, TypeDuration:
		return timeseries.String
	case TypeLong:
		return timeseries.Int64
	case TypeUnsignedLong:
		return timeseries.Uint64
	case TypeDouble:
		return timeseries.Float64
	case TypeBool:
		return timeseries.Bool
	case TypeRFC3339:
		return timeseries.DateTimeRFC3339
	case TypeRFC3339Nano, timeColumnName:
		return timeseries.DateTimeRFC3339Nano
	case TypeNull:
		return timeseries.Null
	}
	return timeseries.Unknown
}

func parseTimeField(input []byte, tfd timeseries.FieldDefinition) (epoch.Epoch, error) {
	switch tfd.DataType {
	case timeseries.DateTimeRFC3339:
		return epoch.ParseRFC3339(input, time.RFC3339)
	case timeseries.DateTimeRFC3339Nano:
		return epoch.ParseRFC3339(input, time.RFC3339Nano)
	}
	return 0, timeseries.ErrInvalidTimeFormat
}
