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
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	trerr "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/switcher"
	sw "github.com/trickstercache/trickster/v2/pkg/proxy/tls"

	"golang.org/x/net/netutil"
)

// ListenerState represents the state of a listener
type ListenerState int32

const (
	// StateStopped indicates the listener is not running
	StateStopped ListenerState = iota
	// StateStarting indicates the listener is starting up
	StateStarting
	// StateReady indicates the listener is ready to accept connections
	StateReady
	// StateStopping indicates the listener is shutting down
	StateStopping
)

// Listener is the Trickster net.Listener implmementation
type Listener struct {
	net.Listener
	tlsConfig    *tls.Config
	tlsSwapper   sw.CertSwapper
	routeSwapper *switcher.SwitchHandler
	server       *http.Server
	exitOnError  bool
	state        int32
	readyCh      chan struct{}
	readyOnce    sync.Once
}

type observedConnection struct {
	*net.TCPConn
}

func (o *observedConnection) Close() error {
	err := o.TCPConn.Close()
	metrics.ProxyActiveConnections.Dec()
	metrics.ProxyConnectionClosed.Inc()
	return err
}

// Accept implements Listener.Accept
func (l *Listener) Accept() (net.Conn, error) {

	metrics.ProxyConnectionRequested.Inc()

	c, err := l.Listener.Accept()
	if err != nil {
		metrics.ProxyConnectionFailed.Inc()
		return c, err
	}

	metrics.ProxyActiveConnections.Inc()
	metrics.ProxyConnectionAccepted.Inc()

	// this is necessary for HTTP/2 to work
	if t, ok := c.(*net.TCPConn); ok {
		return &observedConnection{t}, nil
	}

	return c, nil
}

// CertSwapper returns the CertSwapper reference from the Listener
func (l *Listener) CertSwapper() sw.CertSwapper {
	return l.tlsSwapper
}

// RouteSwapper returns the RouteSwapper reference from the Listener
func (l *Listener) RouteSwapper() *switcher.SwitchHandler {
	return l.routeSwapper
}

// Group is a collection of listeners
type Group struct {
	members        map[string]*Listener
	listenersLock  sync.Mutex
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
}

// NewGroup returns a new Group
func NewGroup() *Group {
	ctx, cancel := context.WithCancel(context.Background())
	return &Group{
		members:        make(map[string]*Listener),
		shutdownCtx:    ctx,
		shutdownCancel: cancel,
	}
}

// State returns the current state of the listener
func (l *Listener) State() ListenerState {
	return ListenerState(atomic.LoadInt32(&l.state))
}

// setState atomically sets the listener state
func (l *Listener) setState(state ListenerState) {
	atomic.StoreInt32(&l.state, int32(state))
}

// markReady signals that the listener is ready to accept connections
func (l *Listener) markReady() {
	l.readyOnce.Do(func() {
		if l.readyCh != nil {
			close(l.readyCh)
		}
	})
}

// WaitForReady waits for the listener to become ready, with optional timeout
func (l *Listener) WaitForReady(timeout time.Duration) bool {
	if l.readyCh == nil {
		return l.State() == StateReady
	}
	if timeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		select {
		case <-l.readyCh:
			return true
		case <-ctx.Done():
			return false
		}
	}
	<-l.readyCh
	return true
}

