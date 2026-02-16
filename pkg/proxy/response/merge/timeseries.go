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

package merge

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// TimeseriesMergeFunc creates a MergeFunc for timeseries data
// The returned function accepts a timeseries.Timeseries and merges it into the accumulator
func TimeseriesMergeFunc(unmarshaler timeseries.UnmarshalerFunc) MergeFunc {
	return func(accum *Accumulator, data any, idx int) error {
		ts, ok := data.(timeseries.Timeseries)
		if !ok {
			// If data is []byte, unmarshal it first (for backward compatibility during transition)
			body, ok := data.([]byte)
			if !ok {
				// Not a timeseries and not []byte
				return nil
			}
			var err error
			ts, err = unmarshaler(body, nil)
			if err != nil {
				return err
			}
		}
		accum.mu.Lock()
		defer accum.mu.Unlock()
		if accum.tsdata == nil {
			accum.tsdata = ts
		} else {
			accum.tsdata.Merge(false, ts)
		}
		return nil
	}
}

// TimeseriesMergeFuncFromBytes creates a MergeFunc that accepts []byte and unmarshals it
// This is a convenience function for call sites that still have []byte
func TimeseriesMergeFuncFromBytes(unmarshaler timeseries.UnmarshalerFunc) func(*Accumulator, []byte, int) error {
	mergeFunc := TimeseriesMergeFunc(unmarshaler)
	return func(accum *Accumulator, body []byte, idx int) error {
		ts, err := unmarshaler(body, nil)
		if err != nil {
			return err
		}
		return mergeFunc(accum, ts, idx)
	}
}

// TimeseriesRespondFunc creates a RespondFunc for timeseries data
// It writes the merged timeseries using the marshaler
func TimeseriesRespondFunc(marshaler timeseries.MarshalWriterFunc, requestOptions *timeseries.RequestOptions) RespondFunc {
	return func(w http.ResponseWriter, r *http.Request, accum *Accumulator, statusCode int) {
		accum.mu.Lock()
		ts := accum.tsdata
		accum.mu.Unlock()
		if ts == nil {
			failures.HandleBadGateway(w, r)
			return
		}
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		headers.StripMergeHeaders(w.Header())
		marshaler(ts, requestOptions, statusCode, w)
	}
}
