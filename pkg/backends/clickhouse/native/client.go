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

// Package native provides a ClickHouse native protocol (port 9000) egress
// adapter. It translates HTTP-shaped proxy requests into native protocol
// queries via clickhouse-go and returns HTTP-shaped responses with JSON bodies
// that the existing ClickHouse Modeler can consume.
package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// NativeClient wraps a clickhouse-go native connection pool and exposes a
// Fetcher compatible with bo.Options.Fetcher.
type NativeClient struct {
	conn driver.Conn
}

// NewNativeClient creates a NativeClient from the backend options. The
// origin URL host:port is used as the native protocol address.
func NewNativeClient(o *bo.Options) (*NativeClient, error) {
	addr := o.Host
	if addr == "" {
		return nil, errors.New("clickhouse native: origin host is empty")
	}
	if !strings.Contains(addr, ":") {
		addr += ":9000"
	}
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:     []string{addr},
		Protocol: clickhouse.Native,
	})
	if err != nil {
		return nil, fmt.Errorf("clickhouse native: open: %w", err)
	}
	return &NativeClient{conn: conn}, nil
}

// Close closes the underlying connection pool.
func (nc *NativeClient) Close() error {
	return nc.conn.Close()
}

// Fetch executes the SQL query embedded in the *http.Request (body or query
// param) against ClickHouse using the native protocol, and returns a
// synthetic *http.Response with a JSON body in ClickHouse WFDocument format:
//
//	{"meta":[{"name":"col","type":"Type"},...], "data":[{"col":"val",...},...], "rows":N}
//
// This matches what the existing HTTP path returns when FORMAT JSON is used.
func (nc *NativeClient) Fetch(r *http.Request) (*http.Response, error) {
	sql, err := extractSQL(r)
	if err != nil {
		return syntheticErrorResponse(http.StatusBadRequest, err), nil
	}

	rows, err := nc.conn.Query(r.Context(), sql)
	if err != nil {
		return syntheticErrorResponse(http.StatusBadGateway, err), nil
	}
	defer rows.Close()

	colTypes := rows.ColumnTypes()
	colNames := rows.Columns()

	meta := make([]map[string]string, len(colNames))
	for i, name := range colNames {
		meta[i] = map[string]string{
			"name": name,
			"type": colTypes[i].DatabaseTypeName(),
		}
	}

	data := make([]map[string]any, 0, 64)
	scanDest := make([]any, len(colNames))
	for i := range scanDest {
		scanDest[i] = new(any)
	}

	for rows.Next() {
		if err := rows.Scan(scanDest...); err != nil {
			return syntheticErrorResponse(http.StatusBadGateway, err), nil
		}
		row := make(map[string]any, len(colNames))
		for i, name := range colNames {
			row[name] = *(scanDest[i].(*any))
		}
		data = append(data, row)
	}
	if err := rows.Err(); err != nil {
		return syntheticErrorResponse(http.StatusBadGateway, err), nil
	}

	rowCount := len(data)
	doc := map[string]any{
		"meta": meta,
		"data": data,
		"rows": rowCount,
	}

	body, err := json.Marshal(doc)
	if err != nil {
		return syntheticErrorResponse(http.StatusInternalServerError, err), nil
	}

	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       r,
	}, nil
}

func extractSQL(r *http.Request) (string, error) {
	if r.Body != nil && r.Body != http.NoBody {
		b, err := request.GetBody(r)
		if err != nil {
			return "", err
		}
		if len(b) > 0 {
			return string(b), nil
		}
	}
	if q := r.URL.Query().Get("query"); q != "" {
		return q, nil
	}
	return "", errors.New("no SQL query found in request")
}

func syntheticErrorResponse(code int, err error) *http.Response {
	body := []byte(err.Error())
	return &http.Response{
		StatusCode:    code,
		Status:        fmt.Sprintf("%d %s", code, http.StatusText(code)),
		Header:        http.Header{"Content-Type": {"text/plain"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}
