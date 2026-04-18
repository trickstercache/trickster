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

package listener

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/connhandler"
	"golang.org/x/net/netutil"
)

// ProtocolListener accepts raw TCP connections and dispatches them to a
// connhandler.ConnectionHandler. It mirrors the lifecycle semantics of the HTTP Listener
// (state machine, readiness signaling, graceful drain) without wrapping an
// http.Server.
type ProtocolListener struct {
	listener net.Listener
	handler  connhandler.ConnectionHandler

	state     atomic.Int32
	readyCh   chan struct{}
	readyOnce sync.Once

	activeConns sync.WaitGroup
	cancel      context.CancelFunc
}

// StartProtocolListener creates a raw TCP listener, registers it with the
// Group, and runs the accept loop. It blocks until the listener is closed.
func (lg *Group) StartProtocolListener(
	name, address string, port, connectionsLimit int,
	handler connhandler.ConnectionHandler, errorFunc func(),
) error {
	pl := &ProtocolListener{
		handler: handler,
		readyCh: make(chan struct{}),
	}
	pl.setState(StateStarting)

	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", address, port))
	if err != nil {
		logger.ErrorSynchronous("protocol listener startup failed",
			logging.Pairs{"listenerName": name, "detail": err})
		pl.setState(StateStopped)
		if errorFunc != nil {
			errorFunc()
		}
		return err
	}
	if connectionsLimit > 0 {
		ln = netutil.LimitListener(ln, connectionsLimit)
	}
	pl.listener = ln

	logger.Info("protocol listener starting",
		logging.Pairs{"listenerName": name, "port": port, "address": address})

	lg.listenersLock.Lock()
	lg.protocolMembers[name] = pl
	lg.listenersLock.Unlock()

	pl.setState(StateReady)
	pl.readyOnce.Do(func() { close(pl.readyCh) })

	ctx, cancel := context.WithCancel(context.Background())
	pl.cancel = cancel

	defer func() {
		cancel()
		pl.activeConns.Wait()
		pl.setState(StateStopped)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			logger.Error("protocol listener accept error",
				logging.Pairs{"listenerName": name, "detail": err})
			if errorFunc != nil {
				errorFunc()
			}
			return err
		}
		metrics.ProxyConnectionRequested.Inc()
		metrics.ProxyConnectionAccepted.Inc()
		metrics.ProxyActiveConnections.Inc()

		pl.activeConns.Add(1)
		go func() {
			defer func() {
				_ = conn.Close()
				metrics.ProxyActiveConnections.Dec()
				metrics.ProxyConnectionClosed.Inc()
				pl.activeConns.Done()
			}()
			if err := handler.HandleConnection(ctx, conn); err != nil {
				logger.Debug("protocol connection handler error",
					logging.Pairs{"listenerName": name, "detail": err})
			}
		}()
	}
}

// DrainAndCloseProtocol drains and closes the named protocol listener.
func (lg *Group) DrainAndCloseProtocol(name string, drainWait time.Duration) error {
	lg.listenersLock.Lock()
	pl, ok := lg.protocolMembers[name]
	if !ok || pl == nil {
		lg.listenersLock.Unlock()
		return nil
	}
	delete(lg.protocolMembers, name)
	lg.listenersLock.Unlock()

	pl.setState(StateStopping)
	if pl.cancel != nil {
		pl.cancel()
	}
	if pl.listener != nil {
		_ = pl.listener.Close()
	}

	done := make(chan struct{})
	go func() {
		pl.activeConns.Wait()
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), drainWait)
	defer cancel()
	select {
	case <-done:
	case <-ctx.Done():
	}
	pl.setState(StateStopped)
	return nil
}

func (pl *ProtocolListener) setState(state ListenerState) {
	pl.state.Store(int32(state))
}

// WaitForReady waits for the protocol listener to become ready.
func (pl *ProtocolListener) WaitForReady(timeout time.Duration) bool {
	if pl.readyCh == nil {
		return ListenerState(pl.state.Load()) == StateReady
	}
	if timeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		select {
		case <-pl.readyCh:
			return true
		case <-ctx.Done():
			return false
		}
	}
	<-pl.readyCh
	return true
}
