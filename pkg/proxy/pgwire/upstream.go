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
package pgwire

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	sslAccepted = 'S'
	// cancelDrainTimeout bounds the wait for the origin to close a cancel connection.
	cancelDrainTimeout = 5 * time.Second
)

var errUpstreamTLSRefused = errors.New("postgres origin refused TLS")

func (c *Config) dialUpstream(ctx context.Context) (net.Conn, error) {
	dialer := net.Dialer{Timeout: c.ConnectTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.Upstream.Address)
	if err != nil {
		return nil, err
	}
	if c.Upstream.TLS == nil {
		return conn, nil
	}
	secured, err := c.startUpstreamTLS(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return secured, nil
}

func (c *Config) startUpstreamTLS(ctx context.Context, conn net.Conn) (net.Conn, error) {
	if c.ConnectTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(c.ConnectTimeout))
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}
	request, err := (&pgproto3.SSLRequest{}).Encode(nil)
	if err != nil {
		return nil, err
	}
	if _, err = conn.Write(request); err != nil {
		return nil, err
	}
	var answer [1]byte
	if _, err = io.ReadFull(conn, answer[:]); err != nil {
		return nil, err
	}
	if answer[0] != sslAccepted {
		return nil, errUpstreamTLSRefused
	}
	secured := tls.Client(conn, c.Upstream.TLS)
	if err = secured.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return secured, nil
}

func (c *Config) cancelUpstream(ctx context.Context, pid uint32, secret []byte) error {
	conn, err := c.dialUpstream(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	request, err := (&pgproto3.CancelRequest{ProcessID: pid, SecretKey: secret}).Encode(nil)
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(cancelDrainTimeout))
	if _, err = conn.Write(request); err != nil {
		return err
	}
	// The origin answers a cancel request only by closing the connection.
	_, _ = io.Copy(io.Discard, conn)
	return nil
}

func (c *Config) loginUpstream(ctx context.Context, database string,
	params map[string]string,
) (*pgconn.HijackedConn, error) {
	if database == "" {
		database = c.Upstream.Database
	}
	host, port, err := net.SplitHostPort(c.Upstream.Address)
	if err != nil {
		return nil, err
	}
	// Every setting pgconn would otherwise default from the process
	// environment is given explicitly, then the dial and TLS are replaced.
	dsn := url.URL{
		Scheme: schemePostgres, Host: c.Upstream.Address, Path: "/" + database,
		User:     url.UserPassword(c.Upstream.User, c.Upstream.Password),
		RawQuery: "sslmode=disable&connect_timeout=" + strconv.Itoa(connectTimeoutSeconds(c.ConnectTimeout)),
	}
	config, err := pgconn.ParseConfig(dsn.String())
	if err != nil {
		// pgconn's parse errors embed the connection string; never surface them.
		return nil, errors.New("invalid postgres upstream configuration")
	}
	config.Host, config.Fallbacks = host, nil
	if n, err := strconv.ParseUint(port, 10, 16); err == nil {
		config.Port = uint16(n)
	}
	config.TLSConfig = c.Upstream.TLS
	config.RuntimeParams = params
	config.ConnectTimeout = c.ConnectTimeout
	conn, err := pgconn.ConnectConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("postgres upstream login: %w", sanitizeConnectError(err))
	}
	hijacked, err := conn.Hijack()
	if err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	return hijacked, nil
}

func connectTimeoutSeconds(d time.Duration) int {
	return max(int(d/time.Second), 1)
}

func sanitizeConnectError(err error) error {
	// keeps the origin's SQLSTATE and drops pgconn's wrapper,
	// whose text includes the user name and address.
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return fmt.Errorf("origin rejected the session (SQLSTATE %s)", pgErr.Code)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errors.New("timed out")
	}
	return errors.New("connection failed")
}
