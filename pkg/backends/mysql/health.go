/*
 * Copyright 2026 The Trickster Authors
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

package mysql

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"

	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/sqlerror"
)

type healthConnection interface {
	Ping() error
	Close()
}

type healthConnector func(context.Context, *vtmysql.ConnParams) (healthConnection, error)

// DefaultHealthCheckConfig returns the protocol-neutral health-check defaults
// for MySQL. Operators can configure interval, timeout, and transition
// thresholds; HTTP request and response matching options do not apply.
func (c *Client) DefaultHealthCheckConfig() *ho.Options {
	return ho.New()
}

// HealthCheckProbe returns a native MySQL probe that authenticates with the
// configured origin transport and executes COM_PING on a fresh connection.
func (c *Client) HealthCheckProbe() healthcheck.Probe {
	params, err := upstreamConnParamsFromOptions(c.Configuration())
	if err != nil {
		return func(context.Context) error {
			return errors.New("mysql health probe configuration is invalid")
		}
	}
	return newMySQLHealthProbe(params, connectHealthOrigin)
}

func connectHealthOrigin(ctx context.Context,
	params *vtmysql.ConnParams,
) (healthConnection, error) {
	return vtmysql.Connect(ctx, params)
}

func newMySQLHealthProbe(params vtmysql.ConnParams, connect healthConnector) healthcheck.Probe {
	return func(ctx context.Context) error {
		conn, err := connect(ctx, &params)
		if err != nil {
			return classifyHealthError("connect", err)
		}
		defer conn.Close()

		pingDone := make(chan error, 1)
		go func() {
			pingDone <- conn.Ping()
		}()
		select {
		case err := <-pingDone:
			if err != nil {
				return classifyHealthError("COM_PING", err)
			}
			return nil
		case <-ctx.Done():
			conn.Close()
			return errors.New("mysql health probe timed out")
		}
	}
}

func classifyHealthError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errors.New("mysql health probe timed out")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errors.New("mysql health probe timed out")
	}
	if sqlErr, ok := errors.AsType[*sqlerror.SQLError](err); ok {
		message := strings.ToLower(sqlErr.Message)
		if strings.Contains(message, "timed out") || strings.Contains(message, "timeout") {
			return errors.New("mysql health probe timed out")
		}
		if strings.Contains(message, "tls") || strings.Contains(message, "x509") ||
			strings.Contains(message, "certificate") {
			return errors.New("mysql TLS handshake or certificate verification failed")
		}
		switch sqlErr.Number() {
		case sqlerror.ERAccessDeniedError, sqlerror.ERDBAccessDenied:
			return errors.New("mysql authentication or database access failed")
		case sqlerror.CRSSLConnectionError:
			return errors.New("mysql TLS handshake or certificate verification failed")
		case sqlerror.CRConnectionError, sqlerror.CRConnHostError:
			if strings.Contains(message, "connection refused") {
				return errors.New("mysql origin refused the connection")
			}
			return errors.New("mysql origin connection failed")
		default:
			return fmt.Errorf("mysql %s failed with server error %d", operation, sqlErr.Number())
		}
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "connection refused"):
		return errors.New("mysql origin refused the connection")
	case strings.Contains(message, "tls"), strings.Contains(message, "x509"),
		strings.Contains(message, "certificate"):
		return errors.New("mysql TLS handshake or certificate verification failed")
	default:
		return fmt.Errorf("mysql %s failed", operation)
	}
}
