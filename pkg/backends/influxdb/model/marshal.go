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
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

var marshalers = map[byte]dataset.Marshaler{
	0: marshalTimeseriesJSON,
	1: marshalTimeseriesJSONPretty,
	2: marshalTimeseriesCSV,
}

// MarshalTimeseries converts a Timeseries into a JSON blob
func MarshalTimeseries(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	err := MarshalTimeseriesWriter(ts, rlo, status, buf)
	return buf.Bytes(), err
}

// MarshalTimeseriesWriter converts a Timeseries into a JSON blob via an io.Writer
func MarshalTimeseriesWriter(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int, w io.Writer) error {
	if ts == nil {
		return timeseries.ErrUnknownFormat
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return timeseries.ErrUnknownFormat
	}
	var of byte
	if rlo != nil {
		of = rlo.OutputFormat
	}
	marshaler, ok := marshalers[of]
	if !ok {
		return timeseries.ErrUnknownFormat
	}
	return marshaler(ds, rlo, status, w)
}

func writeRFC3339Time(w io.Writer, epoch epoch.Epoch, m int64) {
	t := time.Unix(0, int64(epoch))
	w.Write([]byte(`"` + t.Format(time.RFC3339Nano) + `"`))
}

func writeEpochTime(w io.Writer, epoch epoch.Epoch, m int64) {
	w.Write([]byte(strconv.FormatInt(int64(epoch)/m, 10)))
}

func writeValue(w io.Writer, v interface{}, nilVal string) {
	if v == nil {
		w.Write([]byte(nilVal))
	}
	switch t := v.(type) {
	case string:
		w.Write([]byte(`"` + t + `"`))
	case bool:
		w.Write([]byte(strconv.FormatBool(t)))
	case int64:
		w.Write([]byte(strconv.FormatInt(t, 10)))
	case int:
		w.Write([]byte(strconv.Itoa(t)))
	case float64:
		w.Write([]byte(strconv.FormatFloat(t, 'f', -1, 64)))
	}
}

func writeCSVValue(w io.Writer, v interface{}, nilVal string) {
	if v == nil {
		w.Write([]byte(nilVal))
	}
	switch t := v.(type) {
	case string:
		if strings.Contains(t, `"`) {
			t = `"` + strings.Replace(t, `"`, `\"`, -1) + `"`
		} else if strings.Contains(t, " ") || strings.Contains(t, ",") {
			t = `"` + t + `"`
		}
		w.Write([]byte(t))
	case bool:
		w.Write([]byte(strconv.FormatBool(t)))
	case int64:
		w.Write([]byte(strconv.FormatInt(t, 10)))
	case int:
		w.Write([]byte(strconv.Itoa(t)))
	case float64:
		w.Write([]byte(strconv.FormatFloat(t, 'f', -1, 64)))
	}
}

func marshalTimeseriesJSON(ds *dataset.DataSet, rlo *timeseries.RequestOptions, status int, w io.Writer) error {
	if ds == nil {
		return nil
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		h := rw.Header()
		h.Set(headers.NameContentType, headers.ValueApplicationJSON+"; charset=UTF-8")
		rw.WriteHeader(status)
	}
	dw, multiplier := getDateWriter(rlo)

	w.Write([]byte(`{"results":[`))
	lr := len(ds.Results)
	for i := range ds.Results {
		w.Write([]byte(
			fmt.Sprintf(`{"statement_id":%d,"series":[`,
				ds.Results[i].StatementID)))
		ls := len(ds.Results[i].SeriesList)
		for si, s := range ds.Results[i].SeriesList {
			if s == nil {
				continue
			}
			if s.Header.Tags == nil {
				s.Header.Tags = make(dataset.Tags)
			}
			w.Write([]byte(
				fmt.Sprintf(`{"name":"%s","tags":%s,`, s.Header.Name,
					s.Header.Tags.JSON())))
			fl := len(s.Header.FieldsList)
			l := fl + 1
			cols := make([]string, l)
			var j int
			for _, f := range s.Header.FieldsList {
				if j == s.Header.TimestampIndex {
					cols[j] = "time"
					j++
				}
				cols[j] = f.Name
				j++
			}
			w.Write([]byte(
				`"columns":["` + strings.Join(cols, `","`) + `"],"values":[`,
			))
			lp := len(s.Points) - 1
			for j := range s.Points {
				w.Write([]byte("["))
				lv := len(s.Points[j].Values)
				for n, v := range s.Points[j].Values {
					if n == s.Header.TimestampIndex {
						dw(w, s.Points[j].Epoch, multiplier)
						if n < lv {
							w.Write([]byte(","))
						}
						n++
					}
					writeValue(w, v, "null")
					if n < lv {
						w.Write([]byte(","))
					}
				}
				w.Write([]byte("]"))
				if j < lp {
					w.Write([]byte(","))
				}
			}
			w.Write([]byte("]}"))
			if si < ls-1 {
				w.Write([]byte(","))
			}
		}
		w.Write([]byte("]}"))
		if i < lr-1 {
			w.Write([]byte(","))
		}
	}
	w.Write([]byte("]}"))
	return nil
}

