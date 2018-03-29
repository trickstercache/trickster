package main

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
