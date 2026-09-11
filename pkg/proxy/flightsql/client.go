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
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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
	// MaxResponseBytes bounds the IPC bytes buffered from a single upstream
	// response, which must be buffered whole to be cacheable. Zero applies
	// DefaultMaxResponseBytes; a negative value disables the bound.
	MaxResponseBytes int64
	// MaxBufferedBytes bounds response bytes concurrently being assembled or
	// streamed. Zero applies DefaultMaxBufferedBytes; a negative
	// value disables the aggregate bound.
	MaxBufferedBytes int64
	// AllowedLocationHosts lists alternate endpoint authorities that may receive
	// forwarded request metadata. An empty list rejects alternate locations.
	AllowedLocationHosts []string
	// MaxLocationClients caps cached alternate endpoint connections. Zero
	// applies DefaultMaxLocationClients.
	MaxLocationClients int
}

// DefaultMaxResponseBytes bounds a single buffered upstream Flight response
// when UpstreamConfig.MaxResponseBytes is unset.
const DefaultMaxResponseBytes int64 = 128 << 20

// DefaultMaxBufferedBytes bounds concurrent response buffering across a client.
const DefaultMaxBufferedBytes int64 = 512 << 20

// DefaultMaxLocationClients bounds cached alternate endpoint connections.
const DefaultMaxLocationClients = 16

// errResponseTooLarge marks a response that exceeded the buffering bound; it
// surfaces to the downstream client as gRPC ResourceExhausted.
var errResponseTooLarge = errors.New("upstream response exceeds max response bytes")

var errBufferBudgetExhausted = errors.New("upstream response buffering budget exhausted")

// Client is the default UpstreamClient implementation that talks to a
// Flight SQL server over gRPC and returns IPC-encoded bytes.
type Client struct {
	cfg    UpstreamConfig
	client *flightsql.Client
	alloc  memory.Allocator
	// maxResponseBytes is the resolved buffering bound; zero means unbounded.
	maxResponseBytes int64
	bufferBudget     *bufferBudget

	// locations caches Flight clients for endpoints that direct retrieval away
	// from the primary connection, bounded by the upstream's cluster size.
	locationMu   sync.Mutex
	locations    map[string]flight.Client
	maxLocations int

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
	if cfg.MaxLocationClients < 0 {
		return nil, errors.New("max Flight endpoint location clients cannot be negative")
	}
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
	maxResponseBytes := cfg.MaxResponseBytes
	switch {
	case maxResponseBytes == 0:
		maxResponseBytes = DefaultMaxResponseBytes
	case maxResponseBytes < 0:
		maxResponseBytes = 0
	}
	maxBufferedBytes := cfg.MaxBufferedBytes
	switch {
	case maxBufferedBytes == 0:
		maxBufferedBytes = DefaultMaxBufferedBytes
	case maxBufferedBytes < 0:
		maxBufferedBytes = 0
	}
	maxLocations := cfg.MaxLocationClients
	if maxLocations == 0 {
		maxLocations = DefaultMaxLocationClients
	}
	return &Client{
		cfg:              cfg,
		client:           c,
		alloc:            memory.DefaultAllocator,
		maxResponseBytes: maxResponseBytes,
		bufferBudget:     newBufferBudget(maxBufferedBytes),
		maxLocations:     maxLocations,
		prepared:         make(map[string]*preparedStatement),
		locations:        make(map[string]flight.Client),
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

// GetXdbcTypeInfo returns IPC bytes for the upstream's XDBC data type info,
// for all types or the one named by dataType.
func (c *Client) GetXdbcTypeInfo(ctx context.Context,
	dataType *int32,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetXdbcTypeInfo(ctx, dataType)
	})
}

// GetPrimaryKeys returns IPC bytes for a table's primary keys.
func (c *Client) GetPrimaryKeys(ctx context.Context,
	ref flightsql.TableRef,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetPrimaryKeys(ctx, ref)
	})
}

// GetExportedKeys returns IPC bytes for the foreign keys referencing a table.
func (c *Client) GetExportedKeys(ctx context.Context,
	ref flightsql.TableRef,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetExportedKeys(ctx, ref)
	})
}

// GetImportedKeys returns IPC bytes for the foreign keys a table references.
func (c *Client) GetImportedKeys(ctx context.Context,
	ref flightsql.TableRef,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetImportedKeys(ctx, ref)
	})
}

