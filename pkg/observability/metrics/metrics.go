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

// Package metrics implements prometheus metrics and exposes the metrics HTTP listener
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	metricNamespace   = "trickster"
	cacheSubsystem    = "cache"
	proxySubsystem    = "proxy"
	configSubsystem   = "config"
	buildSubsystem    = "build"
	frontendSubsystem = "frontend"
)

// Default histogram buckets used by trickster
var (
	defaultBuckets = []float64{0.05, 0.1, 0.5, 1, 5, 10, 20}
)

// BuildInfo is a Gauge representing the Trickster binary build information of the running server instance
var BuildInfo *prometheus.GaugeVec

// LastReloadSuccessful gauge will be set to 1 if Trickster's last config reload succeeded else 0
var LastReloadSuccessful prometheus.Gauge

// LastReloadSuccessfulTimestamp gauge is the epoch time of the most recent successful config load
var LastReloadSuccessfulTimestamp prometheus.Gauge

// FrontendRequestStatus is a Counter of front end requests that have been processed with their status
var FrontendRequestStatus *prometheus.CounterVec

// FrontendRequestDuration is a histogram that tracks the time it takes to process a request
var FrontendRequestDuration *prometheus.HistogramVec

// FrontendRequestWrittenBytes is a Counter of bytes written for front end requests
var FrontendRequestWrittenBytes *prometheus.CounterVec

// ProxyRequestStatus is a Counter of downstream client requests handled by Trickster
var ProxyRequestStatus *prometheus.CounterVec

// ProxyRequestElements is a Counter of data points in the timeseries returned to the requesting client
var ProxyRequestElements *prometheus.CounterVec

// ProxyRequestDuration is a Histogram of time required in seconds to proxy a given Prometheus query
var ProxyRequestDuration *prometheus.HistogramVec

// CacheObjectOperations is a Counter of operations (in # of objects) performed on a Trickster cache
var CacheObjectOperations *prometheus.CounterVec

// CacheByteOperations is a Counter of operations (in # of bytes) performed on a Trickster cache
var CacheByteOperations *prometheus.CounterVec

// CacheEvents is a Counter of events performed on a Trickster cache
var CacheEvents *prometheus.CounterVec

// CacheObjects is a Gauge representing the number of objects in a Trickster cache
var CacheObjects *prometheus.GaugeVec

// CacheBytes is a Gauge representing the number of bytes in a Trickster cache
var CacheBytes *prometheus.GaugeVec

// CacheMaxObjects is a Gauge for the Trickster cache's Max Object Threshold for triggering an eviction exercise
var CacheMaxObjects *prometheus.GaugeVec

// CacheMaxBytes is a Gauge for the Trickster cache's Max Object Threshold for triggering an eviction exercise
var CacheMaxBytes *prometheus.GaugeVec

// ProxyMaxConnections is a Gauge representing the max number of active concurrent connections in the server
var ProxyMaxConnections prometheus.Gauge

// ProxyActiveConnections is a Gauge representing the number of active connections in the server
var ProxyActiveConnections prometheus.Gauge

// ProxyConnectionRequested is a counter representing the total number of connections requested by clients to the Proxy
var ProxyConnectionRequested prometheus.Counter

// ProxyConnectionAccepted is a counter representing the total number of connections accepted by the Proxy
var ProxyConnectionAccepted prometheus.Counter

// ProxyConnectionClosed is a counter representing the total number of connections closed by the Proxy
var ProxyConnectionClosed prometheus.Counter

// ProxyConnectionFailed is a counter for the total number of connections failed to connect for whatever reason
var ProxyConnectionFailed prometheus.Counter

