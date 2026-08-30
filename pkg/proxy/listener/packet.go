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

package listener

import (
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/switcher"
)

// PacketServer is the lifecycle surface implemented by datagram-based servers
// such as QUIC, which bind a UDP socket rather than accepting TCP connections.
type PacketServer interface {
	server
	Serve(net.PacketConn) error
}

// NewPacketListener binds a UDP socket for a datagram protocol. Connection
// limiting does not apply: QUIC multiplexes every peer over one socket, so
// there are no accepted connections to count.
func NewPacketListener(listenAddress string, listenPort int) (net.PacketConn, error) {
	conn, err := net.ListenPacket("udp", fmt.Sprintf("%s:%d", listenAddress, listenPort))
	if err != nil {
		return nil, err
	}
	logger.Debug("starting proxy packet listener", logging.Pairs{
		"scheme":      "udp",
		logKeyAddress: listenAddress,
		logKeyPort:    listenPort,
	})
	return conn, nil
}

// StartPacketListener starts a datagram server on a Trickster-managed UDP
// socket, joining the same group membership and drain lifecycle as the TCP
// listeners so reloads and shutdown treat every endpoint uniformly.
// build receives the listener's route swapper so a config reload can replace
// the served routes without rebinding the socket.
func (lg *Group) StartPacketListener(listenerName, protocol, address string,
	port int, router http.Handler, build func(http.Handler) PacketServer, f func(),
) error {
	swapper := switcher.NewSwitchHandler(router)
	svr := build(swapper)
	l := &Listener{
		readyCh:      make(chan struct{}),
		server:       svr,
		routeSwapper: swapper,
	}
	l.exitOnError.Store(f != nil)
	l.setState(StateStarting)

	conn, err := NewPacketListener(address, port)
	if err != nil {
		logger.ErrorSynchronous(protocol+" listener startup failed", logging.Pairs{
			logKeyListenerName: listenerName, logKeyDetail: err,
		})
		l.setState(StateStopped)
		if f != nil {
			f()
		}
		return err
	}
	l.packetConn = conn

	logger.Info(protocol+" listener starting", logging.Pairs{
		logKeyListenerName: listenerName, logKeyPort: port, logKeyAddress: address,
	})

	lg.listenersLock.Lock()
	lg.members[listenerName] = l
	lg.listenersLock.Unlock()
	l.setState(StateReady)
	l.markReady()

	err = svr.Serve(conn)
	if err != nil {
		logger.ErrorSynchronous(protocol+" listener stopping", logging.Pairs{
			logKeyListenerName: listenerName, logKeyDetail: err,
		})
		if l.exitOnError.Load() {
			os.Exit(1)
		}
	}
	return err
}
