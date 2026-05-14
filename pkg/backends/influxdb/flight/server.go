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

// Package flight provides an Apache Arrow Flight SQL server that proxies
// queries to an upstream InfluxDB 3.x Flight SQL endpoint, caching IPC byte
// streams keyed by the tokenized SQL statement.
package flight

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

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql/schema_ref"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"google.golang.org/grpc/metadata"
)

// tenantKey derives a stable per-tenant cache namespace from the incoming
// gRPC metadata. client.withAuth forwards `authorization`, `database`, and
// `bucket-name` to the upstream for per-request scoping, so cache keys must
// mirror that scope to avoid returning one tenant's data to another.
// Authorization is hashed so bearer tokens don't leak into cache keys.
func tenantKey(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	db := mdFirst(md, "database")
	bucket := mdFirst(md, "bucket-name")
	auth := mdFirst(md, "authorization")
	authPart := ""
	if auth != "" {
		sum := sha256.Sum256([]byte(auth))
		authPart = hex.EncodeToString(sum[:8])
	}
	return db + "|" + bucket + "|" + authPart
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

	// paramHashes tracks the most recent bound parameter hash per prepared
	// statement handle. Used as part of the DoGetPreparedStatement cache key
	// so two clients executing the same prepared statement with different
	// parameter values don't alias each other's cache entries.
	paramMu     sync.Mutex
	paramHashes map[string]string
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
func NewServer(upstream UpstreamClient, cache Cache) *Server {
	return &Server{
		upstream:    upstream,
		cache:       cache,
		alloc:       memory.DefaultAllocator,
		paramHashes: make(map[string]string),
	}
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

// DoGetStatement executes the query (cache-first, upstream on miss) and streams
// the Arrow IPC record batches back to the client.
func (s *Server) DoGetStatement(ctx context.Context,
	ticket flightsql.StatementQueryTicket,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	query := string(ticket.GetStatementHandle())
	key := tenantKey(ctx) + ":stmt:" + query

	ipcBytes, cached := s.cacheGet(key)
	if !cached {
		b, err := s.upstream.Execute(ctx, query)
		if err != nil {
			return nil, nil, fmt.Errorf("upstream execute: %w", err)
		}
		ipcBytes = b
		s.cacheSet(key, ipcBytes)
	}

	return streamIPCBytes(ipcBytes)
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
// key should be a stable, collision-resistant identifier for the request.
func (s *Server) fetchMetadata(ctx context.Context, key string,
	fetch func(context.Context) ([]byte, error),
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	ipcBytes, cached := s.cacheGet(key)
	if !cached {
		b, err := fetch(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("upstream %s: %w", key, err)
		}
		ipcBytes = b
		s.cacheSet(key, ipcBytes)
	}
	return streamIPCBytes(ipcBytes)
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
	return s.fetchMetadata(ctx, tenantKey(ctx)+":meta:catalogs",
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
	key := tenantKey(ctx) + ":meta:dbschemas:" + strings.Join([]string{
		deref(cmd.GetCatalog()),
		deref(cmd.GetDBSchemaFilterPattern()),
	}, "|")
	return s.fetchMetadata(ctx, key, func(ctx context.Context) ([]byte, error) {
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
	key := tenantKey(ctx) + ":meta:tables:" + strings.Join([]string{
		deref(cmd.GetCatalog()),
		deref(cmd.GetDBSchemaFilterPattern()),
		deref(cmd.GetTableNameFilterPattern()),
		strings.Join(tableTypes, ","),
		strconv.FormatBool(cmd.GetIncludeSchema()),
	}, "|")
	return s.fetchMetadata(ctx, key, func(ctx context.Context) ([]byte, error) {
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
	return s.fetchMetadata(ctx, tenantKey(ctx)+":meta:tabletypes",
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
	return s.fetchMetadata(ctx, fmt.Sprintf("%s:meta:sqlinfo:%v", tenantKey(ctx), rawInfo),
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
	return flightsql.ActionCreatePreparedStatementResult{Handle: handle}, nil
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
// its Arrow IPC output. Cache key includes the bound parameter hash so two
// clients running the same statement with different params don't collide.
func (s *Server) DoGetPreparedStatement(ctx context.Context,
	cmd flightsql.PreparedStatementQuery,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	handle := cmd.GetPreparedStatementHandle()
	key := tenantKey(ctx) + ":prep:" + string(handle) + ":" + s.paramHash(handle)
	return s.fetchMetadata(ctx, key, func(ctx context.Context) ([]byte, error) {
		return s.upstream.ExecutePrepared(ctx, handle)
	})
}

// ClosePreparedStatement releases the upstream handle.
func (s *Server) ClosePreparedStatement(ctx context.Context,
	req flightsql.ActionClosePreparedStatementRequest,
) error {
	handle := req.GetPreparedStatementHandle()
	s.paramMu.Lock()
	delete(s.paramHashes, string(handle))
	s.paramMu.Unlock()
	return s.upstream.ClosePrepared(ctx, handle)
}

func (s *Server) setParamHash(handle []byte, hash string) {
	s.paramMu.Lock()
	defer s.paramMu.Unlock()
	if hash == "" {
		delete(s.paramHashes, string(handle))
		return
	}
	s.paramHashes[string(handle)] = hash
}

func (s *Server) paramHash(handle []byte) string {
	s.paramMu.Lock()
	defer s.paramMu.Unlock()
	return s.paramHashes[string(handle)]
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
func streamIPCBytes(b []byte) (*arrow.Schema, <-chan flight.StreamChunk, error) {
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
			ch <- flight.StreamChunk{Data: rec}
		}
	}()
	return schema, ch, nil
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
	s.cache.Set(query, data, 60*time.Second)
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
