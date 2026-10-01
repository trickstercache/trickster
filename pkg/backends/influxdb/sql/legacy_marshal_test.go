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

package sql

import (
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// v3TimestampOutputLayout matches InfluxDB 3's native output shape: naive UTC
// with no zone suffix, fractional seconds only when present.
const v3TimestampOutputLayout = "2006-01-02T15:04:05.999999999"

// legacyMarshal writes ds as the marshalers did before they streamed time-ordered rows
func legacyMarshal(w io.Writer, ds *dataset.DataSet, of byte) error {
	switch of {
	case iofmt.V3OutputJSONL:
		return legacyMarshalJSONL(w, ds)
	case iofmt.V3OutputCSV:
		return legacyMarshalCSV(w, ds)
	}
	return legacyMarshalJSON(w, ds)
}

// the row's value in column i: its formatted time, a tag, or a value, nil when the row has none
func (r v3Row) legacyCell(i int) any {
	switch {
	case i == 0:
		return time.Unix(0, int64(r.epoch())).UTC().Format(v3TimestampOutputLayout)
	case i <= len(r.s.tags):
		return r.s.tags[i-1]
	}
	if j, ok := r.valueColumn(i); ok {
		return r.seg.Value(j, r.row)
	}
	return nil
}

func legacyMarshalJSON(w io.Writer, ds *dataset.DataSet) error {
	return legacyWriteRows(w, dataSetRows(ds), '[', ',', "]\n")
}

func legacyMarshalJSONL(w io.Writer, ds *dataset.DataSet) error {
	return legacyWriteRows(w, dataSetRows(ds), 0, '\n', "")
}

// writes rows as JSON objects in column order, which encoding/json's maps would sort, between open
// and closing, with sep after each but the last (JSON) or every one (JSONL)
func legacyWriteRows(w io.Writer, rows []v3Row, open, sep byte, closing string) error {
	if err := legacyCheckRowValues(rows); err != nil {
		return err
	}
	cw := tbytes.NewChunkWriter(w)
	if open != 0 {
		cw.Buf = append(cw.Buf, open)
	}
	for i, row := range rows {
		if i > 0 && open != 0 {
			cw.Buf = append(cw.Buf, sep)
		}
		cw.Buf = appendV3Object(cw.Buf, row)
		if open == 0 {
			cw.Buf = append(cw.Buf, sep)
		}
		cw.FlushIfFull()
	}
	cw.Buf = append(cw.Buf, closing...)
	return cw.Close()
}

// nothing is written when a value can't be, as JSON has no NaN or infinities
func legacyCheckRowValues(rows []v3Row) error {
	for _, row := range rows {
		n := min(row.seg.NumCols(), len(row.s.columns)-1-len(row.s.tags))
		for j := range n {
			if err := row.seg.CheckJSON(j, row.row); err != nil {
				return err
			}
		}
	}
	return nil
}

func legacyMarshalCSV(w io.Writer, ds *dataset.DataSet) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	var lastColumns, record []string
	for _, row := range dataSetRows(ds) {
		// one header row per column layout; series sharing a layout share it
		if !slices.Equal(lastColumns, row.s.columns) {
			if err := cw.Write(row.s.columns); err != nil {
				return err
			}
			lastColumns = row.s.columns
		}
		record = record[:0]
		for i := range row.s.columns {
			record = append(record, legacyFormatValue(row.legacyCell(i)))
		}
		if err := cw.Write(record); err != nil {
			return err
		}
	}
	return nil
}

func legacyFormatValue(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", t)
	}
}
