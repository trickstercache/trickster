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

// Package native adapts HTTP proxy requests to ClickHouse native upstreams.
package native

import (
	"bytes"
	"crypto/tls"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native/server"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer/aftership"
	"github.com/trickstercache/trickster/v2/pkg/proxy"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"github.com/ClickHouse/clickhouse-go/v2"
)

type sessionKey struct{ database, username, password string }

// NativeClient is an HTTP transport backed by bounded, per-identity native pools.
type NativeClient struct {
	db               *sql.DB
	options          clickhouse.Options
	defaults         sessionKey
	settings         clickhouse.Settings
	maxOpen, maxIdle int
	idleTime         time.Duration
	mu               sync.Mutex
	pools            map[sessionKey]*sql.DB
	closed           bool
}

// ValidateOptions checks the upstream protocol before any listener starts.
func ValidateOptions(o *bo.Options) error {
	if o == nil {
		return errors.New("nil ClickHouse backend options")
	}
	switch strings.ToLower(o.Protocol) {
	case "", "http", "native":
	default:
		return fmt.Errorf("unsupported ClickHouse upstream protocol %q", o.Protocol)
	}
	if strings.EqualFold(o.Protocol, "native") && o.SigV4 != nil {
		return errors.New("SigV4 is not supported for a native ClickHouse origin")
	}
	return nil
}

// NewNativeClient creates pools lazily; origin credentials and database are defaults.
func NewNativeClient(o *bo.Options) (*NativeClient, error) {
	if err := ValidateOptions(o); err != nil {
		return nil, err
	}
	u, err := url.Parse(o.OriginURL)
	if err != nil {
		return nil, err
	}
	host := o.Host
	if host == "" {
		host = u.Host
	}
	if host == "" {
		return nil, errors.New("clickhouse native: origin host is empty")
	}
	addr := &url.URL{Host: host}
	if addr.Port() == "" {
		host = net.JoinHostPort(strings.Trim(addr.Hostname(), "[]"), "9000")
	}
	c, err := proxy.NewHTTPClient(o)
	if err != nil {
		return nil, err
	}
	nc := &NativeClient{
		options:  clickhouse.Options{Addr: []string{host}, Protocol: clickhouse.Native, DialTimeout: 10 * time.Second},
		defaults: sessionKey{database: "default", username: "default"}, settings: make(clickhouse.Settings),
		maxOpen: o.MaxConcurrentConns, maxIdle: o.MaxIdleConns, idleTime: time.Duration(o.KeepAliveTimeout), pools: make(map[sessionKey]*sql.DB),
	}
	if strings.EqualFold(u.Scheme, "https") || strings.EqualFold(o.Scheme, "https") {
		if transport, ok := c.Transport.(*http.Transport); ok && transport.TLSClientConfig != nil {
			nc.options.TLS = transport.TLSClientConfig.Clone()
		}
		if nc.options.TLS == nil {
			nc.options.TLS = defaultTLSConfig()
		}
	}
	if u.User != nil {
		nc.defaults.username = u.User.Username()
		nc.defaults.password, _ = u.User.Password()
	}
	for key, values := range u.Query() {
		if key == "database" {
			nc.defaults.database = values[len(values)-1]
		} else {
			nc.settings[key] = values[len(values)-1]
		}
	}
	if o.Timeout > 0 {
		nc.options.ReadTimeout = time.Duration(o.Timeout)
	}
	nc.db = nc.open(nc.defaults)
	nc.pools[nc.defaults] = nc.db
	return nc, nil
}

func (nc *NativeClient) open(key sessionKey) *sql.DB {
	options := nc.options
	options.Auth = clickhouse.Auth{Database: key.database, Username: key.username, Password: key.password}
	db := clickhouse.OpenDB(&options)
	db.SetMaxOpenConns(nc.maxOpen)
	db.SetMaxIdleConns(nc.maxIdle)
	db.SetConnMaxIdleTime(nc.idleTime)
	return db
}

