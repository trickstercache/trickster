package main

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

import (
	"net/http"
	"net/url"
	"sync"

	"github.com/prometheus/common/model"
)

// PrometheusVectorEnvelope represents a Vector response object from the Prometheus HTTP API
type PrometheusVectorEnvelope struct {
	Status string               `json:"status"`
	Data   PrometheusVectorData `json:"data"`
}

// PrometheusVectorData represents the Data body of a Vector response object from the Prometheus HTTP API
type PrometheusVectorData struct {
	ResultType string       `json:"resultType"`
	Result     model.Vector `json:"result"`
}

// PrometheusMatrixEnvelope represents a Matrix response object from the Prometheus HTTP API
type PrometheusMatrixEnvelope struct {
	Status string               `json:"status"`
	Data   PrometheusMatrixData `json:"data"`
}

// PrometheusMatrixData represents the Data body of a Matrix response object from the Prometheus HTTP API
type PrometheusMatrixData struct {
	ResultType string       `json:"resultType"`
	Result     model.Matrix `json:"result"`
}

// ClientRequestContext contains the objects needed to fulfull a client request
type ClientRequestContext struct {
	Request            *http.Request
	Writer             http.ResponseWriter
	CacheKey           string
	CacheLookupResult  string
	Matrix             PrometheusMatrixEnvelope
	Origin             PrometheusOriginConfig
	RequestParams      url.Values
	RequestExtents     MatrixExtents
	OriginUpperExtents MatrixExtents
	OriginLowerExtents MatrixExtents
	StepParam          string
	StepMS             int64
	Time               int64
	WaitGroup          sync.WaitGroup
}

// MatrixExtents describes the start and end epoch times (in ms) for a given range of data
type MatrixExtents struct {
	Start int64
	End   int64
}
