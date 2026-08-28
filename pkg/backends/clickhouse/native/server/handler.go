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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/aftership"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Handler translates native requests into the backend's HTTP handler pipeline.
type Handler struct {
	// QueryHandler is the HTTP handler that processes ClickHouse queries
	// (typically the backend's router or QueryHandler).
	QueryHandler http.Handler
}

// HandleConnection serves a ClickHouse native session.
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

	clientRevision := min(hello.ProtoRevision, ServerRevision)

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

			// Only the empty post-query block is supported; inline INSERT data is rejected.
			dataPkt, err := r.ReadByte()
			if err != nil {
				return fmt.Errorf("read post-query data: %w", err)
			}
			if dataPkt != ClientData {
				return fmt.Errorf("expected post-query data packet, got %d", dataPkt)
			}
			if err := skipClientData(r, clientRevision, q.Compression); err != nil {
				_ = writeQueryError(w, bw, err)
				return err
			}

			q.Username, q.Password = hello.Username, hello.Password
			if err := h.handleQuery(ctx, w, bw, q, hello.Database, q.Compression, clientRevision); err != nil {
				return err
			}

		case ClientCancel:
			// nothing to cancel in the proxy case
			continue

		case ClientData:
			// unexpected data outside query context, skip it
			if err := skipClientData(r, clientRevision, false); err != nil {
				return fmt.Errorf("skip unexpected data: %w", err)
			}

		default:
			return fmt.Errorf("unknown packet type: %d", pktType)
		}
	}
}

func (h *Handler) handleQuery(
	ctx context.Context,
	w *protoWriter,
	bw *bufio.Writer,
	q *ClientQueryMsg,
	database string,
	compressed bool,
	revision uint64,
) error {
	sql, _, isSelect, err := aftership.SplitFormat(q.SQL, "JSON")
	if err != nil {
		return writeQueryError(w, bw, err)
	}
	if isSelect {
		sql += " FORMAT JSON"
		trq := &timeseries.TimeRangeQuery{}
		trq.ExtractBackfillTolerance(q.SQL)
		if trq.BackfillTolerance > 0 {
			sql += fmt.Sprintf(" /* trickster-backfill-tolerance:%d */", trq.BackfillTolerance/time.Second)
		}
		options := &timeseries.RequestOptions{}
		options.ExtractFastForwardDisabled(q.SQL)
		if options.FastForwardDisable {
			sql += " /* trickster-fast-forward:off */"
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(sql))
	if err != nil {
		return writeQueryError(w, bw, err)
	}
	qp := req.URL.Query()
	for key, value := range q.Settings {
		qp.Set(key, value)
	}
	for key, value := range q.Parameters {
		qp.Set("param_"+key, value)
	}
	if database != "" {
		qp.Set("database", database)
	}
	qp.Set("query_id", q.QueryID)
	qp.Set("default_format", "JSON")
	req.URL.RawQuery = qp.Encode()
	if q.Username != "" {
		req.SetBasicAuth(q.Username, q.Password)
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

	if err := writeJSONAsNativeBlocks(w, body, compressed, revision); err != nil {
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
func writeJSONAsNativeBlocks(w *protoWriter, body []byte, compressed bool, revision uint64) error {
	var doc wfDocument
	if len(bytes.TrimSpace(body)) == 0 {
		return writeEmptyBlockRevision(w, revision)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return fmt.Errorf("invalid ClickHouse JSON response: %w", err)
	}

	if len(doc.Meta) == 0 {
		return writeEmptyBlockRevision(w, revision)
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
		return writeCompressedDataBlockRevision(w, columns, colValues, numRows, revision)
	}
	return writeDataBlockRevision(w, columns, colValues, numRows, revision)
}