func init() {

	BuildInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: buildSubsystem,
			Name:      "info",
			Help: "A metric with a constant '1' value labeled by version," +
				"revision, and goversion from which Trickster was built.",
		},
		[]string{"goversion", "revision", "version"},
	)

	LastReloadSuccessfulTimestamp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: configSubsystem,
			Name:      "last_reload_success_time_seconds",
			Help:      "Timestamp of the last successful configuration reload.",
		},
	)

	LastReloadSuccessful = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: configSubsystem,
			Name:      "last_reload_successful",
			Help:      "Whether the last configuration reload attempt was successful.",
		},
	)

	FrontendRequestStatus = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: frontendSubsystem,
			Name:      "requests_total",
			Help:      "Count of front end requests handled by Trickster",
		},
		[]string{"backend_name", "provider", "method", "path", "http_status"},
	)

	FrontendRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: frontendSubsystem,
			Name:      "requests_duration_seconds",
			Help:      "Histogram of front end request durations handled by Trickster",
			Buckets:   defaultBuckets,
		},
		[]string{"backend_name", "provider", "method", "path", "http_status"},
	)

	FrontendRequestWrittenBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: frontendSubsystem,
			Name:      "written_bytes_total",
			Help:      "Count of bytes written in front end requests handled by Trickster",
		},
		[]string{"backend_name", "provider", "method", "path", "http_status"})

	ProxyRequestStatus = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "requests_total",
			Help:      "Count of downstream client requests handled by Trickster",
		},
		[]string{"backend_name", "provider", "method", "cache_status", "http_status", "path"},
	)

	ProxyRequestElements = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "points_total",
			Help:      "Count of data points in the timeseries returned to the requesting client.",
		},
		[]string{"backend_name", "provider", "cache_status", "path"},
	)

	ProxyRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "request_duration_seconds",
			Help:      "Time required in seconds to proxy a given Prometheus query.",
			Buckets:   defaultBuckets,
		},
		[]string{"backend_name", "provider", "method", "status", "http_status", "path"},
	)

	ProxyMaxConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "max_connections",
			Help:      "Trickster max number of active connections.",
		},
	)

	ProxyActiveConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "active_connections",
			Help:      "Trickster number of active connections.",
		},
	)

	ProxyConnectionRequested = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "requested_connections_total",
			Help:      "Trickster total number of connections requested by clients.",
		},
	)
	ProxyConnectionAccepted = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "accepted_connections_total",
			Help:      "Trickster total number of accepted connections.",
		},
	)

	ProxyConnectionClosed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "closed_connections_total",
			Help:      "Trickster total number of closed connections.",
		},
	)

	ProxyConnectionFailed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "failed_connections_total",
			Help:      "Trickster total number of failed connections.",
		},
	)

	CacheObjectOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "operation_objects_total",
			Help:      "Count (in # of objects) of operations performed on a Trickster cache.",
		},
		[]string{"cache_name", "provider", "operation", "status"},
	)

	CacheByteOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "operation_bytes_total",
			Help:      "Count (in bytes) of operations performed on a Trickster cache.",
		},
		[]string{"cache_name", "provider", "operation", "status"},
	)

	CacheEvents = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "events_total",
			Help:      "Count of events performed on a Trickster cache.",
		},
		[]string{"cache_name", "provider", "event", "reason"},
	)

	CacheObjects = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "usage_objects",
			Help:      "Number of objects in a Trickster cache.",
		},
		[]string{"cache_name", "provider"},
	)

	CacheBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "usage_bytes",
			Help:      "Number of bytes in a Trickster cache.",
		},
		[]string{"cache_name", "provider"},
	)

	CacheMaxObjects = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "max_usage_objects",
			Help:      "Trickster cache's Max Object Threshold for triggering an eviction exercise.",
		},
		[]string{"cache_name", "provider"},
	)

	CacheMaxBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "max_usage_bytes",
			Help:      "Trickster cache's Max Byte Threshold for triggering an eviction exercise.",
		},
		[]string{"cache_name", "provider"},
	)

	// Register Metrics
	prometheus.MustRegister(FrontendRequestStatus)
	prometheus.MustRegister(FrontendRequestDuration)
	prometheus.MustRegister(FrontendRequestWrittenBytes)
	prometheus.MustRegister(ProxyRequestStatus)
	prometheus.MustRegister(ProxyRequestElements)
	prometheus.MustRegister(ProxyRequestDuration)
	prometheus.MustRegister(ProxyMaxConnections)
	prometheus.MustRegister(ProxyActiveConnections)
	prometheus.MustRegister(ProxyConnectionRequested)
	prometheus.MustRegister(ProxyConnectionAccepted)
	prometheus.MustRegister(ProxyConnectionClosed)
	prometheus.MustRegister(ProxyConnectionFailed)
	prometheus.MustRegister(CacheObjectOperations)
	prometheus.MustRegister(CacheByteOperations)
	prometheus.MustRegister(CacheEvents)
	prometheus.MustRegister(CacheObjects)
	prometheus.MustRegister(CacheBytes)
	prometheus.MustRegister(CacheMaxObjects)
	prometheus.MustRegister(CacheMaxBytes)
	prometheus.MustRegister(BuildInfo)
	prometheus.MustRegister(LastReloadSuccessful)
	prometheus.MustRegister(LastReloadSuccessfulTimestamp)
}

// Handler returns the http handler for the listener
func Handler() http.Handler {
	return promhttp.Handler()
}
