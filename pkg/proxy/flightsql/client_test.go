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
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// partitionedUpstream is a minimal upstream Flight SQL server that answers a
// statement query with one endpoint per partition, so the client's endpoint
// aggregation and location handling can be exercised end to end. Each
// partition's ticket carries its own row values.
type partitionedUpstream struct {
	flightsql.BaseServer
	// partitions holds the row values each endpoint's ticket resolves to.
	partitions [][]int64
	// locations, when set, is applied to every endpoint after the first, so a
	// partition can be directed at another server.
	locations []string
	// mismatchSchema makes the final partition answer with a different schema.
	mismatchSchema bool
	// doGets counts ticket resolutions served by this instance.
	doGets int
}

func partitionSchema() *arrow.Schema {
	return arrow.NewSchema([]arrow.Field{
		{Name: "v", Type: arrow.PrimitiveTypes.Int64},
	}, nil)
}

func (u *partitionedUpstream) GetFlightInfoStatement(_ context.Context,
	_ flightsql.StatementQuery, desc *flight.FlightDescriptor,
) (*flight.FlightInfo, error) {
	endpoints := make([]*flight.FlightEndpoint, 0, len(u.partitions))
	for i := range u.partitions {
		ticket, err := flightsql.CreateStatementQueryTicket(
			[]byte(strconv.Itoa(i)))
		if err != nil {
			return nil, err
		}
		endpoint := &flight.FlightEndpoint{Ticket: &flight.Ticket{Ticket: ticket}}
		if i > 0 && i-1 < len(u.locations) {
			endpoint.Location = []*flight.Location{{Uri: u.locations[i-1]}}
		}
		endpoints = append(endpoints, endpoint)
	}
	return &flight.FlightInfo{
		FlightDescriptor: desc, Endpoint: endpoints,
		TotalRecords: -1, TotalBytes: -1,
	}, nil
}

func (u *partitionedUpstream) DoGetStatement(ctx context.Context,
	ticket flightsql.StatementQueryTicket,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	u.doGets++
	index, err := strconv.Atoi(string(ticket.GetStatementHandle()))
	if err != nil || index >= len(u.partitions) {
		return nil, nil, status.Error(codes.NotFound, "unknown partition")
	}
	schema := partitionSchema()
	if u.mismatchSchema && index == len(u.partitions)-1 {
		schema = arrow.NewSchema([]arrow.Field{
			{Name: "other", Type: arrow.BinaryTypes.String},
		}, nil)
	}
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer builder.Release()
	for _, value := range u.partitions[index] {
		switch typed := builder.Field(0).(type) {
		case *array.Int64Builder:
			typed.Append(value)
		case *array.StringBuilder:
			typed.Append(strconv.FormatInt(value, 10))
		}
	}
	record := builder.NewRecordBatch()
	ch := make(chan flight.StreamChunk, 1)
	ch <- flight.StreamChunk{Data: record}
	close(ch)
	return schema, ch, nil
}

// startUpstream serves an upstream Flight SQL server on an ephemeral port.
func startUpstream(t *testing.T, up *partitionedUpstream) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	flight.RegisterFlightServiceServer(g, flightsql.NewFlightServer(up))
	go func() { _ = g.Serve(l) }()
	t.Cleanup(g.Stop)
	return l.Addr().String()
}

// newUpstreamClient dials a test upstream and closes it during cleanup.
func newUpstreamClient(t *testing.T, cfg UpstreamConfig) *Client {
	t.Helper()
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// executedValues runs a statement through the client and returns every value
// the response's single int64 column carries.
func executedValues(t *testing.T, client *Client) []int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ipcBytes, err := client.Execute(ctx, "SELECT v FROM t")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_, records, err := DecodeRecords(ipcBytes)
	if err != nil {
		t.Fatalf("DecodeRecords: %v", err)
	}
	var out []int64
	for _, record := range records {
		column := record.Column(0).(*array.Int64)
		for row := range int(record.NumRows()) {
			out = append(out, column.Value(row))
		}
		record.Release()
	}
	return out
}

// TestClientConsumesEveryEndpoint verifies a FlightInfo partitioned across
// several endpoints is aggregated rather than truncated to the first.
func TestClientConsumesEveryEndpoint(t *testing.T) {
	up := &partitionedUpstream{partitions: [][]int64{{1, 2}, {3}, {4, 5, 6}}}
	client := newUpstreamClient(t, UpstreamConfig{Address: startUpstream(t, up)})
	got := executedValues(t, client)
	want := []int64{1, 2, 3, 4, 5, 6}
	if len(got) != len(want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("values = %v, want %v", got, want)
		}
	}
	if up.doGets != 3 {
		t.Errorf("resolved %d tickets, want 3", up.doGets)
	}
}

