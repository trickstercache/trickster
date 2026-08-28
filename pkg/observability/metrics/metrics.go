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

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	metricNamespace   = "trickster"
	cacheSubsystem    = "cache"
	proxySubsystem    = providers.Proxy
	configSubsystem   = "config"
	buildSubsystem    = "build"
	frontendSubsystem = "frontend"
	albSubsystem      = "alb"
	healthSubsystem   = "healthcheck"
	sqlSubsystem      = "sql"
	mysqlSubsystem    = "mysql"
	graphiteSubsystem = providers.Graphite
	tlsSubsystem      = "tls"
)

// Default histogram buckets used by trickster
var (
	defaultBuckets = []float64{0.05, 0.1, 0.5, 1, 5, 10, 20}
)

var (
	// BuildInfo is a Gauge representing the Trickster binary build information of the running server instance
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

	// LastReloadSuccessful gauge will be set to 1 if Trickster's last config reload succeeded else 0
	LastReloadSuccessful = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: configSubsystem,
			Name:      "last_reload_successful",
			Help:      "Whether the last configuration reload attempt was successful.",
		},
	)

	// LastReloadSuccessfulTimestamp gauge is the epoch time of the most recent successful config load
	LastReloadSuccessfulTimestamp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: configSubsystem,
			Name:      "last_reload_success_time_seconds",
			Help:      "Timestamp of the last successful configuration reload.",
		},
	)

	// ReloadAttemptsTotal is a Counter of total configuration reload attempts
	ReloadAttemptsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: configSubsystem,
			Name:      "reload_attempts_total",
			Help:      "Total number of configuration reload attempts.",
		},
	)

	// ReloadSuccessesTotal is a Counter of successful configuration reloads
	ReloadSuccessesTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: configSubsystem,
			Name:      "reload_successes_total",
			Help:      "Total number of successful configuration reloads.",
		},
	)

	// ReloadFailuresTotal is a Counter of failed configuration reloads
	ReloadFailuresTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: configSubsystem,
			Name:      "reload_failures_total",
			Help:      "Total number of failed configuration reloads.",
		},
	)

	// ReloadDurationSeconds is a Histogram of configuration reload duration in seconds
	ReloadDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: configSubsystem,
			Name:      "reload_duration_seconds",
			Help:      "Duration of configuration reload operations in seconds.",
			Buckets:   defaultBuckets,
		},
	)

	// FrontendRequestStatus is a Counter of front end requests that have been processed with their status
	FrontendRequestStatus = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: frontendSubsystem,
			Name:      "requests_total",
			Help:      "Count of front end requests handled by Trickster",
		},
		[]string{"backend_name", "provider", "method", "path", "http_status"},
	)

	// FrontendRequestDuration is a histogram that tracks the time it takes to process a request
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

	// FrontendRequestWrittenBytes is a Counter of bytes written for front end requests
	FrontendRequestWrittenBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: frontendSubsystem,
			Name:      "written_bytes_total",
			Help:      "Count of bytes written in front end requests handled by Trickster",
		},
		[]string{"backend_name", "provider", "method", "path", "http_status"},
	)

	// ProxyRequestStatus is a Counter of downstream client requests handled by Trickster
	ProxyRequestStatus = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "requests_total",
			Help:      "Count of downstream client requests handled by Trickster",
		},
		[]string{"backend_name", "provider", "method", "cache_status", "http_status", "path"},
	)

	// ProxyRequestElements is a Counter of data points in the timeseries returned to the requesting client
	ProxyRequestElements = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "points_total",
			Help:      "Count of data points in the timeseries returned to the requesting client.",
		},
		[]string{"backend_name", "provider", "cache_status", "path"},
	)

	// ProxyRequestDuration is a Histogram of time required in seconds to proxy a given Prometheus query
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

	// CacheObjectOperations is a Counter of operations (in # of objects) performed on a Trickster cache
	CacheObjectOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "operation_objects_total",
			Help:      "Count (in # of objects) of operations performed on a Trickster cache.",
		},
		[]string{"cache_name", "provider", "operation", "status"},
	)

	// CacheByteOperations is a Counter of operations (in # of bytes) performed on a Trickster cache
	CacheByteOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "operation_bytes_total",
			Help:      "Count (in bytes) of operations performed on a Trickster cache.",
		},
		[]string{"cache_name", "provider", "operation", "status"},
	)

	// CacheEvents is a Counter of events performed on a Trickster cache
	CacheEvents = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "events_total",
			Help:      "Count of events performed on a Trickster cache.",
		},
		[]string{"cache_name", "provider", "event", "reason"},
	)

	// CacheObjects is a Gauge representing the number of objects in a Trickster cache
	CacheObjects = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "usage_objects",
			Help:      "Number of objects in a Trickster cache.",
		},
		[]string{"cache_name", "provider"},
	)

	// CacheBytes is a Gauge representing the number of bytes in a Trickster cache
	CacheBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "usage_bytes",
			Help:      "Number of bytes in a Trickster cache.",
		},
		[]string{"cache_name", "provider"},
	)

	// CacheMaxObjects is a Gauge for the Trickster cache's Max Object Threshold for triggering an eviction exercise
	CacheMaxObjects = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "max_usage_objects",
			Help:      "Trickster cache's Max Object Threshold for triggering an eviction exercise.",
		},
		[]string{"cache_name", "provider"},
	)

	// CacheMaxBytes is a Gauge for the Trickster cache's Max Object Threshold for triggering an eviction exercise
	CacheMaxBytes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "max_usage_bytes",
			Help:      "Trickster cache's Max Byte Threshold for triggering an eviction exercise.",
		},
		[]string{"cache_name", "provider"},
	)

	// ProxyMaxConnections is a Gauge representing the max number of active concurrent connections in the server
	ProxyMaxConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "max_connections",
			Help:      "Trickster max number of active connections.",
		},
	)

	// ProxyActiveConnections is a Gauge representing the number of active connections in the server
	ProxyActiveConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "active_connections",
			Help:      "Trickster number of active connections.",
		},
	)

	// ProxyConnectionRequested is a counter representing the total number of connections requested by clients to the Proxy
	ProxyConnectionRequested = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "requested_connections_total",
			Help:      "Trickster total number of connections requested by clients.",
		},
	)

	// ProxyConnectionAccepted is a counter representing the total number of connections accepted by the Proxy
	ProxyConnectionAccepted = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "accepted_connections_total",
			Help:      "Trickster total number of accepted connections.",
		},
	)

	// ProxyConnectionClosed is a counter representing the total number of connections closed by the Proxy
	ProxyConnectionClosed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "closed_connections_total",
			Help:      "Trickster total number of closed connections.",
		},
	)

	// ProxyConnectionFailed is a counter for the total number of connections failed to connect for whatever reason
	ProxyConnectionFailed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "failed_connections_total",
			Help:      "Trickster total number of failed connections.",
		},
	)

	// ProxyQueryRangeRejections is a counter for requests rejected due to exceeding the max_query_range limit
	ProxyQueryRangeRejections = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "query_range_rejections_total",
			Help:      "Trickster total number of queries rejected due to exceeding the max_query_range limit.",
		},
		[]string{"backend_name"},
	)

	// SQLQueryAnalysis counts SQL analyzer classifications using bounded mode,
	// dialect, and reason labels. Parse failures and OPC fallback are represented
	// by the invalid_sql reason and object cache mode respectively.
	SQLQueryAnalysis = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: sqlSubsystem,
			Name:      "query_analysis_total",
			Help:      "Count of SQL query cache-eligibility classifications.",
		},
		[]string{"backend_name", "dialect", "cache_mode", "reason"},
	)

	// SQLQueryRewriteFailures counts failures to render a SQL origin request for
	// a cache-miss extent. The reason label is a fixed internal category and
	// never contains query text or parser errors.
	SQLQueryRewriteFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: sqlSubsystem,
			Name:      "query_rewrite_failures_total",
			Help:      "Count of SQL cache-miss extent rewrite failures.",
		},
		[]string{"backend_name", "dialect", "reason"},
	)

	// SQLQueryCache counts native SQL protocol cache outcomes. Unlike the HTTP
	// proxy metrics, this does not manufacture HTTP method or status labels.
	SQLQueryCache = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: sqlSubsystem,
			Name:      "query_cache_total",
			Help:      "Count of SQL query cache outcomes.",
		},
		[]string{"backend_name", "dialect", "cache_mode", "cache_status"},
	)

	// MySQLConnections tracks bounded connection lifecycle outcomes.
	MySQLConnections = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: mysqlSubsystem,
			Name:      "connections_total",
			Help:      "Count of MySQL connection lifecycle events.",
		},
		[]string{"backend_name", "event"},
	)

	// MySQLActiveConnections is the current authenticated-or-handshaking count.
	MySQLActiveConnections = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: mysqlSubsystem,
			Name:      "active_connections",
			Help:      "Current MySQL downstream connections.",
		},
		[]string{"backend_name"},
	)

	// MySQLConnectionErrors tracks handshake, authentication, protocol, and
	// upstream failures without including user-controlled text in labels.
	MySQLConnectionErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: mysqlSubsystem,
			Name:      "errors_total",
			Help:      "Count of MySQL protocol and origin failures.",
		},
		[]string{"backend_name", "class"},
	)

	// GraphiteResolutionLookups counts step-resolution outcomes. confidence
	// is exact | derived | configured | unknown and source is registry |
	// response | probe | static | function | none; both label sets are
	// closed, and neither ever contains a metric path or query text.
	GraphiteResolutionLookups = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: graphiteSubsystem,
			Name:      "resolution_lookups_total",
			Help:      "Count of Graphite step-resolution lookups by confidence and source.",
		},
		[]string{"backend_name", "confidence", "source"},
	)

	// GraphiteProbes counts synthetic requests issued to learn an archive
	// ladder. kind is narrow | wide | find and result is step | empty | error.
	GraphiteProbes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: graphiteSubsystem,
			Name:      "probes_total",
			Help:      "Count of Graphite resolution probes by kind and result.",
		},
		[]string{"backend_name", "kind", "result"},
	)

	// GraphiteLadders is the number of distinct complete archive ladders the
	// resolution registry knows. It should spike during warmup and then flatten
	// at roughly the number of storage-schemas.conf patterns in use.
	GraphiteLadders = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: graphiteSubsystem,
			Name:      "ladders",
			Help:      "Number of distinct Graphite archive ladders known to the resolution registry.",
		},
		[]string{"backend_name"},
	)

	// GraphiteRegistryEntries is the size of each resolution registry layer:
	// leaf | ladder | target | negative.
	GraphiteRegistryEntries = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: graphiteSubsystem,
			Name:      "registry_entries",
			Help:      "Number of entries in each layer of the Graphite resolution registry.",
		},
		[]string{"backend_name", "layer"},
	)

	// GraphiteStepMispredictions counts responses whose step contradicted the
	// predicted one. Any non-zero value is a defect: the prediction is
	// discarded and the request re-served unaccelerated, but the ladder that
	// produced it was wrong.
	GraphiteStepMispredictions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: graphiteSubsystem,
			Name:      "step_mispredictions_total",
			Help:      "Count of Graphite responses whose step differed from the predicted step.",
		},
		[]string{"backend_name"},
	)

	// GraphiteFallbacks counts render requests routed to the unaccelerated
	// lane. reason is a closed set of internal categories and never contains
	// a target expression.
	GraphiteFallbacks = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: graphiteSubsystem,
			Name:      "fallbacks_total",
			Help:      "Count of Graphite render requests served without delta caching, by reason.",
		},
		[]string{"backend_name", "reason"},
	)

	// MySQLRouteSelections tracks bounded native User Router outcomes. Backend
	// and router names come from configuration; usernames are never labels.
	MySQLRouteSelections = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: mysqlSubsystem,
			Name:      "route_selections_total",
			Help:      "Count of native MySQL route-selection outcomes.",
		},
		[]string{"router_name", "backend_name", "outcome"},
	)

	// MySQLCommandLatency measures a fixed set of protocol operations.
	MySQLCommandLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: mysqlSubsystem,
			Name:      "command_duration_seconds",
			Help:      "Duration of bounded MySQL protocol operations.",
			Buckets:   defaultBuckets,
		},
		[]string{"backend_name", "operation"},
	)

	// ALBFanoutFailures counts per-shard failures during ALB fanout. The
	// reason label distinguishes silent contribution failures (e.g. bad
	// encoding, parse errors), explicit panics in the per-shard goroutine,
	// capture-buffer truncation, and routing flap (target was healthy at
	// snapshot time but failing by the time the response was observed).
	// The variant label distinguishes sub-fanouts within a mechanism
	// (e.g. TSM's paired avg-sum / avg-count queries); empty when the
	// mechanism has only one fanout path.
	ALBFanoutFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: albSubsystem,
			Name:      "fanout_failures_total",
			Help:      "Count of per-shard failures during ALB fanout, by mechanism, variant, and reason.",
		},
		[]string{"mechanism", "variant", "reason"},
	)

	// ALBFanoutAttempts counts ALB fanout calls (one increment per All/Race
	// invocation, not per shard). Paired with ALBFanoutFailures so dashboards
	// can compute a failure rate as failures_total / attempts_total. The
	// variant label distinguishes sub-fanouts within a mechanism (e.g. TSM's
	// paired avg-sum / avg-count queries); empty when the mechanism has only
	// one fanout path.
	ALBFanoutAttempts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: albSubsystem,
			Name:      "fanout_attempts_total",
			Help:      "Count of ALB fanout invocations, by mechanism and variant.",
		},
		[]string{"mechanism", "variant"},
	)

	// ALBTSMReplicaEvents counts logical replica-group activity without using
	// configured group names as labels. "group_attempted" and "group_failed"
	// describe logical availability; "fallback", "conflict", and "suppressed"
	// describe replica selection within an available group.
	ALBTSMReplicaEvents = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: albSubsystem,
			Name:      "tsm_replica_events_total",
			Help:      "Count of TSM logical replica-group selection events, by event and variant.",
		},
		[]string{"event", "variant"},
	)

	// ALBFanoutLoserDrain observes how long each losing slot in a
	// fanout.WaitForFirst call takes to exit after the winner is claimed.
	// WaitForFirst cancels raceCtx on winner-claim and returns immediately;
	// losers drain in the background via ctx-cancel propagating through the
	// HTTP transport. This histogram makes that drain observable so operators
	// can distinguish "sub-ms healthy" from "upstream ignoring cancel."
	ALBFanoutLoserDrain = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: albSubsystem,
			Name:      "fanout_loser_drain_seconds",
			Help:      "Time between winner-claim and each losing slot's goroutine exit, by mechanism and variant.",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		},
		[]string{"mechanism", "variant"},
	)

	// ALBPoolRefreshPanicRecovered counts recovered panics in ALB pool refresh
	// worker goroutines (checkHealth, listenStatusUpdates). A dead worker leaves
	// the healthy-target snapshot stale; the per-call re-filter in Targets()
	// still produces correct dispatch, but operator-visible gauges drift.
	ALBPoolRefreshPanicRecovered = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: albSubsystem,
			Name:      "pool_refresh_panic_recovered_total",
			Help:      "Count of recovered panics in ALB pool refresh worker goroutines, by worker.",
		},
		[]string{"worker"},
	)

	// HealthcheckProbePanicRecovered counts recovered panics in the per-target
	// health-probe ticker goroutine. Without recovery, a single panicking probe
	// would kill the loop and freeze the target's Status at its last value,
	// silently masking real upstream failures from operators and ALB pools.
	HealthcheckProbePanicRecovered = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: healthSubsystem,
			Name:      "probe_panic_recovered_total",
			Help:      "Count of recovered panics in the per-target health-probe ticker, by backend.",
		},
		[]string{"backend_name"},
	)

	// HealthcheckProbeLatency records wall-clock duration of each per-target
	// health probe (both successful and failing). Lets ALB routing dashboards
	// distinguish a slow-but-healthy backend from a fast-and-healthy one;
	// without this, only binary healthy/unhealthy state is visible.
	HealthcheckProbeLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: healthSubsystem,
			Name:      "probe_latency_seconds",
			Help:      "Latency of per-target health-check probes, in seconds, by backend.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"backend_name"},
	)

	// ProxyEnginesPanicRecovered counts recovered panics in fire-and-forget
	// goroutines spawned by the proxy/engines layer (DPC cache.Remove, upstream
	// access-log emission, PCF io.Copy pumps). A panic in any of these would
	// otherwise crash the entire trickster process, since the goroutine has no
	// recover above it. The site label identifies which call site recovered.
	ProxyEnginesPanicRecovered = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: proxySubsystem,
			Name:      "engines_panic_recovered_total",
			Help:      "Count of recovered panics in proxy/engines fire-and-forget goroutines, by call site.",
		},
		[]string{"site"},
	)

	// CacheIndexPanicRecovered counts recovered panics in the cache index
	// flusher and reaper goroutines. Without recovery, a panicking flusher
	// leaves the on-disk index stale (cold-start drops the cache); a panicking
	// reaper lets expired entries accumulate until the cache outgrows its
	// configured ceiling. The worker label distinguishes flusher from reaper.
	CacheIndexPanicRecovered = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: cacheSubsystem,
			Name:      "index_panic_recovered_total",
			Help:      "Count of recovered panics in cache index worker goroutines, by worker.",
		},
		[]string{"worker"},
	)

	// HealthHandlerPanicRecovered counts recovered panics in the status-page
	// builder goroutine spawned by the /trickster/health handler. A panic in
	// the builder would freeze the status page at its last rendered text;
	// recovery keeps the handler serving updated state.
	HealthHandlerPanicRecovered = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: healthSubsystem,
			Name:      "handler_panic_recovered_total",
			Help:      "Count of recovered panics in the health status-page builder goroutine.",
		},
	)

	// HealthcheckStatusNotifyPanicRecovered counts recovered panics while
	// notifying a Status subscriber (e.g. a closed channel send). The per-
	// subscriber recover ensures a single bad subscriber cannot kill the probe
	// loop or block notifying the remaining subscribers.
	HealthcheckStatusNotifyPanicRecovered = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: healthSubsystem,
			Name:      "status_notify_panic_recovered_total",
			Help:      "Count of recovered panics while notifying a healthcheck Status subscriber, by backend.",
		},
		[]string{"backend_name"},
	)

	// ALBPoolAdmitsFailing flags ALB pools whose healthy_floor admits a Failing
	// status (floor <= StatusFailing). Operators who set floor below 0 to keep
	// traffic flowing during the Initializing window may not realize they're
	// also admitting members the probe has confirmed broken; the gauge surfaces
	// that misconfiguration without spamming logs.
	ALBPoolAdmitsFailing = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: albSubsystem,
			Name:      "pool_admits_failing",
			Help:      "1 when an ALB pool's healthy_floor admits members in Failing state; 0 otherwise.",
		},
		[]string{"backend_name"},
	)

	// TLSCertificateNotAfter is a Gauge of each serving certificate's
	// NotAfter (expiration) time as unix seconds, by listener and entry.
	TLSCertificateNotAfter = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: tlsSubsystem,
			Name:      "certificate_expiration_time_seconds",
			Help:      "NotAfter time of a serving TLS certificate, as unix seconds.",
		},
		[]string{"listener", "entry"},
	)

	// TLSCertificateLastLoad is a Gauge of the time each serving certificate
	// was last successfully loaded from its source, as unix seconds.
	TLSCertificateLastLoad = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: tlsSubsystem,
			Name:      "certificate_last_load_time_seconds",
			Help:      "Time a serving TLS certificate was last loaded from its source, as unix seconds.",
		},
		[]string{"listener", "entry"},
	)

	// TLSCertificateSwapsTotal counts hot swaps of a rotated certificate into
	// a live listener.
	TLSCertificateSwapsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: tlsSubsystem,
			Name:      "certificate_swaps_total",
			Help:      "Count of TLS certificates hot-swapped into a live listener.",
		},
		[]string{"listener", "entry"},
	)

	// TLSCertificateValidationFailures counts detected certificate source
	// changes that failed pair validation (e.g. a mid-rotation partial write)
	// and were not swapped in; the last-good certificate keeps serving.
	TLSCertificateValidationFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: tlsSubsystem,
			Name:      "certificate_validation_failures_total",
			Help:      "Count of TLS certificate source changes that failed validation.",
		},
		[]string{"entry"},
	)

	// TLSWatcherErrors counts errors reading watched TLS certificate source
	// files; the last-good certificate keeps serving.
	TLSWatcherErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: tlsSubsystem,
			Name:      "watcher_errors_total",
			Help:      "Count of errors reading watched TLS certificate source files.",
		},
		[]string{"entry"},
	)

	// TLSCertificateStoreSize is a Gauge of the number of certificates in
	// each listener's certificate store.
	TLSCertificateStoreSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: tlsSubsystem,
			Name:      "certificate_store_size",
			Help:      "Number of certificates in a listener's TLS certificate store.",
		},
		[]string{"listener"},
	)

	// ALBPoolFloorReset flags ALB pools whose healthy_floor was reset to 0 at
	// startup because one or more pool members have no health check and could
	// never reach the configured floor (>= Passing), which would otherwise
	// empty the pool and 502 every request.
	ALBPoolFloorReset = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: albSubsystem,
			Name:      "pool_floor_reset",
			Help:      "1 when an ALB pool's healthy_floor was reset to 0 because members lack health checks; 0 otherwise.",
		},
		[]string{"backend_name"},
	)
)

