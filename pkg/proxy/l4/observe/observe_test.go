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
package observe

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestListenerMetersTheRelay(t *testing.T) {
	const name = "observe-test"
	o := Listener(name, l4.ProtocolUDP)
	active := metrics.ProxyStreamActiveConnections.WithLabelValues(name, l4.ProtocolUDP)
	in := metrics.ProxyStreamBytes.WithLabelValues(name, l4.ProtocolUDP, l4.DirectionIn)
	out := metrics.ProxyStreamBytes.WithLabelValues(name, l4.ProtocolUDP, l4.DirectionOut)
	proxied := metrics.ProxyStreamConnections.WithLabelValues(name, l4.ProtocolUDP, l4.ResultProxied)
	dropped := metrics.ProxyStreamDroppedDatagrams.WithLabelValues(name, l4.DropQueueFull)

	o.Opened()
	o.Opened()
	o.Ended()
	o.Result(l4.ResultProxied)
	o.Bytes(l4.DirectionIn, 100)
	o.Bytes(l4.DirectionIn, 20)
	o.Bytes(l4.DirectionOut, 7)
	o.Dropped(l4.DropQueueFull)
	for series, want := range map[string][2]float64{
		"active":  {testutil.ToFloat64(active), 1},
		"in":      {testutil.ToFloat64(in), 120},
		"out":     {testutil.ToFloat64(out), 7},
		"proxied": {testutil.ToFloat64(proxied), 1},
		"dropped": {testutil.ToFloat64(dropped), 1},
	} {
		if want[0] != want[1] {
			t.Errorf("%s = %v, want %v", series, want[0], want[1])
		}
	}
}
