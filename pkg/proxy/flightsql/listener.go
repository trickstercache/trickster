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

package flightsql

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"

	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// preparedReapInterval is how often the listener sweeps for idle prepared
// statements abandoned by disconnected clients.
const preparedReapInterval = time.Minute

// ProtocolServer adapts the Flight SQL gRPC server to Trickster's native
// listener lifecycle (listener.ProtocolServer). Socket binding, connection
// limits, drain, and config-reload restarts are owned by the listener group.
type ProtocolServer struct {
	grpc       *grpc.Server
	srv        *Server
	restartKey string
	tlsConfig  atomic.Pointer[tls.Config]
	stopReaper chan struct{}
	closeOnce  sync.Once
	gate       backends.SessionGateSlot
}

// NewProtocolServer returns a ProtocolServer exposing srv over Flight SQL.
// restartKey identifies the backend configuration that produced the server so
// config reloads can decide between reuse and restart. When tlsConfig carries
// a certificate, the listener serves TLS and supports in-place certificate
// rotation via UpdateTLSConfig; otherwise it serves plaintext gRPC.
func NewProtocolServer(srv *Server, restartKey string, tlsConfig *tls.Config) *ProtocolServer {
	s := &ProtocolServer{
		srv: srv, restartKey: restartKey,
		stopReaper: make(chan struct{}),
	}
	var options []grpc.ServerOption
	if tlsConfig != nil && len(tlsConfig.Certificates) > 0 {
		s.UpdateTLSConfig(tlsConfig)
		options = append(options, grpc.Creds(credentials.NewTLS(&tls.Config{
			GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
				return s.tlsConfig.Load(), nil
			},
		})))
	}
	// gRPC calls share connections, so every call is judged, unary and streaming alike
	options = append(options, grpc.ChainUnaryInterceptor(s.admitUnary), grpc.ChainStreamInterceptor(s.admitStream))
	g := grpc.NewServer(options...)
	flight.RegisterFlightServiceServer(g, flightsql.NewFlightServer(srv))
	s.grpc = g
	go s.reapLoop()
	return s
}

// reapLoop periodically closes prepared statements abandoned by disconnected
// clients. It exits when the server shuts down.
func (s *ProtocolServer) reapLoop() {
	ticker := time.NewTicker(preparedReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			s.srv.ReapIdlePrepared(ctx, DefaultPreparedIdleTTL)
			cancel()
		case <-s.stopReaper:
			return
		}
	}
}

// UpdateTLSConfig rotates certificates for subsequently accepted connections.
// It has no effect on a server started without TLS.
func (s *ProtocolServer) UpdateTLSConfig(c *tls.Config) {
	if c == nil || len(c.Certificates) == 0 {
		return
	}
	c = c.Clone()
	// gRPC requires HTTP/2 via ALPN
	c.NextProtos = []string{"h2"}
	s.tlsConfig.Store(c)
}

// Serve accepts Flight SQL connections until Shutdown. It returns nil after a
// clean Shutdown, per the gRPC server contract.
func (s *ProtocolServer) Serve(l net.Listener) error {
	return s.grpc.Serve(l)
}

// Shutdown drains active streams until ctx expires, then forces closure. It
// also stops the prepared-statement reaper and closes the upstream client so
// config-reload restarts don't leak the previous gRPC connection.
func (s *ProtocolServer) Shutdown(ctx context.Context) error {
	defer s.closeOnce.Do(func() {
		close(s.stopReaper)
		if s.srv != nil {
			_ = s.srv.Close()
		}
	})
	done := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.grpc.Stop()
		return ctx.Err()
	}
}

// UpdateSessionGate switches the gate that judges each call
func (s *ProtocolServer) UpdateSessionGate(gate backends.SessionGate) {
	s.gate.Store(gate)
}

func (s *ProtocolServer) admit(ctx context.Context) error {
	if !s.gate.Holds() {
		return nil
	}
	var addr netip.Addr
	if p, ok := peer.FromContext(ctx); ok {
		addr = clientip.FromNetAddr(p.Addr)
	}
	if d := s.gate.Admit(addr); d != nil {
		return status.Error(codes.PermissionDenied, d.Message)
	}
	return nil
}

func (s *ProtocolServer) admitUnary(ctx context.Context, req any, _ *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	if err := s.admit(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (s *ProtocolServer) admitStream(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	if err := s.admit(ss.Context()); err != nil {
		return err
	}
	return handler(srv, ss)
}

// ProtocolRestartKey identifies the backend configuration used by this server.
func (s *ProtocolServer) ProtocolRestartKey() string { return s.restartKey }