// TestClientHonorsEndpointLocations verifies a partition advertised at another
// location is retrieved from that location rather than the primary connection.
func TestClientHonorsEndpointLocations(t *testing.T) {
	remote := &partitionedUpstream{partitions: [][]int64{{0}, {7, 8}}}
	remoteAddr := startUpstream(t, remote)
	primary := &partitionedUpstream{
		partitions: [][]int64{{1, 2}, {0, 0}},
		locations:  []string{"grpc+tcp://" + remoteAddr},
	}
	client := newUpstreamClient(t, UpstreamConfig{
		Address: startUpstream(t, primary), AllowedLocationHosts: []string{remoteAddr},
	})

	got := executedValues(t, client)
	want := []int64{1, 2, 7, 8}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	if primary.doGets != 1 {
		t.Errorf("primary resolved %d tickets, want 1", primary.doGets)
	}
	if remote.doGets != 1 {
		t.Errorf("remote resolved %d tickets, want 1", remote.doGets)
	}
	// the location client is cached and reused across requests
	executedValues(t, client)
	if len(client.locations) != 1 {
		t.Errorf("cached %d location clients, want 1", len(client.locations))
	}
}

// TestClientReusesConnectionForSelfLocations verifies the reuse-connection
// marker and a location naming the configured upstream both stay on the
// primary connection.
func TestClientReusesConnectionForSelfLocations(t *testing.T) {
	up := &partitionedUpstream{partitions: [][]int64{{1}, {2}, {3}}}
	addr := startUpstream(t, up)
	up.locations = []string{flight.LocationReuseConnection, "grpc+tcp://" + addr}
	client := newUpstreamClient(t, UpstreamConfig{Address: addr})

	got := executedValues(t, client)
	if fmt.Sprint(got) != fmt.Sprint([]int64{1, 2, 3}) {
		t.Fatalf("values = %v, want [1 2 3]", got)
	}
	if len(client.locations) != 0 {
		t.Errorf("dialed %d location clients, want 0", len(client.locations))
	}
}

