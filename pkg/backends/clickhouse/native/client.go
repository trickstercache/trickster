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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// NativeClient wraps a clickhouse-go native connection pool and exposes a
// Fetcher compatible with bo.Options.Fetcher.
type NativeClient struct {
	db *sql.DB
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
	db := clickhouse.OpenDB(&clickhouse.Options{
		Addr:     []string{addr},
		Protocol: clickhouse.Native,
	})
	return &NativeClient{db: db}, nil
}

// Close closes the underlying connection pool.
func (nc *NativeClient) Close() error {
	return nc.db.Close()
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

	rows, err := nc.db.QueryContext(r.Context(), sql) // #nosec G701 -- proxy passthrough; SQL is forwarded verbatim from client
	if err != nil {
		return syntheticErrorResponse(http.StatusBadGateway, err), nil
	}
	defer rows.Close()

	colNames, err := rows.Columns()
	if err != nil {
		return syntheticErrorResponse(http.StatusBadGateway, err), nil
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return syntheticErrorResponse(http.StatusBadGateway, err), nil
	}

	meta := make([]map[string]string, len(colNames))
	for i, name := range colNames {
		meta[i] = map[string]string{
			"name": name,
			"type": colTypes[i].DatabaseTypeName(),
		}
	}

	data := make([]map[string]any, 0, 64)
	for rows.Next() {
		scanDest := make([]any, len(colNames))
		ptrs := make([]any, len(colNames))
		for i := range scanDest {
			ptrs[i] = &scanDest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return syntheticErrorResponse(http.StatusBadGateway, err), nil
		}
		row := make(map[string]any, len(colNames))
		for i, name := range colNames {
			row[name] = scanDest[i]
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
