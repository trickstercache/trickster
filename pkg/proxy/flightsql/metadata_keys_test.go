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
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
)

// fakeXdbcTypeInfo implements flightsql.GetXdbcTypeInfo.
type fakeXdbcTypeInfo struct {
	dataType *int32
}

func (f fakeXdbcTypeInfo) GetDataType() *int32 { return f.dataType }

var _ flightsql.GetXdbcTypeInfo = fakeXdbcTypeInfo{}

func drain(t *testing.T, ch <-chan flight.StreamChunk, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range ch {
		chunk.Data.Release()
	}
}

func tableRef(catalog, schema *string, table string) flightsql.TableRef {
	return flightsql.TableRef{Catalog: catalog, DBSchema: schema, Table: table}
}

// TestKeyMetadataRPCsProxyAndCache verifies the type-info and key-discovery
// RPCs reach the upstream, are cached per request shape, and re-fetch when the
// request shape changes rather than returning Unimplemented.
func TestKeyMetadataRPCsProxyAndCache(t *testing.T) {
	dataType := int32(4)
	other := "other"
	tests := []struct {
		name  string
		call  func(*Server, int) (*arrow.Schema, <-chan flight.StreamChunk, error)
		calls func(*fakeUpstream) int
	}{
		{"xdbc type info",
			func(s *Server, round int) (*arrow.Schema, <-chan flight.StreamChunk, error) {
				cmd := fakeXdbcTypeInfo{}
				if round == 2 {
					cmd.dataType = &dataType
				}
				return s.DoGetXdbcTypeInfo(context.Background(), cmd)
			},
			func(f *fakeUpstream) int { return f.xdbcTypeInfoCalls }},
		{"primary keys",
			func(s *Server, round int) (*arrow.Schema, <-chan flight.StreamChunk, error) {
				ref := tableRef(nil, nil, "cpu")
				if round == 2 {
					ref = tableRef(&other, nil, "cpu")
				}
				return s.DoGetPrimaryKeys(context.Background(), ref)
			},
			func(f *fakeUpstream) int { return f.primaryKeysCalls }},
		{"exported keys",
			func(s *Server, round int) (*arrow.Schema, <-chan flight.StreamChunk, error) {
				ref := tableRef(nil, nil, "cpu")
				if round == 2 {
					ref = tableRef(nil, nil, "mem")
				}
				return s.DoGetExportedKeys(context.Background(), ref)
			},
			func(f *fakeUpstream) int { return f.exportedKeysCalls }},
		{"imported keys",
			func(s *Server, round int) (*arrow.Schema, <-chan flight.StreamChunk, error) {
				ref := tableRef(nil, nil, "cpu")
				if round == 2 {
					ref = tableRef(nil, &other, "cpu")
				}
				return s.DoGetImportedKeys(context.Background(), ref)
			},
			func(f *fakeUpstream) int { return f.importedKeysCalls }},
		{"cross reference",
			func(s *Server, round int) (*arrow.Schema, <-chan flight.StreamChunk, error) {
				ref := flightsql.CrossTableRef{
					PKRef: tableRef(nil, nil, "cpu"),
					FKRef: tableRef(nil, nil, "mem"),
				}
				if round == 2 {
					ref.FKRef = tableRef(nil, nil, "disk")
				}
				return s.DoGetCrossReference(context.Background(), ref)
			},
			func(f *fakeUpstream) int { return f.crossReferenceCalls }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up := &fakeUpstream{ipcBytes: buildTestIPC(t)}
			srv := NewServer(up, newMemCache())
			// same request twice → one upstream call
			for range 2 {
				schema, ch, err := tc.call(srv, 1)
				drain(t, ch, err)
				if schema == nil {
					t.Fatal("expected a schema")
				}
			}
			if got := tc.calls(up); got != 1 {
				t.Fatalf("upstream calls = %d, want 1 (second cached)", got)
			}
			// a different request shape must not alias the first
			_, ch, err := tc.call(srv, 2)
			drain(t, ch, err)
			if got := tc.calls(up); got != 2 {
				t.Errorf("upstream calls = %d, want 2 (distinct cache key)", got)
			}
		})
	}
}

// TestKeyMetadataFlightInfo verifies each new metadata RPC advertises a single
// endpoint carrying the command bytes, matching the other metadata families.
func TestKeyMetadataFlightInfo(t *testing.T) {
	srv := NewServer(&fakeUpstream{}, newMemCache())
	desc := &flight.FlightDescriptor{Cmd: []byte("cmd")}
	ref := tableRef(nil, nil, "cpu")
	infos := []func() (*flight.FlightInfo, error){
		func() (*flight.FlightInfo, error) {
			return srv.GetFlightInfoXdbcTypeInfo(context.Background(), fakeXdbcTypeInfo{}, desc)
		},
		func() (*flight.FlightInfo, error) {
			return srv.GetFlightInfoPrimaryKeys(context.Background(), ref, desc)
		},
		func() (*flight.FlightInfo, error) {
			return srv.GetFlightInfoExportedKeys(context.Background(), ref, desc)
		},
		func() (*flight.FlightInfo, error) {
			return srv.GetFlightInfoImportedKeys(context.Background(), ref, desc)
		},
		func() (*flight.FlightInfo, error) {
			return srv.GetFlightInfoCrossReference(context.Background(),
				flightsql.CrossTableRef{PKRef: ref, FKRef: ref}, desc)
		},
	}
	for i, build := range infos {
		info, err := build()
		if err != nil {
			t.Fatalf("info %d: %v", i, err)
		}
		if len(info.Endpoint) != 1 || info.Schema == nil {
			t.Errorf("info %d: endpoints=%d schema=%v", i, len(info.Endpoint), info.Schema != nil)
		}
	}
}

// TestTableRefKeyDistinguishesNilFromEmpty verifies a nil catalog (any
// catalog) and an empty one (no catalog) produce different cache keys.
func TestTableRefKeyDistinguishesNilFromEmpty(t *testing.T) {
	empty := ""
	if tableRefKey(tableRef(nil, nil, "cpu")) == tableRefKey(tableRef(&empty, nil, "cpu")) {
		t.Error("nil and empty catalogs produced the same key")
	}
}

func TestDerefInt32(t *testing.T) {
	if got := derefInt32(nil); got != "" {
		t.Errorf("derefInt32(nil) = %q, want empty", got)
	}
	value := int32(-7)
	if got := derefInt32(&value); got != "-7" {
		t.Errorf("derefInt32(-7) = %q, want -7", got)
	}
}
