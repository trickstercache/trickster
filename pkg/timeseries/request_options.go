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

package timeseries

import (
	"strings"
)

// RequestOptions holds request-specific information about a query
type RequestOptions struct {
	// TimeFormat is a field usable by time series implementations to pass data between the parsed time range query
	// and the data unmarshaler/marshaler to give indications about the format of Timestamps in the serialized dataset
	TimeFormat byte
	// OutputFormat is a field usable by time series implementations to pass data between the parsed time range query
	// and the data unmarshaler/marshaler to give indications about the content type of the serialized output
	OutputFormat byte
	// FastForwardDisable indicates whether the Time Range Query result should include fast forward data
	FastForwardDisable bool
	// BaseTimestampFieldName holds the name of the Base Timestamp Field (in case it is aliased with AS) to help
	// parse WHERE clauses during the initial parsing of a query
	BaseTimestampFieldName string
}

// ExtractFastForwardDisabled will look for the FastForwardUserDisableFlag in the provided string
// and set the flag appropriately in the subject RequestOptions
func (ro *RequestOptions) ExtractFastForwardDisabled(input string) {
	ro.FastForwardDisable = strings.Contains(input, FastForwardUserDisableFlag)
}
