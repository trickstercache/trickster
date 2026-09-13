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
	"slices"
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

// serverProtocols enables cleartext HTTP/2 alongside HTTP/1.1 and TLS HTTP/2.
// h2c is prior-knowledge only (the client sends the HTTP/2 preface), which is
// what gRPC and other cleartext HTTP/2 clients require; Go does not implement
// the Upgrade-based h2c handshake, so no request can be smuggled through one.
func serverProtocols() *http.Protocols {
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetHTTP2(true)
	p.SetUnencryptedHTTP2(true)
	return &p
}

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

const (
	logKeyDetail       = "detail"
	logKeyAddress      = "address"
	logKeyListenerName = "listenerName"
	logKeyPort         = "port"
)

// server is the common surface of http.Server and tcpProxyServer needed for
// graceful shutdown of a Listener.
type server interface {
	Shutdown(context.Context) error
}

// Listener is the Trickster net.Listener implmementation
type Listener struct {
	net.Listener
	// packetConn is set instead of Listener for datagram (QUIC) endpoints
	packetConn   net.PacketConn
	tlsConfig    *tls.Config
	tlsSwapper   sw.CertSwapper
	routeSwapper *switcher.SwitchHandler
	server       server
	exitOnError  atomic.Bool
	state        atomic.Int32
	readyCh      chan struct{}
	readyOnce    sync.Once
}

type observedConnection struct {
	net.Conn
}

// CloseWrite ends the write side alone where the wrapped connection can, so a relay may
// half-close as TCP allows; a PROXY protocol connection exposes its TCP connection for it.
func (o *observedConnection) CloseWrite() error {
	if hc, ok := o.Conn.(interface{ CloseWrite() error }); ok {
		return hc.CloseWrite()
	}
	if tc, ok := o.Conn.(interface{ TCPConn() (*net.TCPConn, bool) }); ok {
		if c, ok := tc.TCPConn(); ok {
			return c.CloseWrite()
		}
	}
	return errors.ErrUnsupported
}

func (o *observedConnection) Close() error {
	if err := o.Conn.Close(); err != nil {
		return err
	}
	// Only the first successful Close adjusts the gauge; a subsequent Close
	// returns an error (net/http may close the same conn more than once).
	metrics.ProxyActiveConnections.Dec()
	metrics.ProxyConnectionClosed.Inc()
	return nil
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
	// *tls.Conn is left unwrapped so http.Server can type-assert it for
	// HTTP/2 (ALPN); every other conn -- including *net.TCPConn and the
	// netutil.LimitListener wrapper -- is wrapped so Close decrements
	// ProxyActiveConnections.
	if _, ok := c.(*tls.Conn); ok {
		return c, nil
	}

	return &observedConnection{Conn: c}, nil
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
	members       map[string]*Listener
	listenersLock sync.Mutex
	done          chan struct{}
	// closed is set when shutdown begins; later starts are refused so a
	// reload racing the shutdown can never publish a listener it misses
	closed bool
	// onPublish is told the key of every listener added, so state prepared for a listener
	// before it existed, such as its certificates, can be applied once it does
	onPublish func(key string)
}

// OnPublish registers f to be called with the group key of every listener published from now on
func (lg *Group) OnPublish(f func(key string)) {
	lg.listenersLock.Lock()
	lg.onPublish = f
	lg.listenersLock.Unlock()
}

// Closed reports whether shutdown has begun for the group.
func (lg *Group) Closed() bool {
	if lg == nil {
		return true
	}
	lg.listenersLock.Lock()
	defer lg.listenersLock.Unlock()
	return lg.closed
}

// publish adds l to the group under name, or refuses it once shutdown has
// begun; the refusal is decided under the same lock as the shutdown snapshot.
func (lg *Group) publish(name string, l *Listener) error {
	lg.listenersLock.Lock()
	if lg.closed {
		lg.listenersLock.Unlock()
		return trerr.ErrListenerGroupClosed
	}
	lg.members[name] = l
	f := lg.onPublish
	lg.listenersLock.Unlock()
	if f != nil {
		f(name)
	}
	return nil
}

