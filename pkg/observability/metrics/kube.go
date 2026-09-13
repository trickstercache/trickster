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

package metrics

import (
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"

	"github.com/prometheus/client_golang/prometheus"
)

// kgwSubsystem is the Kubernetes Gateway/Ingress controller's metric subsystem
const kgwSubsystem = "kgw"

// Reconcile results and error stages for the Kubernetes controller metrics
const (
	KubeResultApplied   = "applied"
	KubeResultUnchanged = "unchanged"
	KubeResultError     = "error"

	KubeStageCompile      = "compile"
	KubeStageApply        = "apply"
	KubeStageCertificates = "certificates"
	KubeStageStatus       = "status"
)

// Kubernetes Gateway/Ingress controller metrics
var (
	// KubeReconciles counts reconcile passes by result
	KubeReconciles = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: kgwSubsystem,
			Name:      "reconciles_total",
			Help:      "Count of Kubernetes controller reconcile passes, by result.",
		},
		[]string{keys.Result},
	)

	// KubeReconcileDuration observes the wall-clock duration of a whole
	// reconcile pass
	KubeReconcileDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: kgwSubsystem,
			Name:      "reconcile_duration_seconds",
			Help:      "Duration of Kubernetes controller reconcile passes.",
			Buckets:   defaultBuckets,
		},
	)

	// KubeReconcileErrors counts the stage a reconcile pass failed in
	KubeReconcileErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: kgwSubsystem,
			Name:      "reconcile_errors_total",
			Help:      "Count of Kubernetes controller reconcile failures, by stage.",
		},
		[]string{keys.Stage},
	)

	// KubeWatchEvents counts informer deliveries by kind and event
	KubeWatchEvents = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: kgwSubsystem,
			Name:      "watch_events_total",
			Help:      "Count of Kubernetes watch events received, by kind and event.",
		},
		[]string{keys.Kind, keys.Event},
	)

	// KubeTranslateDuration observes how long building the IR from the caches takes
	KubeTranslateDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: kgwSubsystem,
			Name:      "translate_duration_seconds",
			Help:      "Duration of translating watched Kubernetes objects into the routing model.",
			Buckets:   defaultBuckets,
		},
	)

	// KubeApplyDuration observes how long applying generated configuration takes
	KubeApplyDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: kgwSubsystem,
			Name:      "apply_duration_seconds",
			Help:      "Duration of applying generated Kubernetes configuration to the data plane.",
			Buckets:   defaultBuckets,
		},
	)

	// KubeGeneratedObjects gauges what the last translation produced, by kind
	KubeGeneratedObjects = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: kgwSubsystem,
			Name:      "generated_objects",
			Help:      "Number of objects in the Kubernetes controller's routing model, by kind.",
		},
		[]string{keys.Kind},
	)

	// KubeStatusWriteFailures counts status updates the API server refused, by kind
	KubeStatusWriteFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: kgwSubsystem,
			Name:      "status_write_failures_total",
			Help:      "Count of Kubernetes status writes that failed, by kind.",
		},
		[]string{keys.Kind},
	)

	// KubeLeader is 1 while this replica writes status and Events
	KubeLeader = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: kgwSubsystem,
			Name:      "leader",
			Help:      "1 when this replica is the Kubernetes controller leader, 0 otherwise.",
		},
	)

	// KubeLastSuccessfulSync is the epoch time the data plane last matched the cluster
	KubeLastSuccessfulSync = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: kgwSubsystem,
			Name:      "last_successful_sync_time_seconds",
			Help:      "Epoch timestamp of the last reconcile pass that applied without error.",
		},
	)

	// KubeRouteInfo joins a generated backend name to the Kubernetes object it serves
	KubeRouteInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: kgwSubsystem,
			Name:      "route_info",
			Help: "A constant 1 labeled by the Kubernetes route and the generated " +
				"backend serving it, for joining backend metrics to routes.",
		},
		[]string{keys.Kind, keys.Route, keys.Namespace, keys.Backend_Name},
	)
)

func init() {
	prometheus.MustRegister(KubeReconciles)
	prometheus.MustRegister(KubeReconcileDuration)
	prometheus.MustRegister(KubeReconcileErrors)
	prometheus.MustRegister(KubeWatchEvents)
	prometheus.MustRegister(KubeTranslateDuration)
	prometheus.MustRegister(KubeApplyDuration)
	prometheus.MustRegister(KubeGeneratedObjects)
	prometheus.MustRegister(KubeStatusWriteFailures)
	prometheus.MustRegister(KubeLeader)
	prometheus.MustRegister(KubeLastSuccessfulSync)
	prometheus.MustRegister(KubeRouteInfo)
}
