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

// Package flightsql provides a vendor-neutral Apache Arrow Flight SQL caching
// proxy: a gRPC protocol server that forwards queries, metadata RPCs, and the
// prepared-statement lifecycle to an upstream Flight SQL endpoint, caching the
// Arrow IPC byte streams in tenant-scoped cache namespaces. Backend providers
// (e.g. InfluxDB 3) wire it up by supplying the upstream address, the metadata
// keys their upstream scopes results by, and a cache; nothing in this package
// is specific to any one Flight SQL implementation.
package flightsql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql/schema_ref"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// KeyScoper derives a tenant cache-namespace suffix from the incoming gRPC
// request context. The client forwards scoping metadata to the upstream for
// per-request result scoping, so cache keys must mirror that scope to avoid
// returning one tenant's data to another.
type KeyScoper func(ctx context.Context) string

// MetadataKeyScoper returns a KeyScoper that joins the first value of each
// named incoming-metadata key with '|', plainKeys first, in the given order.
// Values of hashedKeys are SHA-256-hashed (credentials must never appear in
// cache keys); an absent hashed key contributes an empty component rather
// than a hash of the empty string.
func MetadataKeyScoper(plainKeys, hashedKeys []string) KeyScoper {
	return func(ctx context.Context) string {
		md, _ := metadata.FromIncomingContext(ctx)
		parts := make([]string, 0, len(plainKeys)+len(hashedKeys))
		for _, key := range plainKeys {
			parts = append(parts, mdFirst(md, key))
		}
		for _, key := range hashedKeys {
			value := mdFirst(md, key)
			if value != "" {
				sum := sha256.Sum256([]byte(value))
				value = hex.EncodeToString(sum[:8])
			}
			parts = append(parts, value)
		}
		return strings.Join(parts, "|")
	}
}

// defaultKeyScoper scopes cache entries by the hashed authorization metadata
// only. Deployments whose upstream scopes results by additional request
// metadata (a database or bucket header, for instance) must supply a
// KeyScoper covering that metadata via WithKeyScoper.
var defaultKeyScoper = MetadataKeyScoper(nil, []string{"authorization"})

// tenantKey prefixes the configured key scope with the server's namespace.
func (s *Server) tenantKey(ctx context.Context) string {
	return s.keyPrefix + "|" + s.keyScoper(ctx)
}

