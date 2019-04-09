/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package prometheus

import (
	"encoding/json"
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/prometheus/common/model"
)

// VectorEnvelope represents a Vector response object from the Prometheus HTTP API
type VectorEnvelope struct {
	Status string     `json:"status"`
	Data   VectorData `json:"data"`
}

// VectorData represents the Data body of a Vector response object from the Prometheus HTTP API
type VectorData struct {
	ResultType string       `json:"resultType"`
	Result     model.Vector `json:"result"`
}

// MatrixEnvelope represents a Matrix response object from the Prometheus HTTP API
type MatrixEnvelope struct {
	Status       string              `json:"status"`
	Data         MatrixData          `json:"data"`
	ExtentList   []timeseries.Extent `json:"extents,omitempty"`
	Gaps         []timeseries.Extent `json:"gaps,omitempty"`
	StepDuration time.Duration       `json:"step,omitempty"`
}

// MatrixData represents the Data body of a Matrix response object from the Prometheus HTTP API
type MatrixData struct {
	ResultType string       `json:"resultType"`
	Result     model.Matrix `json:"result"`
}

// MarshalTimeseries ...
func (c *Client) MarshalTimeseries(ts timeseries.Timeseries) ([]byte, error) {
	// Marshal the Envelope back to a json object for Cache Storage
	return json.Marshal(ts)
}

// UnmarshalTimeseries ...
func (c *Client) UnmarshalTimeseries(data []byte) (timeseries.Timeseries, error) {
	me := &MatrixEnvelope{}
	err := json.Unmarshal(data, &me)
	return me, err
}

// UnmarshalInstantaneous ...
func (c *Client) UnmarshalInstantaneous(data []byte) (timeseries.Timeseries, error) {
	ve := &VectorEnvelope{}
	err := json.Unmarshal(data, &ve)
	if err != nil {
		return nil, err
	}
	return ve.ToMatrix(), nil
}

// ToMatrix converts a VectorEnvelope to a MatrixEnvelope
func (ve *VectorEnvelope) ToMatrix() *MatrixEnvelope {
	me := &MatrixEnvelope{}
	me.Status = ve.Status
	me.Data = MatrixData{
		ResultType: "matrix",
		Result:     make(model.Matrix, 0, len(ve.Data.Result)),
	}
	for _, v := range ve.Data.Result {
		v.Timestamp = model.TimeFromUnix(v.Timestamp.Unix()) // Round to nearest Second
		me.Data.Result = append(me.Data.Result, &model.SampleStream{Metric: v.Metric, Values: []model.SamplePair{model.SamplePair{Timestamp: v.Timestamp, Value: v.Value}}})
	}
	return me
}