// refuse closes a bound but unpublished listener and logs the refusal.
func (l *Listener) refuse(listenerName string) {
	if l.Listener != nil {
		_ = l.Listener.Close()
	}
	if l.packetConn != nil {
		_ = l.packetConn.Close()
	}
	l.setState(StateStopped)
	logger.Warn("listener start refused during shutdown",
		logging.Pairs{logKeyListenerName: listenerName})
}

// NewGroup returns a new Group
func NewGroup() *Group {
	return &Group{
		members: make(map[string]*Listener),
		done:    make(chan struct{}),
	}
}

// State returns the current state of the listener
func (l *Listener) State() ListenerState {
	return ListenerState(l.state.Load())
}

// setState atomically sets the listener state
func (l *Listener) setState(state ListenerState) {
	l.state.Store(int32(state))
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
	tlsConfig *tls.Config, proxyProtocol *ProxyProtocolOptions,
) (net.Listener, error) {
	listenerType := "http"
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", listenAddress, listenPort))
	if err != nil {
		// so we can exit one level above, this usually means that the port is in use
		return nil, err
	}
	// the PROXY header precedes the TLS handshake, so it is read beneath TLS
	if proxyProtocol != nil && proxyProtocol.Enabled {
		listener = proxyProtocol.wrap(listener)
	}
	if tlsConfig != nil {
		listenerType = "https"
		listener = tls.NewListener(listener, tlsConfig)
	}

	if connectionsLimit > 0 {
		listener = netutil.LimitListener(listener, connectionsLimit)
		metrics.ProxyMaxConnections.Set(float64(connectionsLimit))
	}

	logger.Debug("starting proxy listener", logging.Pairs{
		"connectionsLimit": connectionsLimit,
		"scheme":           listenerType,
		logKeyAddress:      listenAddress,
		logKeyPort:         listenPort,
	})

	return listener, nil
}

// GroupKey returns the Group membership key for a listener endpoint
func GroupKey(listenerName, protocol string, isTLS bool) string {
	scheme := protocol
	if scheme == "" {
		scheme = "http"
	}
	if isTLS {
		scheme = "https"
	}
	return fmt.Sprintf("listener.%s.%s", listenerName, scheme)
}

// Keys returns the keys of all current group members
func (lg *Group) Keys() []string {
	lg.listenersLock.Lock()
	out := make([]string, 0, len(lg.members))
	for name := range lg.members {
		out = append(out, name)
	}
	lg.listenersLock.Unlock()
	slices.Sort(out)
	return out
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
	f func(), readHeaderTimeout time.Duration, proxyProtocol *ProxyProtocolOptions,
) error {
	l := &Listener{
		routeSwapper: switcher.NewSwitchHandler(router),
		readyCh:      make(chan struct{}),
	}
	l.exitOnError.Store(f != nil)
	l.setState(StateStarting)

	if tlsConfig != nil {
		// the store may start empty for a listener whose certificates arrive at
		// runtime; handshakes fail until the first entry is set
		l.tlsConfig = tlsConfig
		l.tlsSwapper = sw.NewSwapper(tlsConfig.Certificates)
		// Replace the normal GetCertificate function in the TLS config with lg.tlsSwapper's,
		// so users swap certs in the config later without restarting the entire process
		tlsConfig.GetCertificate = l.tlsSwapper.GetCert
		tlsConfig.Certificates = nil
	}

	var err error
	l.Listener, err = NewListener(address, port, connectionsLimit, tlsConfig, proxyProtocol)
	if err != nil {
		logger.ErrorSynchronous(
			"http listener startup failed", logging.Pairs{logKeyListenerName: listenerName, logKeyDetail: err})
		l.setState(StateStopped)
		if f != nil {
			f()
		}
		return err
	}
	logger.Info("http listener starting",
		logging.Pairs{logKeyListenerName: listenerName, logKeyPort: port, logKeyAddress: address})

	// the server is assigned before the listener is published to the group, so
	// a DrainAndClose racing this startup always observes a server to shut down
	svr := &http.Server{
		Handler:           l.routeSwapper,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: readHeaderTimeout,
		Protocols:         serverProtocols(),
	}
	l.server = svr

	if err := lg.publish(listenerName, l); err != nil {
		l.refuse(listenerName)
		return err
	}

	// Mark as ready once listener is created and added
	l.setState(StateReady)
	l.markReady()

	// defer the tracer flush here where the listener connection ends
	defer handleTracerShutdowns(tracers)

	err = svr.Serve(l)
	if err != nil {
		event := "http listener stopping"
		if tlsConfig != nil {
			event = "https listener stopping"
		}
		logger.ErrorSynchronous(event,
			logging.Pairs{logKeyListenerName: listenerName, logKeyDetail: err})
		if l.exitOnError.Load() {
			defer func() {
				os.Exit(1) // exit via defer to allow prior defers to run
			}()
			if tlsConfig != nil {
				return nil
			}
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
				logging.Pairs{logKeyDetail: err.Error()})
		}
	}
}

