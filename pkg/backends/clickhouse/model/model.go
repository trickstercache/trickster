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

package model

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/sqlparser"
)

const formatHeader = "X-Clickhouse-Format"

var marshalers = map[byte]dataset.Marshaler{
	0: marshalTimeseriesJSON,
	1: marshalTimeseriesCSV,
	2: marshalTimeseriesCSVWithNames,
	3: marshalTimeseriesTSV,
	4: marshalTimeseriesTSVWithNames,
	5: marshalTimeseriesTSVWithNamesAndTypes,
}

type WFDocument struct {
	Meta WFMeta `json:"meta"`
	Data WFData `json:"data"`
	Rows *int   `json:"rows"`
}

type WFMeta []WFMetaItem
type WFMetaItem struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

type WFDataItemElement struct {
	Key   string
	Value string
}

type WFDataItem []WFDataItemElement

type WFData []WFDataItem

type tsvWriter struct {
	io.Writer
	writeNames bool
	writeTypes bool
	separator  string
}

// NewModeler returns a collection of modeling functions for clickhouse interoperability
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

func marshalTimeseriesJSON(ds *dataset.DataSet, rlo *timeseries.RequestOptions,
	status int) ([]byte, error) {
	wf, err := toWireFormat(ds)
	if err != nil {
		logger.Error("failed to convert dataset to clickhouse wire format",
			logging.Pairs{"error": err})
	}
	if rlo != nil {
		if rlo.VendorData == nil {
			rlo.VendorData = make(map[string]string)
		}
		rlo.VendorData[formatHeader] = "JSON"
	}
	return json.Marshal(wf)
}

