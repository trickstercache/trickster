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

package relay

import "github.com/prometheus/client_golang/prometheus"

const (
	kindSubscribe          = "subscribe"
	kindFetch              = "fetch"
	kindTrackStatus        = "track_status"
	kindPublish            = "publish"
	kindPublishNamespace   = "publish_namespace"
	kindSubscribeNamespace = "subscribe_namespace"
	kindSubscribeTracks    = "subscribe_tracks"
	kindOther              = "other"

	resultOK      = "ok"
	resultRefused = "refused"

	directionIn         = "in"
	directionOut        = "out"
	directionUpstream   = "upstream"
	directionDownstream = "downstream"

	deliveryStream   = "stream"
	deliveryDatagram = "datagram"
	deliveryFetch    = "fetch"

	sourceLiveEdge = "live_edge"
	sourceCache    = "cache"
	sourceUpstream = "upstream"

	dropOversize          = "oversize"
	dropDeliveryTimeout   = "delivery_timeout"
	dropDatagramQueueFull = "datagram_queue_full"
	dropSpillQueueFull    = "spill_queue_full"

	errClassMalformed = "malformed_track"
	errClassCache     = "cache"
)

// Metrics are the series a relay records to, already bound to its backend; each vector's remaining labels are
// listed beside it
type Metrics struct {
	// Requests counts downstream requests by type and result
	Requests *prometheus.CounterVec
	// ActiveSubscriptions gauges subscriptions by direction (upstream, downstream)
	ActiveSubscriptions *prometheus.GaugeVec
	// Objects counts objects by direction (in, out) and delivery (stream, datagram, fetch)
	Objects *prometheus.CounterVec
	// Bytes counts object payload and property bytes by direction (in, out)
	Bytes *prometheus.CounterVec
	// DroppedObjects counts objects not forwarded, by reason
	DroppedObjects *prometheus.CounterVec
	// FetchObjects counts objects served to FETCH and fills, by source (live_edge, cache, upstream)
	FetchObjects *prometheus.CounterVec
	// LiveEdgeBytes gauges the bytes the live edge holds
	LiveEdgeBytes prometheus.Gauge
	// Publishers gauges the PUBLISH subscriptions publishers hold open to the relay
	Publishers prometheus.Gauge
	// Errors counts failures by class
	Errors *prometheus.CounterVec
}

type meters struct {
	m          *Metrics
	objectsIn  [3]prometheus.Counter
	objectsOut [3]prometheus.Counter
	bytesIn    prometheus.Counter
	bytesOut   prometheus.Counter
}

func (m *Metrics) resolve() *meters {
	if m == nil {
		return nil
	}
	// the per-object series are resolved once, so the hot path does no label lookups
	ms := &meters{m: m}
	for i, d := range []string{deliveryStream, deliveryDatagram, deliveryFetch} {
		ms.objectsIn[i] = m.Objects.WithLabelValues(directionIn, d)
		ms.objectsOut[i] = m.Objects.WithLabelValues(directionOut, d)
	}
	ms.bytesIn = m.Bytes.WithLabelValues(directionIn)
	ms.bytesOut = m.Bytes.WithLabelValues(directionOut)
	return ms
}

func deliveryIndex(delivery string) int {
	switch delivery {
	case deliveryDatagram:
		return 1
	case deliveryFetch:
		return 2
	}
	return 0
}

func (ms *meters) object(direction, delivery string, size int64) {
	if ms == nil {
		return
	}
	i := deliveryIndex(delivery)
	if direction == directionIn {
		ms.objectsIn[i].Inc()
		ms.bytesIn.Add(float64(size))
		return
	}
	ms.objectsOut[i].Inc()
	ms.bytesOut.Add(float64(size))
}

func (ms *meters) request(kind, result string) {
	if ms != nil {
		ms.m.Requests.WithLabelValues(kind, result).Inc()
	}
}

func (ms *meters) subscriptions(direction string, delta float64) {
	if ms != nil {
		ms.m.ActiveSubscriptions.WithLabelValues(direction).Add(delta)
	}
}

func (ms *meters) publishers(delta float64) {
	if ms != nil && ms.m.Publishers != nil {
		ms.m.Publishers.Add(delta)
	}
}

func (ms *meters) drop(reason string) {
	if ms != nil {
		ms.m.DroppedObjects.WithLabelValues(reason).Inc()
	}
}

func (ms *meters) fetched(source string, n int) {
	if ms != nil && n > 0 {
		ms.m.FetchObjects.WithLabelValues(source).Add(float64(n))
	}
}

func (ms *meters) liveEdge(n int64) {
	if ms != nil {
		ms.m.LiveEdgeBytes.Set(float64(n))
	}
}

func (ms *meters) fail(class string) {
	if ms != nil {
		ms.m.Errors.WithLabelValues(class).Inc()
	}
}