// GetCrossReference returns IPC bytes for the foreign-key relationship between
// two tables.
func (c *Client) GetCrossReference(ctx context.Context,
	ref flightsql.CrossTableRef,
) ([]byte, error) {
	ctx = c.withAuth(ctx)
	return c.fetchAsIPC(ctx, func(ctx context.Context) (*flight.FlightInfo, error) {
		return c.client.GetCrossReference(ctx, ref.PKRef, ref.FKRef)
	})
}

// fetchAsIPC calls a FlightInfo-returning function and buffers the record
// batches of every endpoint's ticket, in order, into bounded IPC bytes. A
// FlightInfo may partition one result across endpoints; dropping any is short.
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
	var buf bytes.Buffer
	sink := &boundedWriter{
		w: &buf, remaining: c.maxResponseBytes,
		bounded: c.maxResponseBytes > 0, budget: c.bufferBudget,
	}
	defer sink.Release()
	var w *ipc.Writer
	var schema *arrow.Schema
	closeWriter := func() {
		if w != nil {
			_ = w.Close()
		}
	}
	for _, endpoint := range info.Endpoint {
		reader, err := c.endpointReader(ctx, endpoint)
		if err != nil {
			closeWriter()
			return nil, err
		}
		if w == nil {
			schema = reader.Schema()
			w = ipc.NewWriter(sink, ipc.WithSchema(schema))
		} else if !reader.Schema().Equal(schema) {
			reader.Release()
			closeWriter()
			return nil, errors.New("flight endpoints returned mismatched schemas")
		}
		err = writeStream(w, reader)
		reader.Release()
		if err != nil {
			closeWriter()
			return nil, c.streamError(err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, c.streamError(fmt.Errorf("ipc close: %w", err))
	}
	return buf.Bytes(), nil
}

// writeStream copies one endpoint's record batches into the IPC writer.
func writeStream(w *ipc.Writer, reader *flight.Reader) error {
	for reader.Next() {
		if err := w.Write(reader.RecordBatch()); err != nil {
			return fmt.Errorf("ipc write: %w", err)
		}
	}
	if err := reader.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("flight read: %w", err)
	}
	return nil
}

// streamError converts a buffering-bound overrun into a ResourceExhausted
// status so the downstream client sees why the response was refused.
func (c *Client) streamError(err error) error {
	if errors.Is(err, errResponseTooLarge) {
		return status.Errorf(codes.ResourceExhausted,
			"upstream flight response exceeds the %d byte limit", c.maxResponseBytes)
	}
	if errors.Is(err, errBufferBudgetExhausted) {
		return status.Error(codes.ResourceExhausted,
			"upstream Flight response buffering budget exhausted")
	}
	return err
}

// endpointReader opens a record stream for one FlightInfo endpoint, reusing
// the primary upstream connection unless the endpoint advertises a distinct
// location to retrieve its ticket from.
func (c *Client) endpointReader(ctx context.Context,
	endpoint *flight.FlightEndpoint,
) (*flight.Reader, error) {
	if endpoint == nil || endpoint.Ticket == nil {
		return nil, errors.New("flight endpoint has no ticket")
	}
	target := c.endpointLocation(endpoint)
	if target == "" {
		reader, err := c.client.DoGet(ctx, endpoint.Ticket)
		if err != nil {
			return nil, fmt.Errorf("flight doGet: %w", err)
		}
		return reader, nil
	}
	client, err := c.locationClient(target)
	if err != nil {
		return nil, err
	}
	stream, err := client.DoGet(ctx, endpoint.Ticket)
	if err != nil {
		return nil, fmt.Errorf("flight doGet %s: %w", target, err)
	}
	reader, err := flight.NewRecordReader(stream)
	if err != nil {
		return nil, fmt.Errorf("flight doGet %s: %w", target, err)
	}
	return reader, nil
}

// endpointLocation picks the location URI to retrieve an endpoint from, or
// empty to reuse the primary connection: no locations, an explicit
// reuse-connection marker, or a location naming the configured upstream all
// resolve to the existing connection.
func (c *Client) endpointLocation(endpoint *flight.FlightEndpoint) string {
	target := ""
	for _, location := range endpoint.Location {
		uri := location.GetUri()
		if uri == "" || uri == flight.LocationReuseConnection {
			return ""
		}
		if parsed, err := url.Parse(uri); err == nil &&
			strings.EqualFold(parsed.Host, c.cfg.Address) {
			return ""
		}
		if target == "" {
			target = uri
		}
	}
	return target
}

