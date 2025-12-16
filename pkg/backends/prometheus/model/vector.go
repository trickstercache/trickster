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
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// MergeAndWriteVectorMergeFunc returns a MergeFunc for Vector (timeseries)
func MergeAndWriteVectorMergeFunc(unmarshaler timeseries.UnmarshalerFunc) merge.MergeFunc {
	return merge.TimeseriesMergeFunc(unmarshaler)
}

// MergeAndWriteVectorRespondFunc returns a RespondFunc for Vector (timeseries)
func MergeAndWriteVectorRespondFunc(marshaler timeseries.MarshalWriterFunc) merge.RespondFunc {
	return func(w http.ResponseWriter, r *http.Request, accum *merge.Accumulator, statusCode int) {
		ts := accum.GetTSData()
		if ts == nil {
			failures.HandleBadGateway(w, r)
			return
		}

		if statusCode == 0 {
			statusCode = http.StatusOK
		}

		MarshalTSOrVectorWriter(ts, nil, statusCode, w, true) //revive:disable:unhandled-error
	}
}