func init() {
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
	prometheus.MustRegister(ALBFanoutFailures)
	prometheus.MustRegister(ALBFanoutAttempts)
	prometheus.MustRegister(ALBTSMReplicaEvents)
	prometheus.MustRegister(ALBFanoutLoserDrain)
	prometheus.MustRegister(ALBPoolRefreshPanicRecovered)
	prometheus.MustRegister(HealthcheckProbePanicRecovered)
	prometheus.MustRegister(HealthcheckProbeLatency)
	prometheus.MustRegister(ProxyEnginesPanicRecovered)
	prometheus.MustRegister(CacheIndexPanicRecovered)
	prometheus.MustRegister(HealthHandlerPanicRecovered)
	prometheus.MustRegister(HealthcheckStatusNotifyPanicRecovered)
	prometheus.MustRegister(ALBPoolAdmitsFailing)
	prometheus.MustRegister(ALBPoolFloorReset)
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
	prometheus.MustRegister(ReloadAttemptsTotal)
	prometheus.MustRegister(ReloadSuccessesTotal)
	prometheus.MustRegister(ReloadFailuresTotal)
	prometheus.MustRegister(ReloadDurationSeconds)
	prometheus.MustRegister(ProxyQueryRangeRejections)
	prometheus.MustRegister(SQLQueryAnalysis)
	prometheus.MustRegister(SQLQueryRewriteFailures)
	prometheus.MustRegister(SQLQueryCache)
	prometheus.MustRegister(MySQLConnections)
	prometheus.MustRegister(MySQLActiveConnections)
	prometheus.MustRegister(MySQLConnectionErrors)
	prometheus.MustRegister(GraphiteResolutionLookups)
	prometheus.MustRegister(GraphiteProbes)
	prometheus.MustRegister(GraphiteLadders)
	prometheus.MustRegister(GraphiteRegistryEntries)
	prometheus.MustRegister(GraphiteStepMispredictions)
	prometheus.MustRegister(GraphiteFallbacks)
	prometheus.MustRegister(MySQLRouteSelections)
	prometheus.MustRegister(MySQLCommandLatency)
	prometheus.MustRegister(TLSCertificateNotAfter)
	prometheus.MustRegister(TLSCertificateLastLoad)
	prometheus.MustRegister(TLSCertificateSwapsTotal)
	prometheus.MustRegister(TLSCertificateValidationFailures)
	prometheus.MustRegister(TLSWatcherErrors)
	prometheus.MustRegister(TLSCertificateStoreSize)
}