func (nc *NativeClient) pool(r *http.Request) (*sql.DB, error) {
	key := nc.defaults
	params := r.URL.Query()
	if params.Has("database") {
		key.database = params.Get("database")
	}
	if params.Has("user") {
		key.username = params.Get("user")
		key.password = params.Get("password")
	}
	if user := r.Header.Get("X-ClickHouse-User"); user != "" {
		key.username = user
		key.password = r.Header.Get("X-ClickHouse-Key")
	}
	if user, password, ok := r.BasicAuth(); ok {
		key.username, key.password = user, password
	}
	nc.mu.Lock()
	defer nc.mu.Unlock()
	if nc.closed {
		return nil, errors.New("native ClickHouse transport is closed")
	}
	if db := nc.pools[key]; db != nil {
		return db, nil
	}
	if len(nc.pools) >= 64 {
		return nil, errors.New("native ClickHouse session pool limit reached")
	}
	db := nc.open(key)
	nc.pools[key] = db
	return db, nil
}

// Close releases all native pools, allowing already-running queries to finish.
func (nc *NativeClient) Close() error {
	nc.mu.Lock()
	defer nc.mu.Unlock()
	nc.closed = true
	var errs []error
	for _, db := range nc.pools {
		errs = append(errs, db.Close())
	}
	nc.pools = nil
	return errors.Join(errs...)
}

// CloseIdleConnections retires the transport when its backend generation is replaced.
func (nc *NativeClient) CloseIdleConnections() { _ = nc.Close() }

// RoundTrip implements http.RoundTripper for proxy and health-check requests.
func (nc *NativeClient) RoundTrip(r *http.Request) (*http.Response, error) { return nc.Fetch(r) }

// Fetch executes SQL and encodes the actual result format requested by the caller.
func (nc *NativeClient) Fetch(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	statement, err := extractSQL(r)
	if err != nil {
		return syntheticErrorResponse(http.StatusBadRequest, err), nil
	}
	fallback := r.URL.Query().Get("default_format")
	if fallback == "" {
		fallback = "TabSeparated"
	}
	statement, format, selectQuery, err := aftership.SplitFormat(statement, fallback)
	if err != nil {
		return syntheticErrorResponse(http.StatusBadRequest, err), nil
	}
	if selectQuery && !supportedFormat(format) {
		return syntheticErrorResponse(http.StatusBadRequest, fmt.Errorf("unsupported native upstream output format %q", format)), nil
	}
	db, err := nc.pool(r)
	if err != nil {
		return syntheticErrorResponse(http.StatusServiceUnavailable, err), nil
	}
	settings := make(clickhouse.Settings, len(nc.settings))
	maps.Copy(settings, nc.settings)
	parameters := make(clickhouse.Parameters)
	for key, values := range r.URL.Query() {
		value := values[len(values)-1]
		if name, ok := strings.CutPrefix(key, "param_"); ok {
			parameters[name] = value
			continue
		}
		switch key {
		case "query", "database", "user", "password", "default_format", "query_id", "client_protocol_version", "compress", "decompress":
		default:
			settings[key] = value
		}
	}
	ctx := clickhouse.Context(r.Context(), clickhouse.WithSettings(settings), clickhouse.WithParameters(parameters), clickhouse.WithQueryID(r.URL.Query().Get("query_id")))
	rows, err := db.QueryContext(ctx, statement) // #nosec G701 -- forwards user SQL to the configured origin
	if err != nil {
		return syntheticErrorResponse(http.StatusBadGateway, err), nil
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return syntheticErrorResponse(http.StatusBadGateway, err), nil
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return syntheticErrorResponse(http.StatusBadGateway, err), nil
	}
	columns := make([]server.Column, len(names))
	values := make([][]any, len(names))
	dest := make([]any, len(names))
	pointers := make([]any, len(names))
	for i, name := range names {
		columns[i] = server.Column{Name: name, Type: types[i].DatabaseTypeName()}
		pointers[i] = &dest[i]
	}
	count := 0
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return syntheticErrorResponse(http.StatusBadGateway, err), nil
		}
		for i, value := range dest {
			values[i] = append(values[i], value)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return syntheticErrorResponse(http.StatusBadGateway, err), nil
	}
	var body []byte
	contentType := "text/plain; charset=utf-8"
	if selectQuery || len(columns) > 0 {
		body, contentType, err = encodeResultWithSettings(
			columns, values, count, format, r.URL.Query().Get("client_protocol_version"), r.URL.Query(),
		)
	}
	if err != nil {
		return syntheticErrorResponse(http.StatusBadGateway, err), nil
	}
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{
			"Content-Type": {contentType}, "X-Clickhouse-Format": {format},
		},
		Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: r,
	}, nil
}

