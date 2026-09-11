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
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

type protocolTLSUpdater interface {
	UpdateTLSConfig(*tls.Config)
}

type protocolRouteUpdater interface {
	UpdateRouteResolver(backends.RouteResolver)
}

type protocolRestartKeyer interface {
	ProtocolRestartKey() string
}

// ProtocolServer is the lifecycle surface implemented by native protocol
// servers. Protocol adapters construct these without exposing provider details
// to listener setup.
type ProtocolServer interface {
	server
	Serve(net.Listener) error
}

// StartProtocolListener starts a protocol-terminating server on a Trickster
// listener, preserving the common connection limit, metrics, and drain lifecycle.
func (lg *Group) StartProtocolListener(listenerName, protocol, address string,
	port, connectionsLimit int, svr ProtocolServer, f func(), drainTimeout time.Duration,
) error {
	l := &Listener{readyCh: make(chan struct{}), server: svr}
	l.exitOnError.Store(f != nil)
	l.setState(StateStarting)

	var err error
	l.Listener, err = NewListener(address, port, connectionsLimit, nil, drainTimeout)
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
	logger.Info(protocol+" listener starting", logging.Pairs{
		logKeyListenerName: listenerName, logKeyPort: port, logKeyAddress: address,
	})

	lg.listenersLock.Lock()
	lg.members[listenerName] = l
	lg.listenersLock.Unlock()
	l.setState(StateReady)
	l.markReady()

	err = svr.Serve(l)
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

// UpdateProtocolTLSConfig rotates an in-band protocol server certificate.
func (lg *Group) UpdateProtocolTLSConfig(listenerName string, config *tls.Config) bool {
	l := lg.Get(listenerName)
	if l == nil || l.server == nil {
		return false
	}
	updater, ok := l.server.(protocolTLSUpdater)
	if !ok {
		return false
	}
	updater.UpdateTLSConfig(config)
	return true
}

// UpdateProtocolRouteResolver switches the resolver used for new native
// sessions without disturbing established connections.
func (lg *Group) UpdateProtocolRouteResolver(listenerName string, resolver backends.RouteResolver) bool {
	l := lg.Get(listenerName)
	if l == nil || l.server == nil {
		return false
	}
	updater, ok := l.server.(protocolRouteUpdater)
	if !ok {
		return false
	}
	updater.UpdateRouteResolver(resolver)
	return true
}

// ProtocolRestartKey returns the immutable runtime identity of a protocol server.
func (lg *Group) ProtocolRestartKey(listenerName string) (string, bool) {
	l := lg.Get(listenerName)
	if l == nil || l.server == nil {
		return "", false
	}
	keyer, ok := l.server.(protocolRestartKeyer)
	if !ok {
		return "", false
	}
	return keyer.ProtocolRestartKey(), true
}

// UpdateProtocolHandler updates a native protocol bridge without replacing its listener.
func (lg *Group) UpdateProtocolHandler(name string, h http.Handler) bool {
	l := lg.Get(name)
	if l == nil || l.server == nil {
		return false
	}
	updater, ok := l.server.(interface{ UpdateHandler(http.Handler) })
	if !ok {
		return false
	}
	updater.UpdateHandler(h)
	return true
}
