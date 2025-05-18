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
)

const formatHeader = "X-Clickhouse-Format"

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

// WFDocument is represents the Wire Format structure for ClickHouse
type WFDocument struct {
	Meta WFMeta `json:"meta"`
	Data WFData `json:"data"`
	Rows *int   `json:"rows"`
}

// WFMetaItem is a metadata attribute in the Wire Format Document
type WFMetaItem struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

// WFMeta is a slice of Metadata items in the Wire Format Document
type WFMeta []WFMetaItem

// WFDataItemElement represents a data point and its value in the response.
type WFDataItemElement struct {
	Key   string
	Value string
}

// WFDataItem is a slice of WFDataItemElements (aka a Row) in the WF Document
type WFDataItem []WFDataItemElement

// WFData is the collection of data rows in the document
type WFData []WFDataItem
