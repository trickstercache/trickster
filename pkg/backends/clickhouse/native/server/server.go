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

package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/switcher"
)

// Server runs native connections under the common listener lifecycle.
type Server struct {
	handler     Handler
	tlsConfig   atomic.Pointer[tls.Config]
	requireTLS  bool
	restartKey  string
	mu          sync.Mutex
	listener    net.Listener
	connections map[net.Conn]context.CancelFunc
	stopping    bool
	workers     sync.WaitGroup
}

// New creates a native server. TLS, when required, starts before the native handshake.
func New(h http.Handler, tlsConfig *tls.Config, requireTLS bool, restartKey string) *Server {
	s := &Server{
		handler: Handler{QueryHandler: switcher.NewSwitchHandler(h)}, requireTLS: requireTLS, restartKey: restartKey,
		connections: make(map[net.Conn]context.CancelFunc),
	}
	s.UpdateTLSConfig(tlsConfig)
	return s
}

// ProtocolRestartKey identifies the backend configuration used by this server.
func (s *Server) ProtocolRestartKey() string { return s.restartKey }

// UpdateTLSConfig rotates certificates for subsequently accepted connections.
func (s *Server) UpdateTLSConfig(c *tls.Config) {
	if c != nil {
		c = c.Clone()
		c.NextProtos = nil
	}
	s.tlsConfig.Store(c)
}

// Serve accepts connections until the listener is closed or the server stops.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return l.Close()
	}
	s.listener = l
	s.mu.Unlock()
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			_ = conn.Close()
			return nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.connections[conn] = cancel
		s.workers.Add(1)
		s.mu.Unlock()
		go s.serveConnection(ctx, conn)
	}
}

func (s *Server) serveConnection(ctx context.Context, conn net.Conn) {
	defer s.workers.Done()
	defer func() {
		_ = conn.Close()
		s.mu.Lock()
		if cancel := s.connections[conn]; cancel != nil {
			cancel()
		}
		delete(s.connections, conn)
		s.mu.Unlock()
	}()
	wire := conn
	if s.requireTLS {
		c := s.tlsConfig.Load()
		if c == nil {
			return
		}
		wire = tls.Server(conn, c)
	}
	_ = s.handler.HandleConnection(ctx, wire)
}

// Shutdown stops accepting connections and drains existing sessions until ctx expires.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.stopping = true
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		for conn, cancel := range s.connections {
			cancel()
			_ = conn.Close()
		}
		s.mu.Unlock()
		return ctx.Err()
	}
}

// UpdateHandler switches requests to the current backend generation.
func (s *Server) UpdateHandler(h http.Handler) {
	if sw, ok := s.handler.QueryHandler.(*switcher.SwitchHandler); ok {
		sw.Update(h)
	}
}
