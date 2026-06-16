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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
)

// Handler implements connhandler.ConnectionHandler for the ClickHouse native
// protocol. It accepts native connections, extracts SQL queries, routes them
// through a standard http.Handler (which is the ClickHouse backend's query
// handler), and transcodes the JSON response back to native data blocks.
type Handler struct {
	// QueryHandler is the HTTP handler that processes ClickHouse queries
	// (typically the backend's router or QueryHandler).
	QueryHandler http.Handler
}

// HandleConnection implements connhandler.ConnectionHandler.
func (h *Handler) HandleConnection(ctx context.Context, conn net.Conn) error {
	r := newProtoReader(conn)
	bw := bufio.NewWriterSize(conn, 128*1024)
	w := newProtoWriter(bw)

	// --- handshake ---
	pktType, err := r.ReadByte()
	if err != nil {
		return fmt.Errorf("read hello packet type: %w", err)
	}
	if pktType != ClientHello {
		return fmt.Errorf("expected ClientHello (0), got %d", pktType)
	}
	hello, err := readClientHello(r)
	if err != nil {
		return fmt.Errorf("read client hello: %w", err)
	}

	if err := writeServerHello(w); err != nil {
		return fmt.Errorf("write server hello: %w", err)
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("flush server hello: %w", err)
	}

	clientRevision := hello.ProtoRevision

	// After receiving ServerHello, clients with revision >= 54458 send an
	// addendum (currently just a quota key string). Read and discard it.
	if clientRevision >= RevisionAddendum {
		if _, err := r.str(); err != nil {
			return fmt.Errorf("read client addendum: %w", err)
		}
	}

	// --- connection loop ---
	for {
		if ctx.Err() != nil {
			return nil
		}

		pktType, err = r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read packet type: %w", err)
		}

		switch pktType {
		case ClientPing:
			if err := writePong(w); err != nil {
				return err
			}
			if err := bw.Flush(); err != nil {
				return err
			}

		case ClientQuery:
			q, err := readClientQuery(r, clientRevision)
			if err != nil {
				_ = writeException(w, 62, "DB::Exception", err.Error())
				_ = bw.Flush()
				return err
			}

			// After the query, the client sends an empty data block.
			// For SELECT this is always empty; INSERT with inline data
			// is not yet supported via the native protocol proxy.
			dataPkt, err := r.ReadByte()
			if err != nil {
				return fmt.Errorf("read post-query data: %w", err)
			}
			if dataPkt == ClientData {
				if err := skipClientData(r, clientRevision); err != nil {
					return fmt.Errorf("skip post-query data block: %w", err)
				}
			}

			if err := h.handleQuery(ctx, w, bw, q, hello.Database, q.Compression); err != nil {
				return err
			}

		case ClientCancel:
			// nothing to cancel in the proxy case
			continue

		case ClientData:
			// unexpected data outside query context, skip it
			if err := skipClientData(r, clientRevision); err != nil {
				return fmt.Errorf("skip unexpected data: %w", err)
			}

		default:
			return fmt.Errorf("unknown packet type: %d", pktType)
		}
	}
}

// handleQuery creates a synthetic HTTP request, invokes the QueryHandler,
// parses the JSON response, and writes native protocol data blocks.
func (h *Handler) handleQuery(ctx context.Context, w *protoWriter, bw *bufio.Writer, q *ClientQueryMsg, database string, compressed bool) error {
	sql := q.SQL
	upper := strings.ToUpper(strings.TrimSpace(sql))
	isSelect := strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "WITH")
	// Ensure SELECT queries return JSON so the response can be transcoded
	// to native data blocks. Non-SELECT (INSERT, CREATE, etc.) are proxied
	// as-is without format modification.
	if isSelect && !strings.Contains(upper, "FORMAT ") {
		sql = strings.TrimRight(sql, "; \t\n") + " FORMAT JSON"
	}

	// Use "/" as the path — the backend router handles paths relative to
	// the backend, not the full frontend path (e.g. "/click1/").
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(sql))
	if err != nil {
		return writeQueryError(w, bw, err)
	}
	if database != "" {
		qp := req.URL.Query()
		qp.Set("database", database)
		req.URL.RawQuery = qp.Encode()
	}
	req.Header.Set("Content-Type", "text/plain")

	rec := httptest.NewRecorder()
	h.QueryHandler.ServeHTTP(rec, req)
	resp := rec.Result()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		msg := string(body)
		if msg == "" {
			msg = resp.Status
		}
		return writeQueryError(w, bw, fmt.Errorf("%s", msg))
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return writeQueryError(w, bw, err)
	}

	if err := writeJSONAsNativeBlocks(w, body, compressed); err != nil {
		return writeQueryError(w, bw, err)
	}

	if err := writeEndOfStream(w); err != nil {
		return err
	}
	return bw.Flush()
}

func writeQueryError(w *protoWriter, bw *bufio.Writer, err error) error {
	_ = writeException(w, 62, "DB::Exception", err.Error())
	_ = bw.Flush()
	return nil
}

type wfMetaItem struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type wfDocument struct {
	Meta []wfMetaItem     `json:"meta"`
	Data []map[string]any `json:"data"`
	Rows *int             `json:"rows"`
}

// writeJSONAsNativeBlocks parses a ClickHouse JSON response and writes it as
// native protocol data blocks.
func writeJSONAsNativeBlocks(w *protoWriter, body []byte, compressed bool) error {
	var doc wfDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		// Not JSON — might be a non-SELECT result. Send empty block.
		return writeEmptyBlock(w)
	}

	if len(doc.Meta) == 0 || len(doc.Data) == 0 {
		return writeEmptyBlock(w)
	}

	columns := make([]Column, len(doc.Meta))
	for i, m := range doc.Meta {
		columns[i] = Column(m)
	}

	numRows := uint64(len(doc.Data))
	// Convert row-major data to column-major
	colValues := make([][]any, len(columns))
	for i := range colValues {
		colValues[i] = make([]any, numRows)
	}
	for rowIdx, row := range doc.Data {
		for colIdx, col := range columns {
			colValues[colIdx][rowIdx] = row[col.Name]
		}
	}

	if compressed {
		return writeCompressedDataBlock(w, columns, colValues, numRows)
	}
	return writeDataBlock(w, columns, colValues, numRows)
}