// Handler returns the http handler for the listener
func Handler() http.Handler {
	return promhttp.Handler()
}

// partialDeleter matches the prometheus vector types' DeletePartialMatch
type partialDeleter interface {
	DeletePartialMatch(prometheus.Labels) int
}

// backendSeriesVecs enumerates every metric vector labeled by backend_name,
// so series for a torn-down backend can be removed. Update this list when
// adding a new backend_name-labeled vector.
var backendSeriesVecs = []partialDeleter{
	FrontendRequestStatus,
	FrontendRequestDuration,
	FrontendRequestWrittenBytes,
	ProxyRequestStatus,
	ProxyRequestElements,
	ProxyRequestDuration,
	ProxyQueryRangeRejections,
	SQLQueryAnalysis,
	SQLQueryRewriteFailures,
	SQLQueryCache,
	MySQLConnections,
	MySQLActiveConnections,
	MySQLConnectionErrors,
	MySQLRouteSelections,
	MySQLCommandLatency,
	HealthcheckProbePanicRecovered,
	HealthcheckProbeLatency,
	HealthcheckStatusNotifyPanicRecovered,
	ALBPoolAdmitsFailing,
	ALBPoolFloorReset,
}

// DeleteBackendSeries removes every metric series labeled with the provided
// backend_name. It is called when a runtime-instantiated (discovered) backend
// is torn down, so stale series don't accumulate as elastic members churn.
func DeleteBackendSeries(backendName string) {
	if backendName == "" {
		return
	}
	labels := prometheus.Labels{"backend_name": backendName}
	for _, v := range backendSeriesVecs {
		v.DeletePartialMatch(labels)
	}
}

