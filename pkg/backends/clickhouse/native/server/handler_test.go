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

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// echoJSONHandler returns a ClickHouse-shaped JSON response with the SQL
// query echoed back as a single-row result.
func echoJSONHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		doc := map[string]any{
			"meta": []map[string]string{
				{"name": "query", "type": "String"},
			},
			"data": []map[string]any{
				{"query": string(body)},
			},
			"rows": 1,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(doc)
	})
}

func TestHandlerPingPong(t *testing.T) {
	h := &Handler{
		QueryHandler: echoJSONHandler(),
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.HandleConnection(ctx, serverConn)
	}()

	cw := newProtoWriter(clientConn)
	cr := newProtoReader(clientConn)

	// Send ClientHello
	cw.putByte(ClientHello)
	cw.putStr("test-client")
	cw.putUvarint(1)
	cw.putUvarint(0)
	cw.putUvarint(ServerRevision)
	cw.putStr("default")
	cw.putStr("")
	cw.putStr("")

	// Read ServerHello
	pkt, err := cr.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	if pkt != ServerHello {
		t.Fatalf("expected ServerHello, got %d", pkt)
	}
	// skip rest of hello
	cr.str() // name
	cr.uvarint()
	cr.uvarint()
	cr.uvarint()
	cr.str() // tz
	cr.str() // display
	cr.uvarint()

	// Send addendum (quota key, required for revision >= 54458)
	cw.putStr("")

	// Send Ping
	cw.putByte(ClientPing)

	// Read Pong
	pkt, err = cr.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	if pkt != ServerPong {
		t.Fatalf("expected ServerPong (%d), got %d", ServerPong, pkt)
	}

	// Close client side
	clientConn.Close()
	cancel()
}

func TestHandlerQuery(t *testing.T) {
	h := &Handler{
		QueryHandler: echoJSONHandler(),
	}

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.HandleConnection(ctx, serverConn)
	}()

	cw := newProtoWriter(clientConn)
	cr := newProtoReader(clientConn)

	// Send ClientHello
	cw.putByte(ClientHello)
	cw.putStr("test-client")
	cw.putUvarint(1)
	cw.putUvarint(0)
	cw.putUvarint(ServerRevision)
	cw.putStr("default")
	cw.putStr("")
	cw.putStr("")

	// Read ServerHello
	pkt, _ := cr.ReadByte()
	if pkt != ServerHello {
		t.Fatalf("expected ServerHello, got %d", pkt)
	}
	cr.str()
	cr.uvarint()
	cr.uvarint()
	cr.uvarint()
	cr.str()
	cr.str()
	cr.uvarint()

	// Send addendum (quota key, required for revision >= 54458)
	cw.putStr("")

	// Send ClientQuery
	sendTestQuery(t, cw, "SELECT 1")

	// Send empty ClientData block (required after query)
	sendEmptyDataBlock(cw)

	// Read response — should be ServerData
	pkt, err := cr.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	if pkt != ServerData {
		t.Fatalf("expected ServerData (%d), got %d", ServerData, pkt)
	}

	// Read block name
	blockName, _ := cr.str()
	if blockName != "" {
		t.Fatalf("expected empty block name, got %q", blockName)
	}

	// Skip block info
	cr.uvarint()
	cr.boolean()
	cr.uvarint()
	cr.int32()
	cr.uvarint()

	numCols, _ := cr.uvarint()
	numRows, _ := cr.uvarint()
	if numCols != 1 {
		t.Fatalf("expected 1 column, got %d", numCols)
	}
	if numRows != 1 {
		t.Fatalf("expected 1 row, got %d", numRows)
	}

	colName, _ := cr.str()
	if colName != "query" {
		t.Fatalf("expected column name %q, got %q", "query", colName)
	}

	// Read EndOfStream
	colType, _ := cr.str()
	_ = colType
	cr.boolean() // custom serialization flag
	val, _ := cr.str()
	if val != "SELECT 1 FORMAT JSON" {
		t.Fatalf("expected query value %q, got %q", "SELECT 1 FORMAT JSON", val)
	}

	pkt, _ = cr.ReadByte()
	if pkt != ServerEndOfStream {
		t.Fatalf("expected ServerEndOfStream (%d), got %d", ServerEndOfStream, pkt)
	}

	clientConn.Close()
	cancel()
}

func TestWriteJSONAsNativeBlocks(t *testing.T) {
	doc := map[string]any{
		"meta": []map[string]string{
			{"name": "n", "type": "UInt32"},
		},
		"data": []map[string]any{
			{"n": float64(42)},
		},
		"rows": 1,
	}
	body, _ := json.Marshal(doc)

	var buf bytes.Buffer
	w := newProtoWriter(&buf)
	if err := writeJSONAsNativeBlocks(w, body, false); err != nil {
		t.Fatal(err)
	}

	r := newProtoReader(&buf)
	pkt, _ := r.ReadByte()
	if pkt != ServerData {
		t.Fatalf("expected ServerData, got %d", pkt)
	}
}

// sendTestQuery writes a minimal ClientQuery with the given SQL.
func sendTestQuery(t *testing.T, w *protoWriter, sql string) {
	t.Helper()
	w.putByte(ClientQuery)
	w.putStr("")          // query ID
	w.putByte(1)          // query kind = initial
	w.putStr("")          // initial user
	w.putStr("")          // initial query ID
	w.putStr("127.0.0.1") // initial address
	// initial query start time (8 bytes, revision >= 54449)
	w.putInt64(0)
	w.putByte(1)  // interface = TCP
	w.putStr("")  // os user
	w.putStr("")  // os hostname
	w.putStr("t") // client name
	w.putUvarint(1)
	w.putUvarint(0)
	w.putUvarint(ServerRevision) // client protocol revision
	w.putStr("")                 // quota key (revision >= 54060)
	w.putUvarint(0)              // distributed depth (revision >= 54448)
	w.putUvarint(0)              // version patch (revision >= 54401)
	w.putByte(0)                 // no otel span (revision >= 54442)
	// parallel replicas (revision >= 54453)
	w.putUvarint(0)
	w.putUvarint(0)
	w.putUvarint(0)
	// end of client info

	// settings (empty)
	w.putStr("")
	// interserver secret (revision >= 54441)
	w.putStr("")
	// state
	w.putUvarint(2) // StateComplete
	// compression
	w.putBool(false)
	// SQL
	w.putStr(sql)
	// parameters (revision >= 54459)
	w.putStr("")
}

// sendEmptyDataBlock writes an empty ClientData block.
func sendEmptyDataBlock(w *protoWriter) {
	w.putByte(ClientData)
	w.putStr("") // block name
	// block info
	w.putUvarint(0)  // is_overflows
	w.putBool(false) // bucket_num
	w.putUvarint(2)  // bucket_size
	w.putInt32(-1)   // reserved
	w.putUvarint(0)  // reserved
	// num columns, num rows
	w.putUvarint(0)
	w.putUvarint(0)
}
