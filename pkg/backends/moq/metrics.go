/*
 * Copyright 2026 The Trickster Authors
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

package moq

import (
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus"
)

func relayMetrics(backend string) *relay.Metrics {
	// each vector is bound to the backend once, so the relay resolves only its own remaining labels
	l := prometheus.Labels{keys.Backend_Name: backend}
	return &relay.Metrics{
		Requests:            metrics.MoQRequests.MustCurryWith(l),
		ActiveSubscriptions: metrics.MoQActiveSubscriptions.MustCurryWith(l),
		Objects:             metrics.MoQObjects.MustCurryWith(l),
		Bytes:               metrics.MoQBytes.MustCurryWith(l),
		DroppedObjects:      metrics.MoQDroppedObjects.MustCurryWith(l),
		FetchObjects:        metrics.MoQFetchObjects.MustCurryWith(l),
		LiveEdgeBytes:       metrics.MoQLiveEdgeBytes.WithLabelValues(backend),
		Publishers:          metrics.MoQPublishers.WithLabelValues(backend),
		Errors:              metrics.MoQErrors.MustCurryWith(l),
	}
}

func observeSession(listener, backend, transport, draft, result string) {
	metrics.MoQSessions.WithLabelValues(listener, backend, roleDownstream, transport, draft, result).Inc()
}

func activeSessions(listener, backend string, delta float64) {
	metrics.MoQActiveSessions.WithLabelValues(listener, backend, roleDownstream).Add(delta)
}
