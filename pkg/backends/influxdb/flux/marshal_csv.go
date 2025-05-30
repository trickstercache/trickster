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
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

type state struct {
	s, prev    *dataset.Series
	fds        timeseries.FieldDefinitions
	k          int
	e          timeseries.Extent
	w          *csv.Writer
	h, t, g, d bool
}

func marshalTimeseriesCSVWriter(ds *dataset.DataSet, frb *JSONRequestBody,
	status int, w io.Writer) error {
	if hw, ok := w.(http.ResponseWriter); ok {
		hw.Header().Set(headers.NameContentType, headers.ValueApplicationCSV)
		hw.WriteHeader(status)
	}
	st := &state{
		e: ds.TimeRangeQuery.Extent,
		w: csv.NewWriter(w),
	}
	for _, s := range frb.Dialect.Annotations {
		switch s {
		case AnnotationDatatype:
			st.t = true
		case AnnotationGroup:
			st.g = true
		case AnnotationDefault:
			st.d = true
		}
	}
	st.h = frb.Dialect.Header == nil || *frb.Dialect.Header
	for _, r := range ds.Results {
		for _, s := range r.SeriesList {
			st.s = s
			processCsvSeriesData(st)
			st.k++
		}
	}
	st.w.Flush()
	return nil
}

func printCsvDatatypeAnnotationRow(w *csv.Writer,
	fds timeseries.FieldDefinitions) error {
	cells := make([]string, len(fds))
	for i, fd := range fds {
		if i == 0 {
			cells[i] = "#datatype"
			continue
		}
		cells[i] = fd.SDataType
	}
	return w.Write(cells)
}

func printCsvGroupAnnotationRow(w *csv.Writer,
	fds timeseries.FieldDefinitions) error {
	cells := make([]string, len(fds))
	for i, fd := range fds {
		if i == 0 {
			cells[i] = "#group"
			continue
		}
		if (fd.Role == timeseries.RoleTag && fd.Name != resultColumnName) ||
			fd.Name == startColumnName ||
			fd.Name == stopColumnName {
			cells[i] = sTrue
		} else {
			cells[i] = sFalse
		}
	}
	return w.Write(cells)
}

func printCsvDefaultAnnotationRow(w *csv.Writer,
	fds timeseries.FieldDefinitions) error {
	cells := make([]string, len(fds))
	for i, fd := range fds {
		if i == 0 {
			cells[i] = "#default"
			continue
		}
		cells[i] = fd.DefaultValue
	}
	return w.Write(cells)
}

func printCsvHeaderRow(w *csv.Writer, fds timeseries.FieldDefinitions) error {
	cells := make([]string, len(fds))
	for i, fd := range fds {
		if i == 0 {
			continue
		}
		cells[i] = fd.Name
	}
	return w.Write(cells)
}

func processSeriesHeader(st *state) {
	if st == nil || st.s == nil {
		return
	}
	st.fds = st.s.Header.FieldDefinitions()
	if st.prev == nil { // TODO: also if schema has changed between s and prev
		if st.t {
			if err := printCsvDatatypeAnnotationRow(st.w, st.fds); err != nil {
				logger.Error("failed to write csv datatype annotation row",
					logging.Pairs{"error": err})
			}
		}
		if st.g {
			if err := printCsvGroupAnnotationRow(st.w, st.fds); err != nil {
				logger.Error("failed to write csv group annotation row",
					logging.Pairs{"error": err})
			}
		}
		if st.d {
			if err := printCsvDefaultAnnotationRow(st.w, st.fds); err != nil {
				logger.Error("failed to write csv group annotation row",
					logging.Pairs{"error": err})
			}
		}
		if st.h {
			if err := printCsvHeaderRow(st.w, st.fds); err != nil {
				logger.Error("failed to write csv header row",
					logging.Pairs{"error": err})
			}
		}
	}
	setStartStopTimes(st.fds, st.e)
	st.prev = st.s
}

func processCsvSeriesData(st *state) {
	processSeriesHeader(st)
	for _, p := range st.s.Points {
		if err := processCsvRowData(st, p); err != nil {
			logger.Error("failed to write csv data row",
				logging.Pairs{"error": err})
		}
	}
}

func processCsvRowData(st *state, p dataset.Point) error {
	row := make([]string, len(st.fds))
	var o int
	for _, fd := range st.fds {
		s, usedVal := getCsvCellValue(st.s.Header, fd, p, o, st.k)
		if usedVal {
			o++
		}
		row[fd.OutputPosition] = s
	}
	return st.w.Write(row)
}

func getCsvCellValue(sh dataset.SeriesHeader, fd timeseries.FieldDefinition,
	c dataset.Point, nextValue, table int) (string, bool) {
	switch fd.Role {
	case timeseries.RoleTimestamp:
		return fmt.Sprintf("%v", getFormattedTimestamp(c.Epoch, fd)), false
	case timeseries.RoleTag:
		return sh.Tags[fd.Name], false
	case timeseries.RoleValue:
		if nextValue < len(c.Values) {
			if c.Values[nextValue] == nil {
				return fd.DefaultValue, true
			} else if s, ok := c.Values[nextValue].(string); ok && s == "" {
				return fd.DefaultValue, true
			}
			return fmt.Sprintf("%v", c.Values[nextValue]), true
		}
	case timeseries.RoleUntracked:
		switch fd.Name {
		case tableColumnName:
			return strconv.Itoa(table), false
		case startColumnName, stopColumnName:
			return fd.DefaultValue, false
		}
	}
	return fd.DefaultValue, false
}
