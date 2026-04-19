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

package flight

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"google.golang.org/grpc"
)

// ListenerConfig configures the Flight SQL listener.
type ListenerConfig struct {
	Address string // e.g., "0.0.0.0"
	Port    int
	Name    string // optional identifier for logs/metrics
}

// Listener wraps a gRPC server that exposes a Flight SQL service.
type Listener struct {
	server *grpc.Server
	lis    net.Listener
	name   string
}

var (
	registryMu sync.Mutex
	registry   = map[string]*Listener{}
)

// replaceExistingTimeout bounds how long a same-named listener gets to drain
// when Start is called for a backend that's being reloaded.
const replaceExistingTimeout = 2 * time.Second

// Start begins accepting Flight SQL connections on the configured address
// and registers the listener for coordinated shutdown via ShutdownAll.
// If a listener with the same Name is already registered (e.g., during config
// reload), it is gracefully stopped first so the port can be rebound.
func Start(cfg ListenerConfig, srv *Server) (*Listener, error) {
	if srv == nil {
		return nil, errors.New("server is nil")
	}
	if cfg.Name != "" {
		registryMu.Lock()
		existing, ok := registry[cfg.Name]
		if ok {
			delete(registry, cfg.Name)
		}
		registryMu.Unlock()
		if ok {
			_ = existing.Stop(replaceExistingTimeout)
		}
	}
	addr := fmt.Sprintf("%s:%d", cfg.Address, cfg.Port)
	lis, err := listenWithRetry(addr, 500*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("flight listener: %w", err)
	}
	grpcSrv := grpc.NewServer()
	flight.RegisterFlightServiceServer(grpcSrv, flightsql.NewFlightServer(srv))
	go func() {
		_ = grpcSrv.Serve(lis)
	}()
	l := &Listener{server: grpcSrv, lis: lis, name: cfg.Name}
	registryMu.Lock()
	key := cfg.Name
	if key == "" {
		key = addr // fall back to address for unnamed listeners
	}
	registry[key] = l
	registryMu.Unlock()
	return l, nil
}

// Stop gracefully shuts down the Flight SQL server, draining active streams
// until drainTimeout elapses, then forcing connection closure. Pass 0 for
// immediate force-stop. Blocks until the underlying socket is fully closed
// so callers can rebind the port safely.
func (l *Listener) Stop(drainTimeout time.Duration) error {
	if l.server == nil {
		return nil
	}
	if drainTimeout <= 0 {
		l.server.Stop()
	} else {
		done := make(chan struct{})
		go func() {
			l.server.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(drainTimeout):
			l.server.Stop()
		}
	}
	// Belt-and-suspenders: ensure the net.Listener is closed. gRPC closes it
	// during Stop/GracefulStop, but close is idempotent and this makes the
	// port-release deterministic for immediate rebind.
	if l.lis != nil {
		_ = l.lis.Close()
	}
	return nil
}

// Addr returns the listener's bound address, useful for tests.
func (l *Listener) Addr() net.Addr {
	if l.lis == nil {
		return nil
	}
	return l.lis.Addr()
}

// Name returns the listener's name.
func (l *Listener) Name() string { return l.name }

// listenWithRetry retries net.Listen for up to maxWait to tolerate brief
// kernel-level socket release lag when rebinding a port during config reload.
func listenWithRetry(addr string, maxWait time.Duration) (net.Listener, error) {
	deadline := time.Now().Add(maxWait)
	backoff := 10 * time.Millisecond
	var lastErr error
	for {
		lis, err := net.Listen("tcp", addr)
		if err == nil {
			return lis, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		time.Sleep(backoff)
		if backoff < 100*time.Millisecond {
			backoff *= 2
		}
	}
}

// ShutdownAll gracefully stops all Flight SQL listeners registered via Start
// and clears the registry. Returns the first non-nil error encountered.
// Pass ctx.Deadline() or derive a timeout to bound the drain.
func ShutdownAll(ctx context.Context) error {
	registryMu.Lock()
	ls := make([]*Listener, 0, len(registry))
	for _, l := range registry {
		ls = append(ls, l)
	}
	registry = map[string]*Listener{}
	registryMu.Unlock()

	drain := 5 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining > 0 {
			drain = remaining
		}
	}
	var firstErr error
	for _, l := range ls {
		if err := l.Stop(drain); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