func marshalTimeseriesJSONPretty(ds *dataset.DataSet, rlo *timeseries.RequestOptions, status int, w io.Writer) error {

	if ds == nil {
		return nil
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		h := rw.Header()
		h.Set(headers.NameContentType, headers.ValueApplicationJSON+"; charset=UTF-8")
		rw.WriteHeader(status)
	}
	dw, multiplier := getDateWriter(rlo)

	w.Write([]byte("{\n    \"results\": [\n"))
	lr := len(ds.Results)
	for i := range ds.Results {
		w.Write([]byte(
			fmt.Sprintf("        {\n            \"statement_id\": %d,\n            \"series\": [\n",
				ds.Results[i].StatementID)))
		ls := len(ds.Results[i].SeriesList)
		for si, s := range ds.Results[i].SeriesList {
			if s == nil {
				continue
			}
			if s.Header.Tags == nil {
				s.Header.Tags = make(dataset.Tags)
			}
			w.Write([]byte(
				"                {\n" +
					fmt.Sprintf("                    \"name\": \"%s\",\n", s.Header.Name) +
					"                    \"tags\": {\n"))
			for j, k := range s.Header.Tags.Keys() {
				w.Write([]byte(fmt.Sprintf("                        \"%s\": \"%s\"", k, s.Header.Tags[k])))
				if j < len(s.Header.Tags)-1 {
					w.Write([]byte(","))
				}
				w.Write([]byte("\n"))
			}
			w.Write([]byte("                    },\n                    \"columns\": [\n"))
			for j, v := range s.Header.FieldsList {
				if j == s.Header.TimestampIndex {
					w.Write([]byte("                        \"time\""))
					if j < len(s.Header.FieldsList)-1 {
						w.Write([]byte(","))
					}
					w.Write([]byte("\n"))
					j++
				}
				w.Write([]byte("                        \"" + v.Name + "\""))
				if j < len(s.Header.FieldsList)-1 {
					w.Write([]byte(","))
				}
				w.Write([]byte("\n"))
				j++
			}

			fl := len(s.Header.FieldsList)
			l := fl + 1
			cols := make([]string, l)
			var j int
			for _, f := range s.Header.FieldsList {
				if j == s.Header.TimestampIndex {
					cols[j] = "time"
					j++
				}
				cols[j] = f.Name
				j++
			}
			w.Write([]byte("                    ],\n                    \"values\": [\n"))
			lp := len(s.Points) - 1
			for j := range s.Points {
				w.Write([]byte("                        [\n"))
				lv := len(s.Points[j].Values) - 1
				for n, v := range s.Points[j].Values {
					if n == s.Header.TimestampIndex {
						w.Write([]byte("                            "))
						dw(w, s.Points[j].Epoch, multiplier)
						if n < lv {
							w.Write([]byte(","))
						}
						w.Write([]byte("\n"))
						n++
					}
					w.Write([]byte("                            "))
					writeValue(w, v, "null")
					if n < lv {
						w.Write([]byte(","))
					}
					w.Write([]byte("\n"))
				}
				w.Write([]byte("                        ]"))
				if j < lp {
					w.Write([]byte(","))
				}
				w.Write([]byte("\n"))
			}
			w.Write([]byte("                    ]\n                }"))
			if si < ls-1 {
				w.Write([]byte(","))
			}
			w.Write([]byte("\n"))
		}
		w.Write([]byte("            ]"))
		if i < lr-1 {
			w.Write([]byte(","))
		}
		w.Write([]byte("\n"))
	}
	w.Write([]byte("        }\n    ]\n}\n"))
	return nil
}

func marshalTimeseriesCSV(ds *dataset.DataSet, rlo *timeseries.RequestOptions, status int, w io.Writer) error {
	var headerWritten bool
	dw, multiplier := getDateWriter(rlo)
	if ds == nil {
		return nil
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		h := rw.Header()
		h.Set(headers.NameContentType, headers.ValueApplicationCSV+"; charset=UTF-8")
		rw.WriteHeader(status)
	}
	for _, s := range ds.Results[0].SeriesList {
		if s == nil {
			continue
		}
		if !headerWritten {
			l := len(s.Header.FieldsList) + 1
			cols := make([]string, l)
			var j int
			for _, f := range s.Header.FieldsList {
				if j == s.Header.TimestampIndex {
					cols[j] = "time"
					j++
				}
				cols[j] = f.Name
				j++
			}
			w.Write([]byte("name,tags," + strings.Join(cols, ",") + "\n"))
			headerWritten = true
		}
		for _, p := range s.Points {
			w.Write([]byte(s.Header.Name + ",\"" + s.Header.Tags.StringsWithSep("=", ",") + "\","))
			lv := len(p.Values)
			for n, v := range p.Values {
				if n == s.Header.TimestampIndex {
					dw(w, p.Epoch, multiplier)
					if n < lv-1 {
						w.Write([]byte(","))
					}
					n++
				}
				writeCSVValue(w, v, "")
				if n < lv-1 {
					w.Write([]byte{','})
				}
			}
			w.Write([]byte("\n"))
		}
	}
	return nil
}

type dateWriter func(io.Writer, epoch.Epoch, int64)

func getDateWriter(rlo *timeseries.RequestOptions) (dateWriter, int64) {
	var dw dateWriter
	var tf byte
	var multiplier int64
	if rlo != nil {
		tf = rlo.TimeFormat
	}
	switch tf {
	case 0:
		dw = writeRFC3339Time
	default:
		if m, ok := epochMultipliers[tf]; ok {
			multiplier = m
		} else {
			multiplier = 1
		}
		dw = writeEpochTime
	}
	return dw, multiplier
}
