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
	"bytes"
	"context"
	"crypto/tls"
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
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// UpstreamConfig configures the upstream Flight SQL client.
type UpstreamConfig struct {
	// Address is the upstream Flight SQL endpoint (host:port).
	Address string
	// ForwardMetadataKeys names the inbound gRPC metadata keys forwarded to
	// the upstream on every call (for example authorization and a database or
	// bucket header). The server's cache KeyScoper must cover every key
	// listed here that affects upstream results.
	ForwardMetadataKeys []string
	// DefaultMetadata supplies outgoing metadata values applied when the
	// inbound request did not carry the key (e.g. a statically configured
	// authorization or database).
	DefaultMetadata map[string]string
	// UseTLS dials the upstream with TLS transport credentials.
	UseTLS bool
	// InsecureSkipVerify disables upstream certificate verification when
	// UseTLS is set.
	InsecureSkipVerify bool
}

// Client is the default UpstreamClient implementation that talks to a
// Flight SQL server over gRPC and returns IPC-encoded bytes.
type Client struct {
	cfg    UpstreamConfig
	client *flightsql.Client
	alloc  memory.Allocator

	// prepared statements are tracked by their handle bytes so we can look up
	// the client-side object when Execute / Close is later called.
	preparedMu sync.Mutex
	prepared   map[string]*preparedStatement
}

// preparedStatement pairs an upstream prepared-statement object with a mutex
// serializing bind, execute, and close: *flightsql.PreparedStatement carries
// mutable parameter state and is not safe for concurrent use.
type preparedStatement struct {
	mu sync.Mutex
	ps *flightsql.PreparedStatement
}

// NewClient dials the upstream Flight SQL endpoint.
func NewClient(cfg UpstreamConfig) (*Client, error) {
	credential := insecure.NewCredentials()
	if cfg.UseTLS {
		credential = credentials.NewTLS(&tls.Config{
			InsecureSkipVerify: cfg.InsecureSkipVerify, // #nosec G402 -- this is a user-configurable option, accept risk
		})
	}
	c, err := flightsql.NewClientCtx(context.Background(),
		cfg.Address, nil, nil, grpc.WithTransportCredentials(credential))
	if err != nil {
		return nil, fmt.Errorf("flightsql client: %w", err)
	}
	return &Client{
		cfg:      cfg,
		client:   c,
		alloc:    memory.DefaultAllocator,
		prepared: make(map[string]*preparedStatement),
	}, nil
}

// PrepareStatement creates a prepared statement upstream and returns its handle.
// The handle is opaque bytes minted by the upstream; clients use it as the
// round-trip identifier for Execute / Close.
func (c *Client) PrepareStatement(ctx context.Context,
	query string,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	ps, err := c.client.Prepare(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("flight prepare: %w", err)
	}
	handle := ps.Handle()
	c.preparedMu.Lock()
	c.prepared[string(handle)] = &preparedStatement{ps: ps}
	c.preparedMu.Unlock()
	return handle, nil
}

// ExecutePrepared runs a previously-prepared statement upstream and returns
// the IPC-encoded response bytes. Parameters bound via
// SetPreparedStatementParams are applied by the upstream on execution.
func (c *Client) ExecutePrepared(ctx context.Context,
	handle []byte,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	c.preparedMu.Lock()
	entry, ok := c.prepared[string(handle)]
	c.preparedMu.Unlock()
	if !ok {
		return nil, errors.New("unknown prepared statement handle")
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return entry.ps.Execute(ctx)
	})
}

// SetPreparedStatementParams binds parameter values on the upstream prepared
// statement. The next ExecutePrepared call against this handle will use them.
func (c *Client) SetPreparedStatementParams(_ context.Context,
	handle []byte, params arrow.RecordBatch,
) error {
	c.preparedMu.Lock()
	entry, ok := c.prepared[string(handle)]
	c.preparedMu.Unlock()
	if !ok {
		return errors.New("unknown prepared statement handle")
	}
	entry.mu.Lock()
	entry.ps.SetParameters(params)
	entry.mu.Unlock()
	return nil
}

