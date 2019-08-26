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

package metrics

import (
	"fmt"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
)

const (
	metricNamespace   = "trickster"
	cacheSubsystem    = "cache"
	proxySubsystem    = "proxy"
	frontendSubsystem = "frontend"
)

// Default histogram buckets used by trickster
var (
	defaultBuckets = []float64{0.05, 0.1, 0.5, 1, 5, 10, 20}
)

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

// CacheMaxObjects is a Gauge representing the Trickster cache's Max Object Threshold for triggering an eviction exercise
var CacheMaxObjects *prometheus.GaugeVec

// CacheMaxBytes is a Gauge representing the Trickster cache's Max Object Threshold for triggering an eviction exercise
var CacheMaxBytes *prometheus.GaugeVec

// Init initializes the instrumented metrics and starts the listener endpoint
func Init() {

	FrontendRequestStatus = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: frontendSubsystem,
			Name:      "requests_total",
			Help:      "Count of front end requests handled by Trickster",
		},
		[]string{"origin_name", "origin_type", "method", "path", "http_status"},
	)

	FrontendRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: frontendSubsystem,
			Name:      "requests_duration_seconds",
			Help:      "Histogram of front end request durations handled by Trickster",
			Buckets:   defaultBuckets,
		},
		[]string{"origin_name", "origin_type", "method", "path", "http_status"},
	)

	FrontendRequestWrittenBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: frontendSubsystem,
			Name:      "written_bytes_total",
			Help:      "Count of bytes written in front end requests handled by Trickster",
		},
		[]string{"origin_name", "origin_type", "method", "path", "http_status"})

	ProxyRequestStatus = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "requests_total",
			Help:      "Count of downstream client requests handled by Trickster",
		},
		[]string{"origin_name", "origin_type", "method", "cache_status", "http_status", "path"},
	)

	ProxyRequestElements = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "points_total",
			Help:      "Count of data points in the timeseries returned to the requesting client.",
		},
		[]string{"origin_name", "origin_type", "cache_status", "path"},
	)

	ProxyRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "request_duration_seconds",
			Help:      "Time required in seconds to proxy a given Prometheus query.",
			Buckets:   defaultBuckets,
		},
		[]string{"origin_name", "origin_type", "method", "status", "http_status", "path"},
	)

	CacheObjectOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "operation_objects_total",
			Help:      "Count (in # of objects) of operations performed on a Trickster cache.",
		},
		[]string{"cache_name", "cache_type", "operation", "status"},
	)

	CacheByteOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "operation_bytes_total",
			Help:      "Count (in bytes) of operations performed on a Trickster cache.",
		},
		[]string{"cache_name", "cache_type", "operation", "status"},
	)

	CacheEvents = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "events_total",
			Help:      "Count of events performed on a Trickster cache.",
		},
		[]string{"cache_name", "cache_type", "event", "reason"},
	)

	CacheObjects = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "usage_objects",
			Help:      "Number of objects in a Trickster cache.",
		},
		[]string{"cache_name", "cache_type"},
	)

	CacheBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "usage_bytes",
			Help:      "Number of bytes in a Trickster cache.",
		},
		[]string{"cache_name", "cache_type"},
	)

	CacheMaxObjects = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "max_usage_objects",
			Help:      "Trickster cache's Max Object Threshold for triggering an eviction exercise.",
		},
		[]string{"cache_name", "cache_type"},
	)

	CacheMaxBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "max_usage_bytes",
			Help:      "Trickster cache's Max Byte Threshold for triggering an eviction exercise.",
		},
		[]string{"cache_name", "cache_type"},
	)

	// Register Metrics
	prometheus.MustRegister(FrontendRequestStatus)
	prometheus.MustRegister(FrontendRequestDuration)
	prometheus.MustRegister(FrontendRequestWrittenBytes)
	prometheus.MustRegister(ProxyRequestStatus)
	prometheus.MustRegister(ProxyRequestElements)
	prometheus.MustRegister(ProxyRequestDuration)
	prometheus.MustRegister(CacheObjectOperations)
	prometheus.MustRegister(CacheByteOperations)
	prometheus.MustRegister(CacheEvents)
	prometheus.MustRegister(CacheObjects)
	prometheus.MustRegister(CacheBytes)
	prometheus.MustRegister(CacheMaxObjects)
	prometheus.MustRegister(CacheMaxBytes)

	// Turn up the Metrics HTTP Server
	if config.Metrics != nil && config.Metrics.ListenPort > 0 {
		go func() {

			log.Info("metrics http endpoint starting", log.Pairs{"address": config.Metrics.ListenAddress, "port": fmt.Sprintf("%d", config.Metrics.ListenPort)})

			http.Handle("/metrics", promhttp.Handler())
			if err := http.ListenAndServe(fmt.Sprintf("%s:%d", config.Metrics.ListenAddress, config.Metrics.ListenPort), nil); err != nil {
				log.Error("unable to start metrics http server", log.Pairs{"detail": err.Error()})
				os.Exit(1)
			}
		}()
	}

}