// NewListener creates a new network listener which obeys to the configuration max
// connection limit, monitors connections with prometheus metrics, and is able
// to be gracefully drained
//
// The way this works is by creating a listener and wrapping it with a
// netutil.LimitListener to set a limit.
//
// This limiter will simply block waiting for resources to become available
// whenever clients go above the limit.
//
// To simplify settings limits the listener is wrapped with yet another object
// which observes the connections to set a gauge with the current number of
// connections (with operates with sampling through scrapes), and a set of
// counter metrics for connections accepted, rejected and closed.
func NewListener(listenAddress string, listenPort, connectionsLimit int,
	tlsConfig *tls.Config, _ time.Duration) (net.Listener, error) {

	var listener net.Listener
	var err error

	listenerType := "http"

	if tlsConfig != nil {
		listenerType = "https"
		listener, err = tls.Listen("tcp", fmt.Sprintf("%s:%d", listenAddress, listenPort), tlsConfig)
	} else {
		listener, err = net.Listen("tcp", fmt.Sprintf("%s:%d", listenAddress, listenPort))
	}
	if err != nil {
		// so we can exit one level above, this usually means that the port is in use
		return nil, err
	}

	if connectionsLimit > 0 {
		listener = netutil.LimitListener(listener, connectionsLimit)
		metrics.ProxyMaxConnections.Set(float64(connectionsLimit))
	}

	logger.Debug("starting proxy listener", logging.Pairs{
		"connectionsLimit": connectionsLimit,
		"scheme":           listenerType,
		"address":          listenAddress,
		"port":             listenPort,
	})

	return listener, nil

}

// Get returns the listener if it exists
func (lg *Group) Get(name string) *Listener {
	lg.listenersLock.Lock()
	l, ok := lg.members[name]
	lg.listenersLock.Unlock()
	if ok {
		return l
	}
	return nil
}

// StartListener starts a new HTTP listener and adds it to the listener group
func (lg *Group) StartListener(listenerName, address string, port int, connectionsLimit int,
	tlsConfig *tls.Config, router http.Handler, tracers tracing.Tracers,
	f func(), drainTimeout, readHeaderTimeout time.Duration) error {
	l := &Listener{
		routeSwapper: switcher.NewSwitchHandler(router),
		exitOnError:  f != nil,
		readyCh:      make(chan struct{}),
	}
	l.setState(StateStarting)

	if tlsConfig != nil && len(tlsConfig.Certificates) > 0 {
		l.tlsConfig = tlsConfig
		l.tlsSwapper = sw.NewSwapper(tlsConfig.Certificates)
		// Replace the normal GetCertificate function in the TLS config with lg.tlsSwapper's,
		// so users swap certs in the config later without restarting the entire process
		tlsConfig.GetCertificate = l.tlsSwapper.GetCert
		tlsConfig.Certificates = nil
	}

	var err error
	l.Listener, err = NewListener(address, port, connectionsLimit, tlsConfig, drainTimeout)
	if err != nil {
		logger.ErrorSynchronous(
			"http listener startup failed", logging.Pairs{"listenerName": listenerName, "detail": err})
		l.setState(StateStopped)
		if f != nil {
			f()
		}
		return err
	}
	logger.Info("http listener starting",
		logging.Pairs{"listenerName": listenerName, "port": port, "address": address})

	lg.listenersLock.Lock()
	lg.members[listenerName] = l
	lg.listenersLock.Unlock()

	// Mark as ready once listener is created and added
	l.setState(StateReady)
	l.markReady()

	// defer the tracer flush here where the listener connection ends
	defer handleTracerShutdowns(tracers)

	if tlsConfig != nil {
		svr := &http.Server{
			Handler:           l.routeSwapper,
			TLSConfig:         tlsConfig,
			ReadHeaderTimeout: readHeaderTimeout,
		}
		l.server = svr
		err = svr.Serve(l)
		if err != nil {
			logger.ErrorSynchronous(
				"https listener stopping", logging.Pairs{"listenerName": listenerName, "detail": err})
			if l.exitOnError {
				defer func() {
					os.Exit(1) // exit via defer to allow prior defers to run
				}()
				return nil
			}
		}
		return err
	}

	svr := &http.Server{
		Handler:           l.routeSwapper,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	l.server = svr
	err = svr.Serve(l)
	if err != nil {
		logger.ErrorSynchronous("http listener stopping",
			logging.Pairs{"listenerName": listenerName, "detail": err})
		if l.exitOnError {
			defer func() {
				os.Exit(1) // exit via defer to allow prior defers to run
			}()
		}
	}
	return err
}

func handleTracerShutdowns(tracers tracing.Tracers) {
	for _, v := range tracers {
		if v == nil || v.ShutdownFunc == nil {
			continue
		}
		err := v.ShutdownFunc(context.Background())
		if err != nil {
			logger.Error("tracer shutdown failed",
				logging.Pairs{"detail": err.Error()})
		}
	}
}

// StartListenerRouter starts a new HTTP listener with a new router, and adds it to the listener group
func (lg *Group) StartListenerRouter(listenerName, address string, port int, connectionsLimit int,
	tlsConfig *tls.Config, path string, handler http.Handler,
	tracers tracing.Tracers, f func(), drainTimeout, readHeaderTimeout time.Duration) error {
	router := http.NewServeMux()
	router.Handle(path, handler)
	return lg.StartListener(listenerName, address, port, connectionsLimit,
		tlsConfig, router, tracers, f, drainTimeout, readHeaderTimeout)
}

// DrainAndClose drains and closes the named listener
func (lg *Group) DrainAndClose(listenerName string, drainWait time.Duration) error {
	lg.listenersLock.Lock()
	l, ok := lg.members[listenerName]
	if !ok || l == nil {
		lg.listenersLock.Unlock()
		return trerr.ErrNoSuchListener
	}
	l.exitOnError = false
	l.setState(StateStopping)
	delete(lg.members, listenerName)
	lg.listenersLock.Unlock()

	if l.Listener == nil {
		l.setState(StateStopped)
		return trerr.ErrNilListener
	}

	ctx, cancel := context.WithTimeout(context.Background(), drainWait)
	defer cancel()

	if l.server != nil {
		err := l.server.Shutdown(ctx)
		if err != nil {
			l.setState(StateStopped)
			return err
		}
	}
	l.setState(StateStopped)
	return nil
}

// WaitForReady waits for all listeners in the group to become ready
func (lg *Group) WaitForReady(timeout time.Duration) error {
	lg.listenersLock.Lock()
	listeners := make([]*Listener, 0, len(lg.members))
	for _, l := range lg.members {
		if l != nil {
			listeners = append(listeners, l)
		}
	}
	lg.listenersLock.Unlock()

	if len(listeners) == 0 {
		return nil
	}

	done := make(chan struct{})
	go func() {
		for _, l := range listeners {
			l.WaitForReady(0)
		}
		close(done)
	}()

	if timeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return errors.New("timeout waiting for listeners to become ready")
		}
	}

	<-done
	return nil
}