// TestClientRejectsMismatchedEndpointSchemas verifies partitions that disagree
// on schema fail rather than producing a truncated or malformed stream.
func TestClientRejectsMismatchedEndpointSchemas(t *testing.T) {
	up := &partitionedUpstream{
		partitions:     [][]int64{{1}, {2}},
		mismatchSchema: true,
	}
	client := newUpstreamClient(t, UpstreamConfig{Address: startUpstream(t, up)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Execute(ctx, "SELECT v FROM t"); err == nil {
		t.Fatal("expected a schema mismatch error")
	}
}

// TestClientBoundsResponseSize verifies an oversized response is refused with
// ResourceExhausted instead of buffering without bound.
func TestClientBoundsResponseSize(t *testing.T) {
	values := make([]int64, 4096)
	for i := range values {
		values[i] = int64(i)
	}
	up := &partitionedUpstream{partitions: [][]int64{values}}
	addr := startUpstream(t, up)

	client := newUpstreamClient(t, UpstreamConfig{Address: addr, MaxResponseBytes: 512})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.Execute(ctx, "SELECT v FROM t")
	if err == nil {
		t.Fatal("expected the response to be refused")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("err = %v (code %s), want ResourceExhausted", err, status.Code(err))
	}
	aggregate := newUpstreamClient(t, UpstreamConfig{
		Address: addr, MaxResponseBytes: -1, MaxBufferedBytes: 512,
	})
	if _, err := aggregate.Execute(ctx, "SELECT v FROM t"); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("aggregate err = %v (code %s), want ResourceExhausted", err, status.Code(err))
	}

	// negative bounds remove both limits
	unbounded := newUpstreamClient(t, UpstreamConfig{
		Address: addr, MaxResponseBytes: -1, MaxBufferedBytes: -1,
	})
	if unbounded.maxResponseBytes != 0 {
		t.Errorf("maxResponseBytes = %d, want 0 (unbounded)", unbounded.maxResponseBytes)
	}
	if got := executedValues(t, unbounded); len(got) != len(values) {
		t.Errorf("unbounded response rows = %d, want %d", len(got), len(values))
	}
}

// TestClientDefaultsResponseBound verifies an unset bound resolves to the
// package default rather than to unbounded buffering.
func TestClientDefaultsResponseBound(t *testing.T) {
	up := &partitionedUpstream{partitions: [][]int64{{1}}}
	client := newUpstreamClient(t, UpstreamConfig{Address: startUpstream(t, up)})
	if client.maxResponseBytes != DefaultMaxResponseBytes {
		t.Errorf("maxResponseBytes = %d, want %d",
			client.maxResponseBytes, DefaultMaxResponseBytes)
	}
	if client.bufferBudget == nil || client.bufferBudget.limit != DefaultMaxBufferedBytes {
		t.Errorf("buffer budget = %+v, want %d bytes", client.bufferBudget, DefaultMaxBufferedBytes)
	}
}

func TestBoundedWriterOverrun(t *testing.T) {
	w := &boundedWriter{w: &bytes.Buffer{}, remaining: 4, bounded: true}
	if _, err := w.Write([]byte("abcd")); err != nil {
		t.Fatalf("write within bound: %v", err)
	}
	if _, err := w.Write([]byte("e")); !errors.Is(err, errResponseTooLarge) {
		t.Errorf("err = %v, want %v", err, errResponseTooLarge)
	}
}

func TestBoundedWriterSharedBudget(t *testing.T) {
	budget := newBufferBudget(6)
	first := &boundedWriter{w: &bytes.Buffer{}, budget: budget}
	second := &boundedWriter{w: &bytes.Buffer{}, budget: budget}
	if _, err := first.Write([]byte("abcd")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := second.Write([]byte("efg")); !errors.Is(err, errBufferBudgetExhausted) {
		t.Fatalf("err = %v, want %v", err, errBufferBudgetExhausted)
	}
	first.Release()
	if _, err := second.Write([]byte("efg")); err != nil {
		t.Fatalf("write after release: %v", err)
	}
	second.Release()
	if used := budget.used.Load(); used != 0 {
		t.Errorf("budget used = %d, want 0", used)
	}
}

func TestClientRejectsUnsafeEndpointLocations(t *testing.T) {
	t.Run("unlisted host", func(t *testing.T) {
		client := &Client{
			cfg: UpstreamConfig{}, locations: make(map[string]flight.Client), maxLocations: 1,
		}
		if _, err := client.locationClient("grpc+tcp://127.0.0.1:1"); err == nil {
			t.Fatal("expected an unlisted host error")
		}
	})
	t.Run("TLS downgrade", func(t *testing.T) {
		client := &Client{
			cfg:       UpstreamConfig{UseTLS: true, AllowedLocationHosts: []string{"127.0.0.1:1"}},
			locations: make(map[string]flight.Client), maxLocations: 1,
		}
		if _, err := client.locationClient("grpc+tcp://127.0.0.1:1"); err == nil ||
			!strings.Contains(err.Error(), "downgrade TLS") {
			t.Fatalf("err = %v, want TLS downgrade error", err)
		}
	})
}

func TestClientCapsEndpointLocationCache(t *testing.T) {
	client := &Client{
		cfg:       UpstreamConfig{AllowedLocationHosts: []string{"127.0.0.1:1", "127.0.0.1:2"}},
		locations: make(map[string]flight.Client), maxLocations: 1,
	}
	first, err := client.locationClient("grpc+tcp://127.0.0.1:1/path-a")
	if err != nil {
		t.Fatalf("first location: %v", err)
	}
	defer first.Close()
	if same, err := client.locationClient("grpc+tcp://127.0.0.1:1/path-b"); err != nil || same != first {
		t.Fatalf("canonical location reuse = (%v, %v), want first client", same, err)
	}
	if _, err := client.locationClient("grpc+tcp://127.0.0.1:2"); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("err = %v (code %s), want ResourceExhausted", err, status.Code(err))
	}
}

func TestClientLocationErrors(t *testing.T) {
	client := &Client{locations: make(map[string]flight.Client)}
	tests := []string{
		"unix:///tmp/flight.sock",
		"grpc+tcp://",
		"://not a url",
	}
	for _, uri := range tests {
		t.Run(uri, func(t *testing.T) {
			if _, err := client.locationClient(uri); err == nil {
				t.Errorf("locationClient(%q) returned no error", uri)
			}
		})
	}
}

func TestEndpointReaderRejectsTicketlessEndpoint(t *testing.T) {
	client := &Client{}
	if _, err := client.endpointReader(context.Background(), nil); err == nil {
		t.Error("expected an error for a nil endpoint")
	}
	if _, err := client.endpointReader(context.Background(),
		&flight.FlightEndpoint{}); err == nil {
		t.Error("expected an error for a ticketless endpoint")
	}
}

// TestClientRPCSurface drives every upstream client method against a real
// Flight SQL server over gRPC — the proxy server itself, backed by a fake
// upstream — so each RPC's request encoding and IPC decoding round-trips.
func TestClientRPCSurface(t *testing.T) {
	up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
	_, addr := startTestServer(t, NewServer(up, newMemCache()))
	client := newUpstreamClient(t, UpstreamConfig{
		Address:             addr,
		ForwardMetadataKeys: []string{"database"},
		DefaultMetadata:     map[string]string{"authorization": "token"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ref := flightsql.TableRef{Table: "cpu"}
	rpcs := []struct {
		name string
		call func() ([]byte, error)
	}{
		{"Execute", func() ([]byte, error) { return client.Execute(ctx, "SELECT 1") }},
		{"GetCatalogs", func() ([]byte, error) { return client.GetCatalogs(ctx) }},
		{"GetDBSchemas", func() ([]byte, error) {
			return client.GetDBSchemas(ctx, &flightsql.GetDBSchemasOpts{})
		}},
		{"GetTables", func() ([]byte, error) {
			return client.GetTables(ctx, &flightsql.GetTablesOpts{})
		}},
		{"GetTableTypes", func() ([]byte, error) { return client.GetTableTypes(ctx) }},
		{"GetSqlInfo", func() ([]byte, error) {
			return client.GetSqlInfo(ctx, []flightsql.SqlInfo{flightsql.SqlInfoFlightSqlServerName})
		}},
		{"GetXdbcTypeInfo", func() ([]byte, error) { return client.GetXdbcTypeInfo(ctx, nil) }},
		{"GetPrimaryKeys", func() ([]byte, error) { return client.GetPrimaryKeys(ctx, ref) }},
		{"GetExportedKeys", func() ([]byte, error) { return client.GetExportedKeys(ctx, ref) }},
		{"GetImportedKeys", func() ([]byte, error) { return client.GetImportedKeys(ctx, ref) }},
		{"GetCrossReference", func() ([]byte, error) {
			return client.GetCrossReference(ctx,
				flightsql.CrossTableRef{PKRef: ref, FKRef: flightsql.TableRef{Table: "mem"}})
		}},
		{"GetExecuteSchema", func() ([]byte, error) {
			return client.GetExecuteSchema(ctx, "SELECT 1")
		}},
	}
	for _, rpc := range rpcs {
		t.Run(rpc.name, func(t *testing.T) {
			data, err := rpc.call()
			if err != nil {
				t.Fatalf("%s: %v", rpc.name, err)
			}
			if len(data) == 0 {
				t.Errorf("%s returned no bytes", rpc.name)
			}
		})
	}

	t.Run("prepared lifecycle", func(t *testing.T) {
		handle, err := client.PrepareStatement(ctx, "SELECT * FROM cpu")
		if err != nil {
			t.Fatalf("PrepareStatement: %v", err)
		}
		if _, err := client.GetPreparedSchema(ctx, handle); err != nil {
			t.Fatalf("GetPreparedSchema: %v", err)
		}
		if _, err := client.ExecutePrepared(ctx, handle); err != nil {
			t.Fatalf("ExecutePrepared: %v", err)
		}
		params := buildParamRecord(t, "cpu0")
		defer params.Release()
		if err := client.SetPreparedStatementParams(ctx, handle, params); err != nil {
			t.Fatalf("SetPreparedStatementParams: %v", err)
		}
		if err := client.ClosePrepared(ctx, handle); err != nil {
			t.Fatalf("ClosePrepared: %v", err)
		}
		// a closed handle is unknown to the client
		if _, err := client.ExecutePrepared(ctx, handle); err == nil {
			t.Error("expected an error executing a closed handle")
		}
		if _, err := client.GetPreparedSchema(ctx, handle); err == nil {
			t.Error("expected an error reading a closed handle's schema")
		}
		if err := client.SetPreparedStatementParams(ctx, handle, params); err == nil {
			t.Error("expected an error binding a closed handle")
		}
		if err := client.ClosePrepared(ctx, handle); err != nil {
			t.Errorf("closing an unknown handle: %v", err)
		}
	})
}

// TestClientForwardsAndDefaultsMetadata verifies inbound metadata named in
// ForwardMetadataKeys reaches the upstream and DefaultMetadata fills the rest.
func TestClientForwardsAndDefaultsMetadata(t *testing.T) {
	client := &Client{cfg: UpstreamConfig{
		ForwardMetadataKeys: []string{"database"},
		DefaultMetadata:     map[string]string{"authorization": "token", "empty": ""},
	}}
	inbound := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("database", "mydb", "ignored", "x"))
	md, ok := metadata.FromOutgoingContext(client.withAuth(inbound))
	if !ok {
		t.Fatal("no outgoing metadata")
	}
	if got := md.Get("database"); len(got) != 1 || got[0] != "mydb" {
		t.Errorf("database = %v, want [mydb]", got)
	}
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "token" {
		t.Errorf("authorization = %v, want [token]", got)
	}
	if len(md.Get("ignored")) != 0 {
		t.Error("forwarded a key not named in ForwardMetadataKeys")
	}
	if len(md.Get("empty")) != 0 {
		t.Error("applied an empty default metadata value")
	}

	// with nothing to send, the context is returned unchanged
	bare := &Client{}
	if _, ok := metadata.FromOutgoingContext(
		bare.withAuth(context.Background())); ok {
		t.Error("expected no outgoing metadata")
	}
}