// StartListenerRouter starts a new HTTP listener with a new router, and adds it to the listener group
func (lg *Group) StartListenerRouter(listenerName, address string, port int, connectionsLimit int,
	tlsConfig *tls.Config, path string, handler http.Handler,
	tracers tracing.Tracers, f func(), readHeaderTimeout time.Duration,
) error {
	router := http.NewServeMux()
	router.Handle(path, handler)
	return lg.StartListener(listenerName, address, port, connectionsLimit,
		tlsConfig, router, tracers, f, readHeaderTimeout, nil)
}

// DrainAndClose drains the named listener for up to drainWait, then closes it.
func (lg *Group) DrainAndClose(listenerName string, drainWait time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), drainWait)
	defer cancel()
	return lg.DrainAndCloseContext(ctx, listenerName)
}

// DrainAndCloseContext stops the named listener from accepting, waits for its
// in-flight requests until ctx is done, then closes any remaining connections.
func (lg *Group) DrainAndCloseContext(ctx context.Context, listenerName string) error {
	lg.listenersLock.Lock()
	l, ok := lg.members[listenerName]
	if !ok || l == nil {
		lg.listenersLock.Unlock()
		return trerr.ErrNoSuchListener
	}
	l.exitOnError.Store(false)
	l.setState(StateStopping)
	delete(lg.members, listenerName)
	lg.listenersLock.Unlock()
	defer l.setState(StateStopped)

	if l.Listener == nil && l.packetConn == nil {
		return trerr.ErrNilListener
	}
	if l.server == nil {
		return nil
	}
	err := l.server.Shutdown(ctx)
	if err == nil {
		return nil
	}
	// Shutdown leaves active connections open when ctx expires; close them so
	// the drain deadline is honored rather than advisory.
	if closer, ok := l.server.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	return err
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

// Shutdown drains every listener in the group concurrently for up to drainWait.
func (lg *Group) Shutdown(drainWait time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), drainWait)
	defer cancel()
	return lg.ShutdownContext(ctx)
}

// ShutdownContext drains every listener in the group concurrently until ctx is
// done, then closes whatever is still open. Errors from all listeners are joined.
func (lg *Group) ShutdownContext(ctx context.Context) error {
	lg.listenersLock.Lock()
	lg.closed = true
	names := make([]string, 0, len(lg.members))
	for name := range lg.members {
		names = append(names, name)
	}
	lg.listenersLock.Unlock()

	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			errs[i] = lg.DrainAndCloseContext(ctx, name)
		})
	}
	wg.Wait()

	select {
	case <-lg.done:
	default:
		close(lg.done)
	}
	return errors.Join(errs...)
}

// Serving reports whether the group has at least one listener and every
// listener is accepting connections.
func (lg *Group) Serving() bool {
	if lg == nil {
		return false
	}
	lg.listenersLock.Lock()
	defer lg.listenersLock.Unlock()
	if len(lg.members) == 0 {
		return false
	}
	for _, l := range lg.members {
		if l == nil || l.State() != StateReady {
			return false
		}
	}
	return true
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
	if r, ok := lg.members[routerName]; ok && r.routeSwapper != nil {
		r.routeSwapper.Update(router)
	}
	defer lg.listenersLock.Unlock()
}