func marshalTimeseriesCSV(ds *dataset.DataSet, rlo *timeseries.RequestOptions,
	status int) ([]byte, error) {
	w := new(bytes.Buffer)
	err := marshalTimeseriesXSV(ds, rlo, &tsvWriter{Writer: w, separator: ","})
	if err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

func marshalTimeseriesCSVWithNames(ds *dataset.DataSet,
	rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	w := new(bytes.Buffer)
	err := marshalTimeseriesXSV(ds, rlo,
		&tsvWriter{Writer: w, writeNames: true, separator: ","})
	if err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

func marshalTimeseriesTSV(ds *dataset.DataSet, rlo *timeseries.RequestOptions,
	status int) ([]byte, error) {
	w := new(bytes.Buffer)
	err := marshalTimeseriesXSV(ds, rlo, &tsvWriter{Writer: w, separator: "\t"})
	if err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

func marshalTimeseriesTSVWithNames(ds *dataset.DataSet,
	rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	w := new(bytes.Buffer)
	err := marshalTimeseriesXSV(ds, rlo,
		&tsvWriter{Writer: w, writeNames: true, separator: "\t"})
	if err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

func marshalTimeseriesTSVWithNamesAndTypes(ds *dataset.DataSet,
	rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	w := new(bytes.Buffer)
	err := marshalTimeseriesXSV(ds, rlo,
		&tsvWriter{Writer: w, writeNames: true, writeTypes: true, separator: "\t"})
	if err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

func marshalTimeseriesXSV(ds *dataset.DataSet, rlo *timeseries.RequestOptions,
	tw *tsvWriter) error {
	trq := ds.TimeRangeQuery
	if trq == nil {
		return timeseries.ErrNoTimerangeQuery
	}

	var h map[string]string
	if rlo != nil {
		if rlo.VendorData == nil {
			rlo.VendorData = make(map[string]string)
		}
		h = rlo.VendorData
	}

	if tw.separator == "" {
		tw.separator = ","
	}

	var ctPart, fmtPart string
	switch tw.separator {
	case "\t":
		ctPart = "tab"
		fmtPart = "TSV"
	default:
		ctPart = "comma"
		fmtPart = "CSV"
		tw.separator = ","
	}
	h[headers.NameContentType] = "text/" + ctPart + "-separated-values; charset=UTF-8"
	switch {
	case tw.writeTypes:
		h[formatHeader] = fmtPart + "WithNamesAndTypes"
	case tw.writeNames:
		h[formatHeader] = fmtPart + "WithNames"
	default:
		h[formatHeader] = fmtPart
	}

	if len(trq.TagFieldDefintions) == 0 &&
		len(trq.ValueFieldDefinitions) == 0 {
		return timeseries.ErrNoTimerangeQuery
	}

	if len(ds.Results) == 0 {
		return nil
	}

	fieldCount := len(trq.TagFieldDefintions) + len(trq.ValueFieldDefinitions)
	if trq.TimestampDefinition.OutputPosition > fieldCount {
		return timeseries.ErrTableHeader
	}

	lookup := make(map[string]timeseries.FieldDefinition)
	for _, fd := range trq.ValueFieldDefinitions {
		lookup[fd.Name] = fd
	}

	var i int
	if tw.writeNames || tw.writeTypes {
		rowVals := make([]string, fieldCount)
		fd := trq.TimestampDefinition
		rowVals[fd.OutputPosition] = fd.Name
		for _, fd = range trq.TagFieldDefintions {
			if fd.Name == trq.TimestampDefinition.Name {
				continue
			}
			if fd.OutputPosition > fieldCount {
				continue
			}
			rowVals[fd.OutputPosition] = fd.Name
		}
		for _, fd = range trq.ValueFieldDefinitions {
			rowVals[fd.OutputPosition] = fd.Name
		}
		tw.Write([]byte(strings.Join(rowVals, tw.separator) + "\n"))
	}

	if tw.writeTypes {
		rowVals := make([]string, fieldCount)
		fd := trq.TimestampDefinition
		rowVals[fd.OutputPosition] = fd.SDataType
		for _, fd = range trq.TagFieldDefintions {
			if fd.Name == trq.TimestampDefinition.Name {
				continue
			}
			if fd.OutputPosition > fieldCount {
				continue
			}
			rowVals[fd.OutputPosition] = fd.SDataType
		}
		for _, fd = range trq.ValueFieldDefinitions {
			rowVals[fd.OutputPosition] = fd.SDataType
		}
		tw.Write([]byte(strings.Join(rowVals, tw.separator) + "\n"))
	}

	for _, s := range ds.Results[0].SeriesList {
		for _, p := range s.Points {
			rowVals := make([]string, fieldCount)
			fd := trq.TimestampDefinition
			rowVals[fd.OutputPosition] = sqlparser.FormatOutputTime(p.Epoch, byte(fd.DataType))
			for _, fd = range trq.TagFieldDefintions {
				if fd.Name == trq.TimestampDefinition.Name {
					continue
				}
				if fd.OutputPosition > fieldCount {
					continue
				}
				rowVals[fd.OutputPosition] = wrapCSVCell(s.Header.Tags[fd.Name], tw.separator)
			}
			for i, fd = range trq.ValueFieldDefinitions {
				if i >= len(p.Values) {
					continue
				}
				rowVals[fd.OutputPosition] = wrapCSVCell(p.Values[i].(string), tw.separator)
			}
			tw.Write([]byte(strings.Join(rowVals, tw.separator) + "\n"))
		}
	}
	return nil
}

func wrapCSVCell(in, separator string) string {
	if strings.Contains(in, separator) ||
		(separator == "," && strings.Contains(in, " ")) {
		return `"` + in + `"`
	}
	return in
}

// MarshalTimeseries converts a Timeseries into a JSON blob
func MarshalTimeseries(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	if ts == nil {
		return nil, timeseries.ErrUnknownFormat
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return nil, timeseries.ErrUnknownFormat
	}
	var of byte
	if rlo != nil {
		of = rlo.OutputFormat
	}
	marshaler, ok := marshalers[of]
	if !ok {
		return nil, timeseries.ErrUnknownFormat
	}
	return marshaler(ds, rlo, status)
}

// MarshalTimeseriesWriter converts a Timeseries into a JSON blob via an io.Writer
func MarshalTimeseriesWriter(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, status int, w io.Writer) error {
	b, err := MarshalTimeseries(ts, rlo, status)
	if err != nil {
		return err
	}
	var of byte
	if rlo != nil {
		of = rlo.OutputFormat
	}

	var h http.Header
	if rlo != nil && len(rlo.VendorData) > 0 {
		h = make(http.Header)
		for k, v := range rlo.VendorData {
			h.Set(k, v)
		}
	}
	err = response.WriteResponseHeader(w, status, of, h)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// UnmarshalTimeseries converts a TSV blob into a Timeseries
func UnmarshalTimeseries(data []byte, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
	buf := bytes.NewReader(data)
	return UnmarshalTimeseriesReader(buf, trq)
}

// UnmarshalTimeseriesReader converts a TSV blob into a Timeseries via io.Reader
func UnmarshalTimeseriesReader(reader io.Reader, trq *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {

	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}

	// ClickHouse doesn't support multi-statements, so there is always exactly 1 result element
	r := &dataset.Result{
		SeriesList: make([]*dataset.Series, 0, 8),
	}
	ds := &dataset.DataSet{
		TimeRangeQuery: trq,
		ExtentList:     timeseries.ExtentList{trq.Extent},
		Results:        []*dataset.Result{r},
	}
	sl := dataset.SeriesLookup{}
	var s *dataset.Series
	var key dataset.SeriesLookupKey
	var i, fieldCount, valueCount, tsi int
	var ok bool
	var err error

	tfi := make(map[int]int)
	vfi := make(map[int]int)

	br := bufio.NewReader(reader)

	// ClickHouse returns a TSV, which gets converted directly into a DataSet
	for {
		// this iterates the TSV line-by-line until the end is reached
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			line = line[:len(line)-1]
		}
		if err != nil && (err != io.EOF) {
			break
		}

		// this splits the line into cells
		parts := strings.Split(line, "\t")

		p := dataset.Point{}
		var ps, m int
		var b, c byte
		sh := dataset.SeriesHeader{
			QueryStatement: trq.Statement,
			Tags:           make(dataset.Tags),
		}

		// ensure the correct number of fields are present on this line
		if i != 0 && len(parts) != fieldCount {
			if i == 1 {
				return nil, timeseries.ErrTableHeader
			}
			goto nextIteration
		}

		// special handling for the 2 header rows (field names and types)
		if i < 2 {
			if i == 0 {
				// line 0 is the Field Names line, so its column count is authoritative
				fieldCount = len(parts)

				// iterate the line parts to get value vs tag fields defined
				for j, name := range parts {
					if name == "" {
						goto nextPart
					}
					if trq.TimestampDefinition.Name == name {
						trq.TimestampDefinition.OutputPosition = j
						goto nextPart
					}
					for l := range trq.TagFieldDefintions {
						if trq.TagFieldDefintions[l].Name == name {
							trq.TagFieldDefintions[l].OutputPosition = j
							tfi[j] = l
							goto nextPart
						}
					}

					if trq.ValueFieldDefinitions == nil {
						trq.ValueFieldDefinitions = make([]timeseries.FieldDefinition, 0, 8)
					}

					vfi[j] = len(trq.ValueFieldDefinitions)

					trq.ValueFieldDefinitions = append(trq.ValueFieldDefinitions,
						timeseries.FieldDefinition{
							OutputPosition: j,
							Name:           name,
						},
					)
					valueCount++
					goto nextPart

				nextPart:
				}
				goto nextIteration
			}
			// line 1 is the Field Types line
			for j, dt := range parts {
				if tsi == j {
					trq.TimestampDefinition.SDataType = dt
					continue
				}
				if l, ok := vfi[j]; ok {
					trq.ValueFieldDefinitions[l].SDataType = dt
					continue
				}
				if l, ok := tfi[j]; ok {
					trq.TagFieldDefintions[l].SDataType = dt
				}
			}
			goto nextIteration
		}

		// it's a data row. this sorts the fields into tags and values,
		m = 0
		p.Values = make([]any, valueCount)
		for j, val := range parts {
			if tsi == j {
				p.Epoch, c, _ = sqlparser.ParseEpoch(val)
				if c > 0 && b == 0 {
					b = c
				}
				continue
			}
			if l, ok := tfi[j]; ok {
				sh.Tags[trq.TagFieldDefintions[l].Name] = val
				continue
			}
			p.Values[m] = val
			ps += len(val)
			m++
		}
		if trq.TimestampDefinition.DataType == 0 && b > 0 {
			trq.TimestampDefinition.DataType = timeseries.FieldDataType(b)
		}

		// this determines if the series is already defined and creates if it is not
		sh.CalculateSize()
		key.Hash = sh.CalculateHash()
		if s, ok = sl[key]; !ok {
			s = &dataset.Series{
				Header:    sh,
				Points:    make(dataset.Points, 0, 512),
				PointSize: int64(ps),
			}
			sl[key] = s
			r.SeriesList = append(r.SeriesList, s)
		}
		s.Points = append(s.Points, p)

	nextIteration:
		if err == io.EOF {
			break
		}
		i++
	}
	if err != nil && err != io.EOF {
		return nil, err
	}

	r.SeriesList.SortPoints()

	return ds, nil
}

func toWireFormat(ds *dataset.DataSet) (*WFDocument, error) {

	trq := ds.TimeRangeQuery
	tsfd := trq.TimestampDefinition

	d := &WFDocument{}

	// metadata
	fieldCount := len(trq.TagFieldDefintions) +
		len(trq.ValueFieldDefinitions)
	d.Meta = make(WFMeta, fieldCount)
	d.Meta[tsfd.OutputPosition] = WFMetaItem{
		Name: tsfd.Name,
		Type: tsfd.SDataType,
	}

	for _, fd := range trq.TagFieldDefintions {
		if fd.Name == trq.TimestampDefinition.Name {
			continue
		}
		if fd.OutputPosition > fieldCount {
			continue
		}
		d.Meta[fd.OutputPosition] = WFMetaItem{
			Name: fd.Name,
			Type: fd.SDataType,
		}

	}
	for _, fd := range trq.ValueFieldDefinitions {
		if fd.OutputPosition > fieldCount {
			continue
		}
		d.Meta[fd.OutputPosition] = WFMetaItem{
			Name: fd.Name,
			Type: fd.SDataType,
		}
	}

	// timeseries data
	var maxRowCount, k int
	if len(ds.Results) == 0 {
		d.Rows = &k
		return d, nil
	}

	for _, s := range ds.Results[0].SeriesList {
		maxRowCount += len(s.Points)
	}

	data := make(WFData, maxRowCount)
	for _, s := range ds.Results[0].SeriesList {
		for _, p := range s.Points {
			item := make(WFDataItem, fieldCount)
			if tsfd.OutputPosition > fieldCount {
				continue
			}
			item[tsfd.OutputPosition] = WFDataItemElement{
				Key:   d.Meta[tsfd.OutputPosition].Name,
				Value: sqlparser.FormatOutputTime(p.Epoch, byte(tsfd.DataType)),
			}

			for _, fd := range trq.TagFieldDefintions {
				if fd.Name == trq.TimestampDefinition.Name {
					continue
				}
				if fd.OutputPosition > fieldCount {
					continue
				}

				item[fd.OutputPosition] = WFDataItemElement{
					Key:   d.Meta[fd.OutputPosition].Name,
					Value: s.Header.Tags[fd.Name],
				}
			}

			for i, fd := range trq.ValueFieldDefinitions {
				if fd.Name == trq.TimestampDefinition.Name {
					continue
				}
				if fd.OutputPosition > fieldCount {
					continue
				}
				if i >= len(p.Values) {
					continue
				}
				item[fd.OutputPosition] = WFDataItemElement{
					Key:   d.Meta[fd.OutputPosition].Name,
					Value: p.Values[i].(string),
				}
			}
			var j int
			for i := range item {
				if item[i].Key == "" {
					continue
				}
				item[j] = item[i]
				j++
			}
			data[k] = item[:j]
			k++
		}
	}

	d.Data = data[:k]
	d.Rows = &k
	return d, nil
}

func (d WFDataItem) MarshalJSON() ([]byte, error) {
	buf := bytes.NewBuffer([]byte{'{'})
	var sep bool
	for _, e := range d {
		if sep {
			buf.Write([]byte{','})
		}
		buf.WriteString(fmt.Sprintf(`"%s":"%s"`, e.Key, e.Value))
		sep = true
	}
	buf.Write([]byte{'}'})
	return buf.Bytes(), nil
}
