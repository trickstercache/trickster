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
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/influxdata/influxdb/models"
)

// WFDocument the Wire Format Document for the timeseries
type WFDocument struct {
	Results []WFResult `json:"results"`
	Err     string     `json:"error,omitempty"`
}

// WFResult is the Result section of the WFD
type WFResult struct {
	StatementID int          `json:"statement_id"`
	SeriesList  []models.Row `json:"series,omitempty"`
	Err         string       `json:"error,omitempty"`
}

var epochMultipliers = map[byte]int64{
	1: 1,             // nanoseconds
	2: 1000,          // microseconds
	3: 1000000,       // milliseconds
	4: 1000000000,    // seconds
	5: 60000000000,   // minutes
	6: 3600000000000, // hours
}

// NewModeler returns a collection of modeling functions for influxdb interoperability
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