// ClosePrepared releases the upstream prepared statement.
func (c *Client) ClosePrepared(ctx context.Context, handle []byte) error {
	ctx = c.withAuth(ctx)
	c.preparedMu.Lock()
	entry, ok := c.prepared[string(handle)]
	if ok {
		delete(c.prepared, string(handle))
	}
	c.preparedMu.Unlock()
	if !ok {
		return nil
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.ps.Close(ctx)
}

// GetExecuteSchema returns the serialized schema of the result set the query
// would produce, without executing it.
func (c *Client) GetExecuteSchema(ctx context.Context, query string) ([]byte, error) {
	ctx = c.withAuth(ctx)
	result, err := c.client.GetExecuteSchema(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("flight schema: %w", err)
	}
	return result.GetSchema(), nil
}

// GetPreparedSchema returns the serialized result-set schema of a prepared
// statement, preferring the dataset schema the upstream returned at prepare
// time and falling back to the schema RPC.
func (c *Client) GetPreparedSchema(ctx context.Context, handle []byte) ([]byte, error) {
	c.preparedMu.Lock()
	entry, ok := c.prepared[string(handle)]
	c.preparedMu.Unlock()
	if !ok {
		return nil, errors.New("unknown prepared statement handle")
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if schema := entry.ps.DatasetSchema(); schema != nil {
		return flight.SerializeSchema(schema, c.alloc), nil
	}
	result, err := entry.ps.GetSchema(c.withAuth(ctx))
	if err != nil {
		return nil, fmt.Errorf("flight prepared schema: %w", err)
	}
	return result.GetSchema(), nil
}

// Execute runs a SQL query against the upstream and returns the IPC-encoded
// bytes (schema + record batches) of the entire response. Results are buffered
// to enable caching.
func (c *Client) Execute(ctx context.Context, query string) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.Execute(ctx, query)
	})
}

// GetCatalogs returns IPC bytes for the upstream's catalog list.
func (c *Client) GetCatalogs(ctx context.Context) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetCatalogs(ctx)
	})
}

// GetDBSchemas returns IPC bytes for the upstream's DB schema list.
func (c *Client) GetDBSchemas(ctx context.Context,
	opts *flightsql.GetDBSchemasOpts,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetDBSchemas(ctx, opts)
	})
}

// GetTables returns IPC bytes for the upstream's table list.
func (c *Client) GetTables(ctx context.Context,
	opts *flightsql.GetTablesOpts,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetTables(ctx, opts)
	})
}

// GetTableTypes returns IPC bytes for the upstream's supported table types.
func (c *Client) GetTableTypes(ctx context.Context) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetTableTypes(ctx)
	})
}

// GetSqlInfo returns IPC bytes for the upstream's SQL info records.
func (c *Client) GetSqlInfo(ctx context.Context,
	info []flightsql.SqlInfo,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetSqlInfo(ctx, info)
	})
}

// fetchAsIPC calls a FlightInfo-returning function, resolves the first endpoint
// ticket via DoGet, and buffers the resulting record batches into IPC bytes.
func (c *Client) fetchAsIPC(ctx context.Context,
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

// withAuth builds the outgoing metadata for an upstream call. Inbound
// metadata keys named in ForwardMetadataKeys (from a client calling our
// server) are forwarded through — this lets the end client's scoping headers
// (authorization, database, ...) flow to the upstream without
// reconfiguration. DefaultMetadata fills in keys the inbound request did not
// carry.
func (c *Client) withAuth(ctx context.Context) context.Context {
	out := metadata.MD{}
	if in, ok := metadata.FromIncomingContext(ctx); ok {
		for _, h := range c.cfg.ForwardMetadataKeys {
			if v := in.Get(h); len(v) > 0 {
				out.Set(h, v...)
			}
		}
	}
	for key, value := range c.cfg.DefaultMetadata {
		if value != "" && len(out.Get(key)) == 0 {
			out.Set(key, value)
		}
	}
	if len(out) == 0 {
		return ctx
	}
	return metadata.NewOutgoingContext(ctx, out)
}

// Close releases the gRPC connection.
func (c *Client) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// Compile-time check
var _ UpstreamClient = (*Client)(nil)