func supportedFormat(format string) bool {
	switch strings.ToLower(format) {
	case "json", "native", "csv", "csvwithnames", "tabseparated", "tsv", "tabseparatedwithnames", "tsvwithnames", "tabseparatedwithnamesandtypes", "tsvwithnamesandtypes":
		return true
	}
	return false
}

func encodeResultWithSettings(
	columns []server.Column,
	values [][]any,
	count int,
	format, protocolVersion string,
	settings url.Values,
) ([]byte, string, error) {
	var out bytes.Buffer
	lower := strings.ToLower(format)
	if lower == "native" {
		revision, err := strconv.ParseUint(protocolVersion, 10, 64)
		if protocolVersion != "" && err != nil {
			return nil, "", err
		}
		err = server.EncodeNativeFormat(&out, columns, values, uint64(count), revision) //nolint:gosec // count is the nonnegative number of scanned rows
		return out.Bytes(), "application/octet-stream", err
	}
	if lower == "json" {
		meta := make([]map[string]string, len(columns))
		data := make([]map[string]any, count)
		for i, col := range columns {
			meta[i] = map[string]string{"name": col.Name, "type": col.Type}
		}
		for row := range count {
			data[row] = make(map[string]any, len(columns))
			for col, field := range columns {
				data[row][field.Name] = jsonValue(values[col][row], field.Type)
			}
		}
		err := json.NewEncoder(&out).Encode(map[string]any{"meta": meta, "data": data, "rows": count})
		return out.Bytes(), "application/json", err
	}
	for _, col := range columns {
		typ := scalarType(col.Type)
		if strings.HasPrefix(typ, "Array(") || strings.HasPrefix(typ, "Map(") || strings.HasPrefix(typ, "Tuple(") {
			return nil, "", fmt.Errorf("text output for %s is unsupported; use JSON or Native", col.Type)
		}
	}
	csvFormat := strings.HasPrefix(lower, "csv")
	withNames := strings.Contains(lower, "withnames")
	withTypes := strings.HasSuffix(lower, "andtypes")
	if !csvFormat {
		writeTSVRows(
			&out, columns, values, count, withNames, withTypes,
			settings.Get("format_tsv_null_representation"),
			settingEnabled(settings.Get("output_format_tsv_crlf_end_of_line")),
		)
		return out.Bytes(), "text/plain; charset=utf-8", nil
	}
	row := make([]string, len(columns))
	rows := make([][]string, 0, count+2)
	if withNames {
		for i, c := range columns {
			row[i] = c.Name
		}
		rows = append(rows, slices.Clone(row))
	}
	if withTypes {
		for i, c := range columns {
			row[i] = c.Type
		}
		rows = append(rows, slices.Clone(row))
	}
	for i := range count {
		for j, c := range columns {
			row[j] = textValue(values[j][i], c.Type)
		}
		rows = append(rows, slices.Clone(row))
	}
	writer := csv.NewWriter(&out)
	if delimiter := settings.Get("format_csv_delimiter"); delimiter != "" {
		runes := []rune(delimiter)
		if len(runes) != 1 {
			return nil, "", errors.New("format_csv_delimiter must contain exactly one character")
		}
		writer.Comma = runes[0]
	}
	writer.UseCRLF = settingEnabled(settings.Get("output_format_csv_crlf_end_of_line"))
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			return nil, "", err
		}
	}
	writer.Flush()
	return out.Bytes(), "text/plain; charset=utf-8", writer.Error()
}