// Shutdown gracefully shuts down all listeners in the group
func (lg *Group) Shutdown(drainWait time.Duration) error {
	lg.listenersLock.Lock()
	names := make([]string, 0, len(lg.members))
	for name := range lg.members {
		names = append(names, name)
	}
	lg.listenersLock.Unlock()

	var firstErr error
	for _, name := range names {
		if err := lg.DrainAndClose(name, drainWait); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	lg.shutdownCancel()
	return firstErr
}

// UpdateFrontendRouters will swap out the routers across the named Listeners with the provided ones
func (lg *Group) UpdateFrontendRouters(mainRouter http.Handler, adminRouter http.Handler) {
	lg.listenersLock.Lock()
	defer lg.listenersLock.Unlock()
	if mainRouter != nil {
		for k, v := range lg.members {
			if k == "httpListener" || k == "tlsListener" {
				v.routeSwapper.Update(mainRouter)
				break
			}
		}
	}
	if v, ok := lg.members["mgmtListener"]; ok && adminRouter != nil {
		v.routeSwapper.Update(adminRouter)
	}
}

// UpdateRouter will swap out the router for the Group with the provided name
func (lg *Group) UpdateRouter(routerName string, router http.Handler) {
	lg.listenersLock.Lock()
	if r, ok := lg.members[routerName]; ok {
		r.routeSwapper.Update(router)
	}
	defer lg.listenersLock.Unlock()
}
