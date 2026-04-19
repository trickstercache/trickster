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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// UpstreamConfig configures the upstream Flight SQL client.
type UpstreamConfig struct {
	// Address is the upstream Flight SQL endpoint (host:port).
	Address string
	// Database is the v3 database name, sent via the `database` metadata header.
	Database string
	// BearerToken is the optional auth token.
	BearerToken string
}

// FlightSQLClient is the default UpstreamClient implementation that talks to a
// Flight SQL server over gRPC and returns IPC-encoded bytes.
type FlightSQLClient struct {
	cfg    UpstreamConfig
	client *flightsql.Client
	conn   *grpc.ClientConn
	alloc  memory.Allocator

	// prepared statements are tracked by their handle bytes so we can look up
	// the client-side object when Execute / Close is later called.
	preparedMu sync.Mutex
	prepared   map[string]*flightsql.PreparedStatement
}

// NewFlightSQLClient dials the upstream Flight SQL endpoint.
func NewFlightSQLClient(cfg UpstreamConfig) (*FlightSQLClient, error) {
	conn, err := grpc.NewClient(cfg.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("grpc dial: %w", err)
	}
	c, err := flightsql.NewClientCtx(context.Background(),
		cfg.Address, nil, nil,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("flightsql client: %w", err)
	}
	return &FlightSQLClient{
		cfg:      cfg,
		client:   c,
		conn:     conn,
		alloc:    memory.DefaultAllocator,
		prepared: make(map[string]*flightsql.PreparedStatement),
	}, nil
}

// PrepareStatement creates a prepared statement upstream and returns its handle.
// The handle is opaque bytes minted by the upstream; clients use it as the
// round-trip identifier for Execute / Close.
func (c *FlightSQLClient) PrepareStatement(ctx context.Context,
	query string,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	ps, err := c.client.Prepare(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("flight prepare: %w", err)
	}
	handle := ps.Handle()
	c.preparedMu.Lock()
	c.prepared[string(handle)] = ps
	c.preparedMu.Unlock()
	return handle, nil
}

// ExecutePrepared runs a previously-prepared statement upstream and returns
// the IPC-encoded response bytes. Parameter binding is not yet supported —
// clients that rely on bound params will see the same data each call.
func (c *FlightSQLClient) ExecutePrepared(ctx context.Context,
	handle []byte,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	c.preparedMu.Lock()
	ps, ok := c.prepared[string(handle)]
	c.preparedMu.Unlock()
	if !ok {
		return nil, errors.New("unknown prepared statement handle")
	}
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return ps.Execute(ctx)
	})
}

// SetPreparedStatementParams binds parameter values on the upstream prepared
// statement. The next ExecutePrepared call against this handle will use them.
func (c *FlightSQLClient) SetPreparedStatementParams(_ context.Context,
	handle []byte, params arrow.RecordBatch,
) error {
	c.preparedMu.Lock()
	ps, ok := c.prepared[string(handle)]
	c.preparedMu.Unlock()
	if !ok {
		return errors.New("unknown prepared statement handle")
	}
	ps.SetParameters(params)
	return nil
}

// ClosePrepared releases the upstream prepared statement.
func (c *FlightSQLClient) ClosePrepared(ctx context.Context, handle []byte) error {
	ctx = c.withAuth(ctx)
	c.preparedMu.Lock()
	ps, ok := c.prepared[string(handle)]
	if ok {
		delete(c.prepared, string(handle))
	}
	c.preparedMu.Unlock()
	if !ok {
		return nil
	}
	return ps.Close(ctx)
}

// Execute runs a SQL query against the upstream and returns the IPC-encoded
// bytes (schema + record batches) of the entire response. Results are buffered
// to enable caching.
func (c *FlightSQLClient) Execute(ctx context.Context, query string) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.Execute(ctx, query)
	})
}

// GetCatalogs returns IPC bytes for the upstream's catalog list.
func (c *FlightSQLClient) GetCatalogs(ctx context.Context) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetCatalogs(ctx)
	})
}

// GetDBSchemas returns IPC bytes for the upstream's DB schema list.
func (c *FlightSQLClient) GetDBSchemas(ctx context.Context,
	opts *flightsql.GetDBSchemasOpts,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetDBSchemas(ctx, opts)
	})
}

// GetTables returns IPC bytes for the upstream's table list.
func (c *FlightSQLClient) GetTables(ctx context.Context,
	opts *flightsql.GetTablesOpts,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetTables(ctx, opts)
	})
}

// GetTableTypes returns IPC bytes for the upstream's supported table types.
func (c *FlightSQLClient) GetTableTypes(ctx context.Context) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetTableTypes(ctx)
	})
}

// GetSqlInfo returns IPC bytes for the upstream's SQL info records.
func (c *FlightSQLClient) GetSqlInfo(ctx context.Context,
	info []flightsql.SqlInfo,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetSqlInfo(ctx, info)
	})
}

// fetchAsIPC calls a FlightInfo-returning function, resolves the first endpoint
// ticket via DoGet, and buffers the resulting record batches into IPC bytes.
func (c *FlightSQLClient) fetchAsIPC(ctx context.Context,
	getInfo func(context.Context) (*flight.FlightInfo, error),
) ([]byte, error) {
	info, err := getInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("flight request: %w", err)
	}
	if len(info.Endpoint) == 0 {
		return nil, errors.New("flight info has no endpoints")
	}
	reader, err := c.client.DoGet(ctx, info.Endpoint[0].Ticket)
	if err != nil {
		return nil, fmt.Errorf("flight doGet: %w", err)
	}
	defer reader.Release()

	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(reader.Schema()))
	for reader.Next() {
		rec := reader.RecordBatch()
		if err := w.Write(rec); err != nil {
			return nil, fmt.Errorf("ipc write: %w", err)
		}
	}
	if err := reader.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("flight read: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("ipc close: %w", err)
	}
	return buf.Bytes(), nil
}

// withAuth adds the bearer token and database headers to the outgoing context.
// Inbound metadata (from a client calling our server) is forwarded through —
// this lets the end client's `database` and `authorization` headers flow to
// the upstream without reconfiguration.
func (c *FlightSQLClient) withAuth(ctx context.Context) context.Context {
	out := metadata.MD{}
	if in, ok := metadata.FromIncomingContext(ctx); ok {
		for _, h := range []string{"authorization", "database", "bucket-name"} {
			if v := in.Get(h); len(v) > 0 {
				out.Set(h, v...)
			}
		}
	}
	if c.cfg.BearerToken != "" && len(out.Get("authorization")) == 0 {
		out.Set("authorization", "Bearer "+c.cfg.BearerToken)
	}
	if c.cfg.Database != "" && len(out.Get("database")) == 0 {
		out.Set("database", c.cfg.Database)
	}
	if len(out) == 0 {
		return ctx
	}
	return metadata.NewOutgoingContext(ctx, out)
}

// Close releases the gRPC connection.
func (c *FlightSQLClient) Close() error {
	if c.client != nil {
		_ = c.client.Close()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Compile-time check
var _ UpstreamClient = (*FlightSQLClient)(nil)
