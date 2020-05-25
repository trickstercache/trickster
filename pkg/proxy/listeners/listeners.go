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
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/tricksterproxy/trickster/pkg/proxy/errors"
	ph "github.com/tricksterproxy/trickster/pkg/proxy/handlers"
	sw "github.com/tricksterproxy/trickster/pkg/proxy/tls"
	"github.com/tricksterproxy/trickster/pkg/tracing"
	tl "github.com/tricksterproxy/trickster/pkg/util/log"
	"github.com/tricksterproxy/trickster/pkg/util/metrics"
	"golang.org/x/net/netutil"

	"github.com/gorilla/handlers"
)

// Listener is the Trickster net.Listener implmementation
type Listener struct {
	net.Listener
	tlsConfig    *tls.Config
	tlsSwapper   *sw.CertSwapper
	routeSwapper *ph.SwitchHandler
	server       *http.Server
	exitOnError  bool
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

	return observedConnection{c}, nil
}

// CertSwapper returns the CertSwapper reference from the Listener
func (l *Listener) CertSwapper() *sw.CertSwapper {
	return l.tlsSwapper
}

// RouteSwapper returns the RouteSwapper reference from the Listener
func (l *Listener) RouteSwapper() *ph.SwitchHandler {
	return l.routeSwapper
}

// ListenerGroup is a collection of listeners
type ListenerGroup struct {
	members       map[string]*Listener
	listenersLock sync.Mutex
}

// NewListenerGroup returns a new ListenerGroup
func NewListenerGroup() *ListenerGroup {
	return &ListenerGroup{
		members: make(map[string]*Listener),
	}
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
	tlsConfig *tls.Config, drainTimeout time.Duration, log *tl.Logger) (net.Listener, error) {

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
		listener = &connectionsLimitObProxy{
			netutil.LimitListener(listener, connectionsLimit),
		}
		metrics.ProxyMaxConnections.Set(float64(connectionsLimit))
	}

	log.Debug("starting proxy listener", tl.Pairs{
		"connectionsLimit": connectionsLimit,
		"scheme":           listenerType,
		"address":          listenAddress,
		"port":             listenPort,
	})

	return listener, nil

}

// Get returns the listener if it exists
func (lg *ListenerGroup) Get(name string) *Listener {
	lg.listenersLock.Lock()
	l, ok := lg.members[name]
	lg.listenersLock.Unlock()
	if ok {
		return l
	}
	return nil
}

// StartListener starts a new HTTP listener and adds it to the listener group
func (lg *ListenerGroup) StartListener(listenerName, address string, port int, connectionsLimit int,
	tlsConfig *tls.Config, router http.Handler, wg *sync.WaitGroup, tracers tracing.Tracers,
	exitOnError bool, drainTimeout time.Duration, log *tl.Logger) error {
	if wg != nil {
		defer wg.Done()
	}
	l := &Listener{routeSwapper: ph.NewSwitchHandler(router), exitOnError: exitOnError}
	if tlsConfig != nil && len(tlsConfig.Certificates) > 0 {
		l.tlsConfig = tlsConfig
		l.tlsSwapper = sw.NewSwapper(tlsConfig.Certificates)
		// Replace the normal GetCertificate function in the TLS config with lg.tlsSwapper's,
		// so users swap certs in the config later without restarting the entire process
		tlsConfig.GetCertificate = l.tlsSwapper.GetCert
		tlsConfig.Certificates = nil
	}

	var err error
	l.Listener, err = NewListener(address, port, connectionsLimit, tlsConfig, drainTimeout, log)
	if err != nil {
		log.Error("http listener startup failed", tl.Pairs{"name": listenerName, "detail": err})
		if exitOnError {
			os.Exit(1)
		}
		return err
	}
	log.Info("http listener starting",
		tl.Pairs{"name": listenerName, "port": port, "address": address})

	lg.listenersLock.Lock()
	lg.members[listenerName] = l
	lg.listenersLock.Unlock()

	// defer the tracer flush here where the listener connection ends
	if tracers != nil {
		for _, v := range tracers {
			if v != nil && v.Flusher != nil {
				defer v.Flusher()
			}
		}
	}

	if tlsConfig != nil {
		svr := &http.Server{
			Handler:   handlers.CompressHandler(l.routeSwapper),
			TLSConfig: tlsConfig,
		}
		l.server = svr
		err = svr.Serve(l)
		if err != nil {
			log.Error("https listener stopping", tl.Pairs{"name": listenerName, "detail": err})
			if l.exitOnError {
				os.Exit(1)
			}
		}
		return err
	}

	svr := &http.Server{
		Handler: handlers.CompressHandler(l.routeSwapper),
	}
	l.server = svr
	err = svr.Serve(l)
	if err != nil {
		log.Error("http listener stopping", tl.Pairs{"name": listenerName, "detail": err})
		if l.exitOnError {
			os.Exit(1)
		}
	}
	return err
}

// StartListenerRouter starts a new HTTP listener with a new router, and adds it to the listener group
func (lg *ListenerGroup) StartListenerRouter(listenerName, address string, port int, connectionsLimit int,
	tlsConfig *tls.Config, path string, handler http.Handler, wg *sync.WaitGroup,
	tracers tracing.Tracers, exitOnError bool, drainTimeout time.Duration, log *tl.Logger) error {
	router := http.NewServeMux()
	router.Handle(path, handler)
	return lg.StartListener(listenerName, address, port, connectionsLimit,
		tlsConfig, router, wg, tracers, exitOnError, drainTimeout, log)
}

// DrainAndClose drains and closes the named listener
func (lg *ListenerGroup) DrainAndClose(listenerName string, drainWait time.Duration) error {
	lg.listenersLock.Lock()
	if l, ok := lg.members[listenerName]; ok {
		l.exitOnError = false
		delete(lg.members, listenerName)
		lg.listenersLock.Unlock()
		if l == nil || l.Listener == nil {
			return errors.ErrNilListener
		}
		ctx := context.Background()
		go func() {
			time.Sleep(drainWait)
			ctx.Done()
		}()
		if l.server != nil {
			go l.server.Shutdown(ctx)
		}
		return nil
	}
	lg.listenersLock.Unlock()
	return errors.ErrNoSuchListener
}

// UpdateFrontendRouters will swap out the routers across the named Listeners with the provided ones
func (lg *ListenerGroup) UpdateFrontendRouters(mainRouter http.Handler, adminRouter http.Handler) {
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
	if v, ok := lg.members["reloadListener"]; ok && adminRouter != nil {
		v.routeSwapper.Update(adminRouter)
	}
}

// UpdateRouter will swap out the router for the ListenerGroup with the provided name
func (lg *ListenerGroup) UpdateRouter(routerName string, router http.Handler) {
	lg.listenersLock.Lock()
	if r, ok := lg.members[routerName]; ok {
		r.routeSwapper.Update(router)
	}
	defer lg.listenersLock.Unlock()
}