// locationClient returns the cached Flight client for a location URI, dialing
// it on first use with the transport the URI's scheme calls for.
func (c *Client) locationClient(uri string) (flight.Client, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("flight endpoint location %q: %w", uri, err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("flight endpoint location %q has no host", uri)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("flight endpoint location %q contains user information", uri)
	}
	allowed := false
	for _, host := range c.cfg.AllowedLocationHosts {
		if strings.EqualFold(host, parsed.Host) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("flight endpoint location host %q is not allowed", parsed.Host)
	}
	var credential credentials.TransportCredentials
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "grpc+tls":
		credential = credentials.NewTLS(&tls.Config{
			InsecureSkipVerify: c.cfg.InsecureSkipVerify, // #nosec G402 -- mirrors the configured upstream posture
		})
	case "grpc", "grpc+tcp":
		if c.cfg.UseTLS {
			return nil, fmt.Errorf("flight endpoint location %q would downgrade TLS", uri)
		}
		credential = insecure.NewCredentials()
	default:
		return nil, fmt.Errorf("unsupported flight endpoint location scheme %q",
			parsed.Scheme)
	}
	cacheKey := scheme + "://" + strings.ToLower(parsed.Host)
	c.locationMu.Lock()
	defer c.locationMu.Unlock()
	if client, ok := c.locations[cacheKey]; ok {
		return client, nil
	}
	if len(c.locations) >= c.maxLocations {
		return nil, status.Errorf(codes.ResourceExhausted,
			"Flight endpoint location client limit of %d reached", c.maxLocations)
	}
	client, err := flight.NewClientWithMiddlewareCtx(context.Background(),
		parsed.Host, nil, nil, grpc.WithTransportCredentials(credential))
	if err != nil {
		return nil, fmt.Errorf("flight endpoint location %q: %w", uri, err)
	}
	c.locations[cacheKey] = client
	return client, nil
}

// boundedWriter caps how many bytes a response may buffer, failing the copy
// with errResponseTooLarge rather than growing the heap without bound.
type boundedWriter struct {
	w         *bytes.Buffer
	remaining int64
	bounded   bool
	budget    *bufferBudget
	reserved  int64
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if b.bounded {
		if int64(len(p)) > b.remaining {
			return 0, errResponseTooLarge
		}
	}
	requested := int64(len(p))
	if !b.budget.acquire(requested) {
		return 0, errBufferBudgetExhausted
	}
	b.reserved += requested
	n, err := b.w.Write(p)
	if unwritten := requested - int64(n); unwritten > 0 {
		b.budget.release(unwritten)
		b.reserved -= unwritten
	}
	if b.bounded {
		b.remaining -= int64(n)
	}
	return n, err
}

// Release returns this writer's aggregate reservation.
func (b *boundedWriter) Release() {
	b.budget.release(b.reserved)
	b.reserved = 0
}

type bufferBudget struct {
	limit int64
	used  atomic.Int64
}

func newBufferBudget(limit int64) *bufferBudget {
	if limit <= 0 {
		return nil
	}
	return &bufferBudget{limit: limit}
}

func (b *bufferBudget) acquire(size int64) bool {
	if b == nil || size <= 0 {
		return true
	}
	for {
		used := b.used.Load()
		if size > b.limit-used {
			return false
		}
		if b.used.CompareAndSwap(used, used+size) {
			return true
		}
	}
}

func (b *bufferBudget) release(size int64) {
	if b != nil && size > 0 {
		b.used.Add(-size)
	}
}

func (c *Client) responseBufferBudget() *bufferBudget {
	return c.bufferBudget
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

// Close releases the gRPC connections, including any opened for endpoint
// locations.
func (c *Client) Close() error {
	c.locationMu.Lock()
	for uri, client := range c.locations {
		_ = client.Close()
		delete(c.locations, uri)
	}
	c.locationMu.Unlock()
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// Compile-time check
var _ UpstreamClient = (*Client)(nil)
