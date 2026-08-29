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
	"net"

	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"google.golang.org/grpc"
)

// ProtocolServer adapts the Flight SQL gRPC server to Trickster's native
// listener lifecycle (listener.ProtocolServer). Socket binding, connection
// limits, drain, and config-reload restarts are owned by the listener group.
type ProtocolServer struct {
	grpc       *grpc.Server
	restartKey string
}

// NewProtocolServer returns a ProtocolServer exposing srv over Flight SQL.
// restartKey identifies the backend configuration that produced the server so
// config reloads can decide between reuse and restart.
func NewProtocolServer(srv *Server, restartKey string) *ProtocolServer {
	g := grpc.NewServer()
	flight.RegisterFlightServiceServer(g, flightsql.NewFlightServer(srv))
	return &ProtocolServer{grpc: g, restartKey: restartKey}
}

// Serve accepts Flight SQL connections until Shutdown. It returns nil after a
// clean Shutdown, per the gRPC server contract.
func (s *ProtocolServer) Serve(l net.Listener) error {
	return s.grpc.Serve(l)
}

// Shutdown drains active streams until ctx expires, then forces closure.
func (s *ProtocolServer) Shutdown(ctx context.Context) error {
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

// ProtocolRestartKey identifies the backend configuration used by this server.
func (s *ProtocolServer) ProtocolRestartKey() string { return s.restartKey }
