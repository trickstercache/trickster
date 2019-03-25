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
	metricNamespace = "trickster"
	cacheSubsystem  = "cache"
	proxySubsystem  = "proxy"
)

// ProxyRequestStatus ...
var ProxyRequestStatus *prometheus.CounterVec

// ProxyRequestElements ...
var ProxyRequestElements *prometheus.CounterVec

// ProxyRequestDuration ...
var ProxyRequestDuration *prometheus.HistogramVec

// CacheObjectOperations ...
var CacheObjectOperations *prometheus.CounterVec

// CacheByteOperations ...
var CacheByteOperations *prometheus.CounterVec

// CacheEvents ...
var CacheEvents *prometheus.CounterVec

// CacheObjects ...
var CacheObjects *prometheus.GaugeVec

// CacheBytes ...
var CacheBytes *prometheus.GaugeVec

// CacheMaxObjects ...
var CacheMaxObjects *prometheus.GaugeVec

// CacheMaxBytes ...
var CacheMaxBytes *prometheus.GaugeVec

// Init initializes the instrumented metrics and starts the listener endpoint
func Init() {

	ProxyRequestStatus = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "requests_total",
			Help:      "Count of downstream client requests handled by trickster",
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
			Buckets:   []float64{0.05, 0.1, 0.5, 1, 5, 10, 20},
		},
		[]string{"origin_name", "origin_type", "method", "status", "http_status", "path"},
	)

	CacheObjectOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "object_operations_total",
			Help:      "Count (in # of objects) of operations performed on a Trickster cache.",
		},
		[]string{"cache_name", "cache_type", "operation", "status"},
	)

	CacheByteOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "byte_operations_total",
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
			Name:      "objects_total",
			Help:      "Count of objects in a Trickster cache.",
		},
		[]string{"cache_name", "cache_type"},
	)

	CacheBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "bytes_total",
			Help:      "Count of bytes in a Trickster cache.",
		},
		[]string{"cache_name", "cache_type"},
	)

	CacheMaxObjects = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "max_objects_total",
			Help:      "Trickster cache's Max Object Threshold for triggering an eviction exercise.",
		},
		[]string{"cache_name", "cache_type"},
	)

	CacheMaxBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "max_bytes_total",
			Help:      "Trickster cache's Max Byte Threshold for triggering an eviction exercise.",
		},
		[]string{"cache_name", "cache_type"},
	)

	// Register Metrics
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