func mdFirst(md metadata.MD, key string) string {
	if md == nil {
		return ""
	}
	v := md.Get(key)
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// Server is a Flight SQL server that acts as a caching proxy to an upstream
// Flight SQL service (e.g., InfluxDB 3.x).
type Server struct {
	flightsql.BaseServer

	upstream UpstreamClient
	cache    Cache
	alloc    memory.Allocator

	// cacheTTL bounds the lifetime of cached response bytes.
	cacheTTL time.Duration
	// keyPrefix namespaces cache keys per backend so two backends sharing a
	// named cache never alias each other's entries.
	keyPrefix string
	// keyScoper derives the per-tenant cache-namespace suffix.
	keyScoper KeyScoper
	// deltaConfig and delta enable the delta-proxy-cache tier for statement
	// queries when configured via WithDeltaCache.
	deltaConfig *DeltaConfig
	delta       *deltaRunner

	// prepared tracks server-side state per prepared-statement handle: the
	// most recent bound parameter hash (part of the DoGetPreparedStatement
	// cache key, so two clients executing the same prepared statement with
	// different parameter values don't alias each other's cache entries) and
	// the last-access time used to reap statements abandoned by disconnected
	// clients.
	paramMu  sync.Mutex
	prepared map[string]*preparedMeta
}

// preparedMeta is the server-side bookkeeping for one prepared statement.
type preparedMeta struct {
	// query is the statement text the handle was prepared from; it lets
	// parameterless executions share the statement cache tiers.
	query      string
	paramHash  string
	lastAccess time.Time
}

// DefaultCacheTTL is the cache lifetime applied when no WithCacheTTL option
// is provided.
const DefaultCacheTTL = 60 * time.Second

// DefaultPreparedIdleTTL is how long a prepared statement may go unused before
// the reaper closes it upstream. Flight SQL has no session teardown signal for
// abandoned handles, so idle expiry bounds the growth of the handle registries
// when clients disconnect without calling ClosePreparedStatement.
const DefaultPreparedIdleTTL = 15 * time.Minute

// ServerOption customizes a Server.
type ServerOption func(*Server)

// WithCacheTTL sets the cache lifetime for query and metadata responses.
func WithCacheTTL(ttl time.Duration) ServerOption {
	return func(s *Server) {
		if ttl > 0 {
			s.cacheTTL = ttl
		}
	}
}

// WithCacheKeyPrefix namespaces the server's cache keys, typically by backend
// name.
func WithCacheKeyPrefix(prefix string) ServerOption {
	return func(s *Server) { s.keyPrefix = prefix }
}

// WithKeyScoper sets the tenant cache-namespace derivation. It must cover
// every request-metadata key the upstream scopes results by; the default
// scopes by hashed authorization only.
func WithKeyScoper(scoper KeyScoper) ServerOption {
	return func(s *Server) {
		if scoper != nil {
			s.keyScoper = scoper
		}
	}
}

// UpstreamClient is the minimum surface the server needs from a Flight SQL
// client implementation. This lets us swap in a fake for tests.
// Each method returns the IPC-encoded bytes (schema + record batches) of the
// upstream response so callers can cache the whole stream verbatim.
type UpstreamClient interface {
	Execute(ctx context.Context, query string) ([]byte, error)
	GetCatalogs(ctx context.Context) ([]byte, error)
	GetDBSchemas(ctx context.Context, opts *flightsql.GetDBSchemasOpts) ([]byte, error)
	GetTables(ctx context.Context, opts *flightsql.GetTablesOpts) ([]byte, error)
	GetTableTypes(ctx context.Context) ([]byte, error)
	GetSqlInfo(ctx context.Context, info []flightsql.SqlInfo) ([]byte, error)
	// GetExecuteSchema and GetPreparedSchema return serialized Arrow schemas
	// rather than IPC streams.
	GetExecuteSchema(ctx context.Context, query string) ([]byte, error)
	GetPreparedSchema(ctx context.Context, handle []byte) ([]byte, error)
	PrepareStatement(ctx context.Context, query string) ([]byte, error)
	SetPreparedStatementParams(ctx context.Context, handle []byte, params arrow.RecordBatch) error
	ExecutePrepared(ctx context.Context, handle []byte) ([]byte, error)
	ClosePrepared(ctx context.Context, handle []byte) error
	Close() error
}

// Cache stores serialized Arrow IPC byte streams keyed by query.
type Cache interface {
	Get(key string) ([]byte, bool)
	Set(key string, data []byte, ttl time.Duration)
}

// NewServer constructs a Flight SQL server with the given upstream and cache.
func NewServer(upstream UpstreamClient, cache Cache, opts ...ServerOption) *Server {
	s := &Server{
		upstream:  upstream,
		cache:     cache,
		alloc:     memory.DefaultAllocator,
		cacheTTL:  DefaultCacheTTL,
		keyScoper: defaultKeyScoper,
		prepared:  make(map[string]*preparedMeta),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.deltaConfig != nil {
		s.delta = newDeltaRunner(*s.deltaConfig, s.keyPrefix)
	}
	return s
}

// GetFlightInfoStatement handles a SQL query request. It returns a FlightInfo
// with a single endpoint whose ticket carries the query text. The actual
// execution happens in DoGetStatement when the client fetches the ticket.
func (s *Server) GetFlightInfoStatement(_ context.Context,
	cmd flightsql.StatementQuery, desc *flight.FlightDescriptor,
) (*flight.FlightInfo, error) {
	ticket, err := flightsql.CreateStatementQueryTicket([]byte(cmd.GetQuery()))
	if err != nil {
		return nil, err
	}
	return &flight.FlightInfo{
		FlightDescriptor: desc,
		Endpoint: []*flight.FlightEndpoint{
			{Ticket: &flight.Ticket{Ticket: ticket}},
		},
		TotalRecords: -1,
		TotalBytes:   -1,
	}, nil
}

// DoGetStatement executes the query and streams the Arrow IPC record batches
// back to the client. With a delta tier configured, the analyzer routes each
// statement across the delta, object, and proxy tiers; otherwise responses
// are cached whole in the object tier, with nondeterministic statements never
// cached.
func (s *Server) DoGetStatement(ctx context.Context,
	ticket flightsql.StatementQueryTicket,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	query := string(ticket.GetStatementHandle())
	if s.delta != nil {
		return s.delta.serve(ctx, s, query)
	}
	if volatileQuery(query) {
		b, err := s.upstream.Execute(ctx, query)
		if err != nil {
			return nil, nil, fmt.Errorf("upstream execute: %w", err)
		}
		return streamIPCBytes(ctx, b)
	}
	ipcBytes, _, err := s.objectTier(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	return streamIPCBytes(ctx, ipcBytes)
}

// objectTier serves a statement from the verbatim-IPC object cache: the whole
// response cached briefly under the tenant-scoped statement key.
func (s *Server) objectTier(ctx context.Context,
	query string,
) ([]byte, cachestatus.LookupStatus, error) {
	key := s.tenantKey(ctx) + ":stmt:" + query
	ipcBytes, cached := s.cacheGet(key)
	if cached {
		return ipcBytes, cachestatus.LookupStatusHit, nil
	}
	b, err := s.upstream.Execute(ctx, query)
	if err != nil {
		return nil, cachestatus.LookupStatusProxyError, fmt.Errorf("upstream execute: %w", err)
	}
	s.cacheSet(key, b)
	return b, cachestatus.LookupStatusKeyMiss, nil
}

// flightInfoForCommand constructs a FlightInfo for metadata RPCs. The ticket
// is the command proto bytes from the descriptor; the server framework decodes
// and routes to the appropriate DoGetX method.
func (s *Server) flightInfoForCommand(desc *flight.FlightDescriptor,
	schema *arrow.Schema,
) *flight.FlightInfo {
	return &flight.FlightInfo{
		Endpoint:         []*flight.FlightEndpoint{{Ticket: &flight.Ticket{Ticket: desc.Cmd}}},
		FlightDescriptor: desc,
		Schema:           flight.SerializeSchema(schema, s.alloc),
		TotalRecords:     -1,
		TotalBytes:       -1,
	}
}

// fetchMetadata centralizes the cache-then-upstream pattern for metadata RPCs.
// key should be a stable, collision-resistant identifier for the request; kind
// is a low-cardinality label safe to surface in error messages, which must not
// echo the tenant-scoped cache key.
func (s *Server) fetchMetadata(ctx context.Context, kind, key string,
	fetch func(context.Context) ([]byte, error),
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	ipcBytes, cached := s.cacheGet(key)
	if !cached {
		b, err := fetch(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("upstream %s: %w", kind, err)
		}
		ipcBytes = b
		s.cacheSet(key, ipcBytes)
	}
	return streamIPCBytes(ctx, ipcBytes)
}

// GetFlightInfoCatalogs returns a FlightInfo describing the catalog list.
func (s *Server) GetFlightInfoCatalogs(_ context.Context,
	desc *flight.FlightDescriptor,
) (*flight.FlightInfo, error) {
	return s.flightInfoForCommand(desc, schema_ref.Catalogs), nil
}

// DoGetCatalogs streams the upstream's catalog list (cache-first).
func (s *Server) DoGetCatalogs(ctx context.Context,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	return s.fetchMetadata(ctx, "catalogs", s.tenantKey(ctx)+":meta:catalogs",
		s.upstream.GetCatalogs)
}

// GetFlightInfoSchemas returns a FlightInfo describing DB schemas.
func (s *Server) GetFlightInfoSchemas(_ context.Context,
	_ flightsql.GetDBSchemas, desc *flight.FlightDescriptor,
) (*flight.FlightInfo, error) {
	return s.flightInfoForCommand(desc, schema_ref.DBSchemas), nil
}

// DoGetDBSchemas streams the upstream's DB schema list (cache-first).
func (s *Server) DoGetDBSchemas(ctx context.Context,
	cmd flightsql.GetDBSchemas,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	opts := &flightsql.GetDBSchemasOpts{
		Catalog:               cmd.GetCatalog(),
		DbSchemaFilterPattern: cmd.GetDBSchemaFilterPattern(),
	}
	key := s.tenantKey(ctx) + ":meta:dbschemas:" + strings.Join([]string{
		deref(cmd.GetCatalog()),
		deref(cmd.GetDBSchemaFilterPattern()),
	}, "|")
	return s.fetchMetadata(ctx, "dbschemas", key, func(ctx context.Context) ([]byte, error) {
		return s.upstream.GetDBSchemas(ctx, opts)
	})
}

// GetFlightInfoTables returns a FlightInfo describing the table list.
func (s *Server) GetFlightInfoTables(_ context.Context,
	cmd flightsql.GetTables, desc *flight.FlightDescriptor,
) (*flight.FlightInfo, error) {
	schema := schema_ref.Tables
	if cmd.GetIncludeSchema() {
		schema = schema_ref.TablesWithIncludedSchema
	}
	return s.flightInfoForCommand(desc, schema), nil
}

// DoGetTables streams the upstream's table list (cache-first).
func (s *Server) DoGetTables(ctx context.Context,
	cmd flightsql.GetTables,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	tableTypes := cmd.GetTableTypes()
	opts := &flightsql.GetTablesOpts{
		Catalog:                cmd.GetCatalog(),
		DbSchemaFilterPattern:  cmd.GetDBSchemaFilterPattern(),
		TableNameFilterPattern: cmd.GetTableNameFilterPattern(),
		TableTypes:             tableTypes,
		IncludeSchema:          cmd.GetIncludeSchema(),
	}
	key := s.tenantKey(ctx) + ":meta:tables:" + strings.Join([]string{
		deref(cmd.GetCatalog()),
		deref(cmd.GetDBSchemaFilterPattern()),
		deref(cmd.GetTableNameFilterPattern()),
		strings.Join(tableTypes, ","),
		strconv.FormatBool(cmd.GetIncludeSchema()),
	}, "|")
	return s.fetchMetadata(ctx, "tables", key, func(ctx context.Context) ([]byte, error) {
		return s.upstream.GetTables(ctx, opts)
	})
}

// GetFlightInfoTableTypes returns a FlightInfo describing table types.
func (s *Server) GetFlightInfoTableTypes(_ context.Context,
	desc *flight.FlightDescriptor,
) (*flight.FlightInfo, error) {
	return s.flightInfoForCommand(desc, schema_ref.TableTypes), nil
}

// DoGetTableTypes streams the upstream's table types (cache-first).
func (s *Server) DoGetTableTypes(ctx context.Context,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	return s.fetchMetadata(ctx, "tabletypes", s.tenantKey(ctx)+":meta:tabletypes",
		s.upstream.GetTableTypes)
}

// GetFlightInfoSqlInfo returns a FlightInfo describing SQL info. BaseServer's
// default implementation fails with NotFound unless info is locally registered
// via RegisterSqlInfo; we override to always route through to DoGetSqlInfo so
// the response reflects upstream capabilities.
func (s *Server) GetFlightInfoSqlInfo(_ context.Context,
	_ flightsql.GetSqlInfo, desc *flight.FlightDescriptor,
) (*flight.FlightInfo, error) {
	return s.flightInfoForCommand(desc, schema_ref.SqlInfo), nil
}

// DoGetSqlInfo streams upstream SQL info records (cache-first). The default
// BaseServer GetFlightInfoSqlInfo/DoGetSqlInfo use locally-registered info; we
// intercept DoGetSqlInfo and proxy to upstream instead so values reflect the
// actual upstream capabilities (version, dialect, etc.).
func (s *Server) DoGetSqlInfo(ctx context.Context,
	cmd flightsql.GetSqlInfo,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	rawInfo := cmd.GetInfo()
	info := make([]flightsql.SqlInfo, len(rawInfo))
	for i, v := range rawInfo {
		info[i] = flightsql.SqlInfo(v)
	}
	return s.fetchMetadata(ctx, "sqlinfo", fmt.Sprintf("%s:meta:sqlinfo:%v", s.tenantKey(ctx), rawInfo),
		func(ctx context.Context) ([]byte, error) {
			return s.upstream.GetSqlInfo(ctx, info)
		})
}

// deref returns the dereferenced string or empty when nil.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// CreatePreparedStatement proxies prepared-statement creation to the upstream
// and passes the handle back to the client. No caching at the prepare stage —
// caching happens on the DoGetPreparedStatement path keyed by the handle.
func (s *Server) CreatePreparedStatement(ctx context.Context,
	req flightsql.ActionCreatePreparedStatementRequest,
) (flightsql.ActionCreatePreparedStatementResult, error) {
	handle, err := s.upstream.PrepareStatement(ctx, req.GetQuery())
	if err != nil {
		return flightsql.ActionCreatePreparedStatementResult{},
			fmt.Errorf("upstream prepare: %w", err)
	}
	s.registerPrepared(handle, req.GetQuery())
	return flightsql.ActionCreatePreparedStatementResult{Handle: handle}, nil
}

// GetSchemaStatement returns the result-set schema a query would produce,
// cache-first under the tenant-scoped statement key. ADBC drivers call this
// before execution to type result bindings.
func (s *Server) GetSchemaStatement(ctx context.Context,
	cmd flightsql.StatementQuery, _ *flight.FlightDescriptor,
) (*flight.SchemaResult, error) {
	query := cmd.GetQuery()
	key := s.tenantKey(ctx) + ":schema:" + query
	if schema, cached := s.cacheGet(key); cached {
		return &flight.SchemaResult{Schema: schema}, nil
	}
	schema, err := s.statementSchema(ctx, query)
	if err != nil {
		return nil, err
	}
	s.cacheSet(key, schema)
	return &flight.SchemaResult{Schema: schema}, nil
}

// GetSchemaPreparedStatement returns a prepared statement's result-set
// schema, cache-first under the handle (a handle's schema is stable for its
// lifetime).
func (s *Server) GetSchemaPreparedStatement(ctx context.Context,
	cmd flightsql.PreparedStatementQuery, _ *flight.FlightDescriptor,
) (*flight.SchemaResult, error) {
	handle := cmd.GetPreparedStatementHandle()
	query, _ := s.preparedState(handle)
	key := s.tenantKey(ctx) + ":prepschema:" + string(handle)
	if schema, cached := s.cacheGet(key); cached {
		return &flight.SchemaResult{Schema: schema}, nil
	}
	schema, err := s.upstream.GetPreparedSchema(ctx, handle)
	if err != nil {
		if status.Code(err) != codes.Unimplemented || query == "" {
			return nil, fmt.Errorf("upstream prepared schema: %w", err)
		}
		schema, err = s.statementSchema(ctx, query)
		if err != nil {
			return nil, err
		}
	}
	s.cacheSet(key, schema)
	return &flight.SchemaResult{Schema: schema}, nil
}

// statementSchema resolves a statement's serialized result-set schema:
// upstream schema RPC first, and when the upstream does not implement it
// (InfluxDB 3 Core among them), by executing the statement through the
// object tier and reading the response stream's schema. The executed
// response is cached, so the cost amortizes into the execution the client
// is typically about to perform anyway.
func (s *Server) statementSchema(ctx context.Context, query string) ([]byte, error) {
	schema, err := s.upstream.GetExecuteSchema(ctx, query)
	if err == nil {
		return schema, nil
	}
	if status.Code(err) != codes.Unimplemented {
		return nil, fmt.Errorf("upstream schema: %w", err)
	}
	ipcBytes, _, err := s.objectTier(ctx, query)
	if err != nil {
		return nil, err
	}
	reader, err := ipc.NewReader(bytes.NewReader(ipcBytes))
	if err != nil {
		return nil, fmt.Errorf("schema from execution: %w", err)
	}
	defer reader.Release()
	return flight.SerializeSchema(reader.Schema(), s.alloc), nil
}

// GetFlightInfoPreparedStatement returns a FlightInfo whose ticket is the
// command proto bytes from the descriptor, same pattern as metadata RPCs.
// The framework decodes and routes to DoGetPreparedStatement.
func (s *Server) GetFlightInfoPreparedStatement(_ context.Context,
	_ flightsql.PreparedStatementQuery, desc *flight.FlightDescriptor,
) (*flight.FlightInfo, error) {
	// No static schema available pre-execution; pass nil to let Arrow infer
	// from the response stream. The real schema surfaces in DoGet.
	return &flight.FlightInfo{
		Endpoint: []*flight.FlightEndpoint{
			{Ticket: &flight.Ticket{Ticket: desc.Cmd}},
		},
		FlightDescriptor: desc,
		TotalRecords:     -1,
		TotalBytes:       -1,
	}, nil
}

// DoPutPreparedStatementQuery receives parameter bindings from the client and
// forwards them to the upstream prepared statement. The parameter hash is
// recorded against the handle so DoGet cache keys reflect the bound values.
func (s *Server) DoPutPreparedStatementQuery(ctx context.Context,
	cmd flightsql.PreparedStatementQuery,
	reader flight.MessageReader, _ flight.MetadataWriter,
) ([]byte, error) {
	handle := cmd.GetPreparedStatementHandle()
	if !reader.Next() {
		// no record batches sent — treat as clearing params
		s.setParamHash(handle, "")
		return handle, nil
	}
	rec := reader.RecordBatch()
	rec.Retain()
	defer rec.Release()
	if err := s.upstream.SetPreparedStatementParams(ctx, handle, rec); err != nil {
		return nil, fmt.Errorf("upstream set params: %w", err)
	}
	hash, err := hashRecordBatch(rec)
	if err != nil {
		return nil, fmt.Errorf("hash params: %w", err)
	}
	s.setParamHash(handle, hash)
	return handle, nil
}

// DoGetPreparedStatement executes the upstream prepared statement and streams
// its Arrow IPC output. A parameterless prepared statement is equivalent to
// executing its statement text, so with a delta tier configured it shares the
// statement query tiers (delta, object, proxy) — including their cache
// entries — with plain Execute calls of the same text. Parameterized
// executions are cached whole, keyed by the bound parameter hash so two
// clients running the same statement with different params don't collide.
func (s *Server) DoGetPreparedStatement(ctx context.Context,
	cmd flightsql.PreparedStatementQuery,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	handle := cmd.GetPreparedStatementHandle()
	query, paramHash := s.preparedState(handle)
	if s.delta != nil && query != "" && paramHash == "" {
		return s.delta.serve(ctx, s, query)
	}
	key := s.tenantKey(ctx) + ":prep:" + string(handle) + ":" + paramHash
	return s.fetchMetadata(ctx, "prepared statement", key, func(ctx context.Context) ([]byte, error) {
		return s.upstream.ExecutePrepared(ctx, handle)
	})
}

// ClosePreparedStatement releases the upstream handle.
func (s *Server) ClosePreparedStatement(ctx context.Context,
	req flightsql.ActionClosePreparedStatementRequest,
) error {
	handle := req.GetPreparedStatementHandle()
	s.paramMu.Lock()
	delete(s.prepared, string(handle))
	s.paramMu.Unlock()
	return s.upstream.ClosePrepared(ctx, handle)
}

// registerPrepared records the statement text a handle was prepared from.
func (s *Server) registerPrepared(handle []byte, query string) {
	s.paramMu.Lock()
	defer s.paramMu.Unlock()
	meta, ok := s.prepared[string(handle)]
	if !ok {
		meta = &preparedMeta{}
		s.prepared[string(handle)] = meta
	}
	meta.query = query
	meta.lastAccess = time.Now()
}

// preparedState returns the handle's statement text and current parameter
// hash, recording the access.
func (s *Server) preparedState(handle []byte) (string, string) {
	s.paramMu.Lock()
	defer s.paramMu.Unlock()
	meta, ok := s.prepared[string(handle)]
	if !ok {
		meta = &preparedMeta{}
		s.prepared[string(handle)] = meta
	}
	meta.lastAccess = time.Now()
	return meta.query, meta.paramHash
}

func (s *Server) setParamHash(handle []byte, hash string) {
	s.paramMu.Lock()
	defer s.paramMu.Unlock()
	meta, ok := s.prepared[string(handle)]
	if !ok {
		meta = &preparedMeta{}
		s.prepared[string(handle)] = meta
	}
	meta.paramHash = hash
	meta.lastAccess = time.Now()
}

// ReapIdlePrepared closes prepared statements not accessed within maxIdle,
// releasing their upstream resources, and returns how many were reaped. It is
// called periodically by the protocol listener; clients that disconnect
// without closing their statements would otherwise leak handles on both this
// process and the upstream indefinitely.
func (s *Server) ReapIdlePrepared(ctx context.Context, maxIdle time.Duration) int {
	cutoff := time.Now().Add(-maxIdle)
	s.paramMu.Lock()
	var idle [][]byte
	for handle, meta := range s.prepared {
		if meta.lastAccess.Before(cutoff) {
			idle = append(idle, []byte(handle))
			delete(s.prepared, handle)
		}
	}
	s.paramMu.Unlock()
	for _, handle := range idle {
		_ = s.upstream.ClosePrepared(ctx, handle)
	}
	return len(idle)
}

// Close releases the upstream client connection. The server must not be used
// after Close.
func (s *Server) Close() error {
	if s.upstream != nil {
		return s.upstream.Close()
	}
	return nil
}

// hashRecordBatch returns a stable hex-encoded hash of an Arrow RecordBatch
// by writing its IPC-encoded bytes through sha256.
func hashRecordBatch(rec arrow.RecordBatch) (string, error) {
	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(rec.Schema()))
	if err := w.Write(rec); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// streamIPCBytes parses serialized Arrow IPC bytes and returns the schema
// plus a channel of stream chunks the Flight server framework will write out.
// The goroutine feeding the channel exits when ctx is canceled (e.g., the
// client disconnects mid-stream) so it never blocks forever holding record
// batches.
func streamIPCBytes(ctx context.Context, b []byte,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	r, err := ipc.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, nil, fmt.Errorf("ipc reader: %w", err)
	}
	schema := r.Schema()
	ch := make(chan flight.StreamChunk)
	go func() {
		defer close(ch)
		defer r.Release()
		for r.Next() {
			rec := r.RecordBatch()
			rec.Retain()
			select {
			case ch <- flight.StreamChunk{Data: rec}:
			case <-ctx.Done():
				rec.Release()
				return
			}
		}
	}()
	return schema, ch, nil
}

// volatileQuery reports whether a statement references nondeterministic
// functions whose results must never be cached.
func volatileQuery(query string) bool {
	lower := strings.ToLower(query)
	for _, token := range []string{
		"now(", "current_timestamp", "current_date", "current_time", "random(",
		"gen_random_uuid",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func (s *Server) cacheGet(query string) ([]byte, bool) {
	if s.cache == nil {
		return nil, false
	}
	return s.cache.Get(query)
}

func (s *Server) cacheSet(query string, data []byte) {
	if s.cache == nil {
		return
	}
	s.cache.Set(query, data, s.cacheTTL)
}

// EncodeRecords serializes a slice of Arrow records into IPC bytes with their
// shared schema. Useful when constructing cached results from non-Arrow sources.
func EncodeRecords(schema *arrow.Schema, records []arrow.RecordBatch) ([]byte, error) {
	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(schema))
	for _, rec := range records {
		if err := w.Write(rec); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeRecords parses IPC bytes into the schema and a slice of records.
// Primarily used in tests.
func DecodeRecords(b []byte) (*arrow.Schema, []arrow.RecordBatch, error) {
	r, err := ipc.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, nil, err
	}
	defer r.Release()
	var recs []arrow.RecordBatch
	for r.Next() {
		rec := r.RecordBatch()
		rec.Retain()
		recs = append(recs, rec)
	}
	return r.Schema(), recs, r.Err()
}

// Compile-time interface checks
var _ array.Builder = (*array.StringBuilder)(nil)
