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

package main

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ApplicationMetrics enumerates the metrics collected and reported by the trickster application.
type ApplicationMetrics struct {
	CacheRequestStatus   *prometheus.CounterVec
	CacheRequestElements *prometheus.CounterVec
	ProxyRequestDuration *prometheus.HistogramVec
}

// Unregister removes registered metrics from the Prometheus metrics instrumentation.
func (metrics ApplicationMetrics) Unregister() {
	prometheus.Unregister(metrics.CacheRequestStatus)
	prometheus.Unregister(metrics.CacheRequestElements)
	prometheus.Unregister(metrics.ProxyRequestDuration)
}

func (metrics ApplicationMetrics) ListenAndServe(config *Config, logger log.Logger) {
	// Turn up the Metrics HTTP Server
	if config.Metrics.ListenPort > 0 {
		go func() {

			level.Info(logger).Log("event", "metrics http endpoint starting", "address", config.Metrics.ListenAddress, "port", fmt.Sprintf("%d", config.Metrics.ListenPort))

			http.Handle("/metrics", promhttp.Handler())
			if err := http.ListenAndServe(fmt.Sprintf("%s:%d", config.Metrics.ListenAddress, config.Metrics.ListenPort), nil); err != nil {
				level.Error(logger).Log("event", "unable to start metrics http server", "detail", err.Error())
				os.Exit(1)
			}
		}()
	}
}

// NewApplicationMetrics returns a ApplicationMetrics object and instantiates an HTTP server for polling them.
func NewApplicationMetrics() *ApplicationMetrics {
	metrics := ApplicationMetrics{
		CacheRequestStatus: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "trickster_requests_total",
				Help: "Count of the total number of requests Trickster has handled",
			},
			[]string{"origin", "origin_type", "method", "status", "http_status"},
		),
		CacheRequestElements: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "trickster_points_total",
				Help: "Count of data points returned in a Prometheus query_range Request",
			},
			[]string{"origin", "origin_type", "status"},
		),
		ProxyRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "trickster_proxy_duration_seconds",
				Help:    "Time required in seconds to proxy a given Prometheus query.",
				Buckets: []float64{0.05, 0.1, 0.5, 1, 5, 10, 20},
			},
			[]string{"origin", "origin_type", "method", "status", "http_status"},
		),
	}

	prometheus.MustRegister(metrics.CacheRequestStatus)
	prometheus.MustRegister(metrics.CacheRequestElements)
	prometheus.MustRegister(metrics.ProxyRequestDuration)

	return &metrics
}
