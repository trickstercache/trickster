/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package listeners

import (
	"net"

	"github.com/tricksterproxy/trickster/pkg/util/metrics"
)

type connectionsLimitObProxy struct {
	net.Listener
}

// Accept implements Listener.Accept
func (l *connectionsLimitObProxy) Accept() (net.Conn, error) {

	metrics.ProxyConnectionRequested.Inc()

	c, err := l.Listener.Accept()
	if err != nil {
		metrics.ProxyConnectionFailed.Inc()
		return c, err
	}

	metrics.ProxyActiveConnections.Inc()
	metrics.ProxyConnectionAccepted.Inc()

	return observedConnection{c}, nil
}

type observedConnection struct {
	net.Conn
}

func (o observedConnection) Close() error {
	err := o.Conn.Close()

	metrics.ProxyActiveConnections.Dec()
	metrics.ProxyConnectionClosed.Inc()

	return err
}
