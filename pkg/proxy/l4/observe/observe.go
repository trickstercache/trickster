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
// Package observe binds the stream relay's events to Trickster's metrics.
package observe

import (
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"

	"github.com/prometheus/client_golang/prometheus"
)

// listener meters one stream listener. The series touched per connection and per datagram
// are resolved once, here, so the relay's hot path is a plain add with no label lookup.
type listener struct {
	name, protocol string
	active         prometheus.Gauge
	in, out        prometheus.Counter
}

// Listener returns the observer for the named stream listener.
func Listener(name, protocol string) l4.Observer {
	return &listener{
		name: name, protocol: protocol,
		active: metrics.ProxyStreamActiveConnections.WithLabelValues(name, protocol),
		in:     metrics.ProxyStreamBytes.WithLabelValues(name, protocol, l4.DirectionIn),
		out:    metrics.ProxyStreamBytes.WithLabelValues(name, protocol, l4.DirectionOut),
	}
}

func (l *listener) Opened() { l.active.Inc() }

func (l *listener) Ended() { l.active.Dec() }

func (l *listener) Result(result string) {
	metrics.ProxyStreamConnections.WithLabelValues(l.name, l.protocol, result).Inc()
}

func (l *listener) Bytes(direction string, n int64) {
	if direction == l4.DirectionIn {
		l.in.Add(float64(n))
		return
	}
	l.out.Add(float64(n))
}

func (l *listener) Dropped(reason string) {
	metrics.ProxyStreamDroppedDatagrams.WithLabelValues(l.name, reason).Inc()
}
