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

package stream_test

import (
	"encoding/json/jsontext"
	"errors"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// The decoders below show how providers use the stream package and Builder.

var testTRQ = &timeseries.TimeRangeQuery{
	Statement: "q",
	Extent:    timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(10, 0)},
	Step:      time.Second,
}

var rowFields = timeseries.SeriesFields{
	Timestamp: timeseries.FieldDefinition{Name: "time", DataType: timeseries.DateTimeUnixMilli,
		Role: timeseries.RoleTimestamp},
	Tags: timeseries.FieldDefinitions{{Name: "host", DataType: timeseries.String,
		Role: timeseries.RoleTag, OutputPosition: 1}},
	Values: timeseries.FieldDefinitions{{Name: "value", DataType: timeseries.Float64,
		Role: timeseries.RoleValue, OutputPosition: 2}},
}

var (
	errBadHeader   = errors.New("bad header")
	errMetricFirst = errors.New("metric must precede values")
)

func newTSVDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	// decodes "time\thost\tvalue" rows that may arrive in any order
	b := dataset.NewBuilder(trq, dataset.BuilderOptions{Fields: rowFields, SeriesName: "tsv",
		QueryStatement: trq.Statement, Duplicates: dataset.DuplicatesError})
	var header bool
	var cols [][]byte
	onLine := func(line []byte) error {
		if !header {
			if string(line) != "time\thost\tvalue" {
				return errBadHeader
			}
			header = true
			return nil
		}
		cols = stream.SplitFields(line, '\t', cols)
		if len(cols) != 3 {
			return timeseries.ErrInvalidBody
		}
		ep, err := epoch.ParseDecimal(cols[0], timeseries.DateTimeUnixMilli)
		if err != nil {
			return err
		}
		v, err := stream.ParseValue(cols[2], timeseries.Float64)
		if err != nil {
			return err
		}
		r := b.Row()
		r.SetEpoch(ep)
		r.SetTag(0, cols[1])
		r.AddValue(v)
		return r.Commit()
	}
	return stream.NewLines(onLine, func() (timeseries.Timeseries, error) {
		if !header {
			return nil, errBadHeader
		}
		return b.Finish()
	}), nil
}

func newMatrixDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	// decodes a Prometheus-style matrix, one series at a time
	b := dataset.NewBuilder(trq, dataset.BuilderOptions{Duplicates: dataset.DuplicatesError})
	valueFields := timeseries.FieldDefinitions{{Name: "value", DataType: timeseries.Float64,
		Role: timeseries.RoleValue}}
	var status string
	series := func(dec *jsontext.Decoder) error {
		defer b.EndSeries()
		var open bool
		return stream.Object(dec, func(key string) error {
			switch key {
			case "metric":
				var tags dataset.Tags
				if err := stream.Decode(dec, &tags); err != nil {
					return err
				}
				b.StartSeries(dataset.SeriesHeader{Name: "matrix", Tags: tags, ValueFieldsList: valueFields})
				open = true
				return nil
			case "values":
				if !open {
					return errMetricFirst
				}
				return stream.Array(dec, func() error {
					// each [time, "value"] pair is read an element at a time, as raw bytes valid
					// only until the next read, so nothing is copied that the row doesn't keep
					r := b.Row()
					n := 0
					err := stream.Array(dec, func() error {
						raw, err := dec.ReadValue()
						if err != nil {
							return err
						}
						switch n++; n {
						case 1:
							ep, err := epoch.ParseDecimal(raw, timeseries.DateTimeUnixSecs)
							r.SetEpoch(ep)
							return err
						case 2:
							v, err := stream.ParseJSONValue(raw, timeseries.Float64)
							r.AddValue(v)
							return err
						}
						return timeseries.ErrInvalidBody
					})
					if err != nil || n != 2 {
						return errors.Join(err, timeseries.ErrInvalidBody)
					}
					return r.Commit()
				})
			}
			return stream.Skip(dec)
		})
	}
	walk := func(dec *jsontext.Decoder) error {
		return stream.Object(dec, func(key string) error {
			switch key {
			case "status":
				return stream.Decode(dec, &status)
			case "data":
				return stream.Object(dec, func(key string) error {
					if key != "result" {
						return stream.Skip(dec)
					}
					return stream.Array(dec, func() error { return series(dec) })
				})
			}
			return stream.Skip(dec)
		})
	}
	return stream.NewJSON(walk, func() (timeseries.Timeseries, error) {
		ds, err := b.Finish()
		if err != nil {
			return nil, err
		}
		ds.Status = status
		return ds, nil
	}), nil
}

func newRowsDecoder(trq *timeseries.TimeRangeQuery) (stream.Decoder, error) {
	// decodes {"rows":[[time,host,value],...],"total":n} rows that may arrive in
	// any order, checking the trailing total once all rows are read
	b := dataset.NewBuilder(trq, dataset.BuilderOptions{Fields: rowFields, SeriesName: "rows",
		TagString: stream.JSONTagString, SortSeries: true})
	var count, total int
	walk := func(dec *jsontext.Decoder) error {
		return stream.Object(dec, func(key string) error {
			switch key {
			case "rows":
				return stream.Array(dec, func() error {
					r := b.Row()
					n := 0
					err := stream.Array(dec, func() error {
						raw, err := dec.ReadValue()
						if err != nil {
							return err
						}
						switch n++; n {
						case 1:
							ep, err := epoch.ParseDecimal(raw, timeseries.DateTimeUnixMilli)
							r.SetEpoch(ep)
							return err
						case 2:
							// the tag's bytes are copied
							r.SetTag(0, raw)
							return nil
						case 3:
							v, err := stream.ParseJSONValue(raw, timeseries.Float64)
							r.AddValue(v)
							return err
						}
						return timeseries.ErrInvalidBody
					})
					if err != nil || n != 3 {
						return errors.Join(err, timeseries.ErrInvalidBody)
					}
					count++
					return r.Commit()
				})
			case "total":
				return stream.Decode(dec, &total)
			}
			return stream.Skip(dec)
		})
	}
	return stream.NewJSON(walk, func() (timeseries.Timeseries, error) {
		if count != total {
			return nil, timeseries.ErrInvalidBody
		}
		return b.Finish()
	}), nil
}