// ALB Autodiscovery metrics
var (
	// ALBDiscoveryMembers is the current count of discovered pool members
	// per ALB and discoverer.
	ALBDiscoveryMembers = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: albSubsystem,
			Name:      "discovery_members",
			Help:      "Current number of discovered ALB pool members.",
		},
		[]string{"alb_name", "discoverer"},
	)

	// ALBDiscoveryMemberChanges counts discovered-member lifecycle events;
	// the event label is add or remove.
	ALBDiscoveryMemberChanges = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: albSubsystem,
			Name:      "discovery_member_changes_total",
			Help:      "Count of discovered ALB pool member additions and removals.",
		},
		[]string{"alb_name", "discoverer", "event"},
	)

	// ALBDiscoverySnapshots counts membership snapshots processed per ALB.
	// The result label is applied (membership updated), unchanged (no-op),
	// rejected (guardrail-refused, e.g. a min_members violation), or
	// partial (applied, but one or more member instantiations failed).
	ALBDiscoverySnapshots = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: albSubsystem,
			Name:      "discovery_snapshots_total",
			Help:      "Count of ALB autodiscovery membership snapshots processed, by result.",
		},
		[]string{"alb_name", "discoverer", "result"},
	)

	// ALBDiscoveryLastRefresh is the unix timestamp of the last successfully
	// processed (applied or unchanged) snapshot per ALB, for staleness
	// alerting.
	ALBDiscoveryLastRefresh = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: albSubsystem,
			Name:      "discovery_last_refresh_success_time_seconds",
			Help:      "Epoch timestamp of the last successfully processed autodiscovery snapshot.",
		},
		[]string{"alb_name", "discoverer"},
	)

	// DiscoveryRefreshErrors counts provider-side refresh and watch
	// failures (DNS resolution errors, kubernetes list/sync failures, file
	// read/parse failures), per discoverer.
	DiscoveryRefreshErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: "discovery",
			Name:      "refresh_errors_total",
			Help:      "Count of autodiscovery provider refresh/watch errors.",
		},
		[]string{"discoverer", "provider"},
	)
)

func init() {
	prometheus.MustRegister(ALBDiscoveryMembers)
	prometheus.MustRegister(ALBDiscoveryMemberChanges)
	prometheus.MustRegister(ALBDiscoverySnapshots)
	prometheus.MustRegister(ALBDiscoveryLastRefresh)
	prometheus.MustRegister(DiscoveryRefreshErrors)
}

// albDiscoveryVecs enumerates the vectors labeled by alb_name for
// autodiscovery, so a torn-down (or reloaded-away) discovery-backed ALB's
// series can be removed.
var albDiscoveryVecs = []partialDeleter{
	ALBDiscoveryMembers,
	ALBDiscoveryMemberChanges,
	ALBDiscoverySnapshots,
	ALBDiscoveryLastRefresh,
}

// DeleteALBDiscoverySeries removes every autodiscovery metric series for
// the provided ALB name; called when its dynamic pool manager stops.
func DeleteALBDiscoverySeries(albName string) {
	if albName == "" {
		return
	}
	labels := prometheus.Labels{"alb_name": albName}
	for _, v := range albDiscoveryVecs {
		v.DeletePartialMatch(labels)
	}
}
