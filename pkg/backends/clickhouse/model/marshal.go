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
	"io"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// MarshalTimeseries converts a Timeseries into a JSON blob
func MarshalTimeseries(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int) ([]byte, error) {
	w := getMarshalBuf()
	err := MarshalTimeseriesWriter(ts, rlo, status, w)
	if err != nil {
		putMarshalBuf(w)
		return nil, err
	}
	b := append([]byte(nil), w.Bytes()...)
	putMarshalBuf(w)
	return b, nil
}

// MarshalTimeseriesWriter converts a Timeseries into a JSON blob via an io.Writer
func MarshalTimeseriesWriter(ts timeseries.Timeseries,
	rlo *timeseries.RequestOptions, status int, w io.Writer,
) error {
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
	switch of {
	case 0:
		return marshalTimeseriesJSON(w, ds, rlo, status)
	case 1:
		return marshalTimeseriesXSV(w, ds, rlo, false, false, ',')
	case 2:
		return marshalTimeseriesXSV(w, ds, rlo, true, false, ',')
	case 3:
		return marshalTimeseriesXSV(w, ds, rlo, false, false, '\t')
	case 4:
		return marshalTimeseriesXSV(w, ds, rlo, true, false, '\t')
	case 5:
		return marshalTimeseriesXSV(w, ds, rlo, true, true, '\t')
	}
	return timeseries.ErrUnknownFormat
}
