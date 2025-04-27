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
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

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

type tsvWriter struct {
	io.Writer
	writeNames bool
	writeTypes bool
	separator  string
}

type col struct {
	name  string
	val   string
	quote string
}

func (c col) String() string {
	return `			"` + c.name + `": ` + c.quote + c.val + c.quote
}

type cols []col

func (cs cols) String() string {
	var sb = strings.Builder{}
	sb.WriteString("		{\n")
	j := len(cs) - 1
	for i, c := range cs {
		sb.WriteString(c.String())
		if i < j {
			sb.WriteString(",")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("		}")
	return sb.String()
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
	type md struct {
		name  string
		typ   string
		quote bool
	}
	trq := ds.TimeRangeQuery
	if trq == nil {
		return nil, timeseries.ErrNoTimerangeQuery
	}
	var h map[string]string
	if rlo != nil {
		if rlo.VendorData == nil {
			h = make(map[string]string)
			rlo.VendorData = h
		}
	} else {
		h = make(map[string]string)
	}
	h[formatHeader] = "JSON"
	w := new(bytes.Buffer)
	w.Write([]byte(`{
	"meta":
	[`,
	))

	fieldCount := len(trq.TagFieldDefintions) + len(trq.ValueFieldDefinitions)
	mds := make([]md, fieldCount)

	fd := trq.TimestampDefinition

	mds[fd.OutputPosition] = md{
		name:  fd.Name,
		typ:   fd.SDataType,
		quote: shouldQuote(fd.SDataType),
	}

	for _, fd = range trq.TagFieldDefintions {
		if fd.Name == trq.TimestampDefinition.Name {
			continue
		}
		if fd.OutputPosition > fieldCount {
			continue
		}
		mds[fd.OutputPosition] = md{
			name:  fd.Name,
			typ:   fd.SDataType,
			quote: shouldQuote(fd.SDataType),
		}

	}
	for _, fd = range trq.ValueFieldDefinitions {
		if fd.OutputPosition > fieldCount {
			continue
		}
		mds[fd.OutputPosition] = md{
			name:  fd.Name,
			typ:   fd.SDataType,
			quote: shouldQuote(fd.SDataType),
		}
	}
	l := len(mds) - 1
	for i, m := range mds {
		w.Write([]byte(`
		{
			"name": "` + m.name + `",
			"type": "` + m.typ + `"
		}`),
		)
		if i < l {
			w.Write([]byte(","))
		}
	}

	w.Write([]byte(`
	],

	"data":
	[
`,
	))

	var j int64
	var ending = ""
	for _, s := range ds.Results[0].SeriesList {
		for _, p := range s.Points {
			c := make(cols, fieldCount)
			fd := trq.TimestampDefinition
			if fd.OutputPosition > fieldCount {
				continue
			}
			c[fd.OutputPosition] = col{
				name: mds[fd.OutputPosition].name,
				val:  sqlparser.FormatOutputTime(p.Epoch, byte(fd.DataType)),
			}
			if mds[fd.OutputPosition].quote {
				c[fd.OutputPosition].quote = `"`
			}

			var i int
			for _, fd = range trq.TagFieldDefintions {
				if fd.Name == trq.TimestampDefinition.Name {
					continue
				}
				if fd.OutputPosition > fieldCount {
					continue
				}

				c[fd.OutputPosition] = col{
					name: mds[fd.OutputPosition].name,
					val:  s.Header.Tags[fd.Name],
				}
				if mds[fd.OutputPosition].quote {
					c[fd.OutputPosition].quote = `"`
				}
			}

			for i, fd = range trq.ValueFieldDefinitions {
				if fd.Name == trq.TimestampDefinition.Name {
					continue
				}
				if fd.OutputPosition > fieldCount {
					continue
				}
				if i >= len(p.Values) {
					continue
				}
				c[fd.OutputPosition] = col{
					name: mds[fd.OutputPosition].name,
					val:  p.Values[i].(string),
				}
				if mds[fd.OutputPosition].quote {
					c[fd.OutputPosition].quote = `"`
				}
			}

			w.Write([]byte(ending))
			w.Write([]byte(c.String()))
			j++
			ending = ",\n"
		}
	}
	w.Write([]byte(
		`
	],
	
	"rows": `))
	w.Write([]byte(strconv.FormatInt(j, 10)))
	w.Write([]byte("\n}\n"))
	return w.Bytes(), nil
}

func shouldQuote(in string) bool {
	if in == "String" || in == "UInt64" || in == "FixedString" {
		return true
	}
	return false
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

	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			line = line[:len(line)-1]
		}
		if err != nil && (err != io.EOF || line == "") {
			break
		}

		parts := strings.Split(line, "\t")
		lp := len(parts)

		p := dataset.Point{}
		var ps, m int
		var b, c byte
		sh := dataset.SeriesHeader{
			QueryStatement: trq.Statement,
			Tags:           make(dataset.Tags),
		}

		// ensure the correct number of fields are present on this line
		if i != 0 && lp != fieldCount {
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

		// it's a data row. this sort the fields into tags and values,
		m = 0
		p.Values = make([]interface{}, valueCount)
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

	var wg sync.WaitGroup
	wg.Add(len(r.SeriesList))
	for _, s = range r.SeriesList {
		go func(gs *dataset.Series) {
			sort.Sort(gs.Points)
			wg.Done()
		}(s)
	}
	wg.Wait()

	return ds, nil
}