func writeTSVRows(
	out *bytes.Buffer,
	columns []server.Column,
	values [][]any,
	count int,
	withNames, withTypes bool,
	nullRepresentation string,
	useCRLF bool,
) {
	if nullRepresentation == "" {
		nullRepresentation = "\\N"
	}
	replacer := strings.NewReplacer("\\", "\\\\", "\t", "\\t", "\n", "\\n", "\r", "\\r", "\x00", "\\0")
	writeRow := func(row []string, nulls []bool) {
		for i, field := range row {
			if i > 0 {
				_ = out.WriteByte('\t')
			}
			if nulls != nil && nulls[i] {
				_, _ = out.WriteString(nullRepresentation)
			} else {
				_, _ = out.WriteString(replacer.Replace(field))
			}
		}
		if useCRLF {
			_ = out.WriteByte('\r')
		}
		_ = out.WriteByte('\n')
	}
	row := make([]string, len(columns))
	if withNames {
		for i, col := range columns {
			row[i] = col.Name
		}
		writeRow(row, nil)
	}
	if withTypes {
		for i, col := range columns {
			row[i] = col.Type
		}
		writeRow(row, nil)
	}
	nulls := make([]bool, len(columns))
	for i := range count {
		for j, col := range columns {
			nulls[j] = indirectValue(values[j][i]) == nil
			row[j] = textValue(values[j][i], col.Type)
		}
		writeRow(row, nulls)
	}
}

func settingEnabled(value string) bool {
	return value == "1" || strings.EqualFold(value, "true")
}

func jsonValue(value any, typ string) any {
	value = indirectValue(value)
	if t, ok := value.(time.Time); ok {
		return textValue(t, typ)
	}
	return value
}

func textValue(value any, typ string) string {
	value = indirectValue(value)
	typ = scalarType(typ)
	if value == nil {
		return "\\N"
	}
	if t, ok := value.(time.Time); ok {
		if typ == "Date" || typ == "Date32" {
			return t.UTC().Format("2006-01-02")
		}
		if after, ok0 := strings.CutPrefix(typ, "DateTime64("); ok0 {
			precision, _ := strconv.Atoi(strings.TrimSpace(strings.Split(after, ",")[0]))
			if precision == 0 {
				precision, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(typ, "DateTime64("), ")"))
			}
			if precision > 0 && precision <= 9 {
				return t.UTC().Format("2006-01-02 15:04:05." + strings.Repeat("0", precision))
			}
		}
		return t.UTC().Format("2006-01-02 15:04:05")
	}
	if b, ok := value.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(value)
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

func defaultTLSConfig() *tls.Config { return &tls.Config{MinVersion: tls.VersionTLS12} }

func indirectValue(value any) any {
	for value != nil {
		rv := reflect.ValueOf(value)
		if rv.Kind() != reflect.Pointer {
			return value
		}
		if rv.IsNil() {
			return nil
		}
		value = rv.Elem().Interface()
	}
	return nil
}

func scalarType(typ string) string {
	for {
		if inner, ok := strings.CutPrefix(typ, "Nullable("); ok {
			typ = strings.TrimSuffix(inner, ")")
			continue
		}
		if inner, ok := strings.CutPrefix(typ, "LowCardinality("); ok {
			typ = strings.TrimSuffix(inner, ")")
			continue
		}
		return typ
	}
}
