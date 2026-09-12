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

// Package options configures the stream (tcp, tls and udp) listener protocols.
package options

import (
	"errors"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

const (
	// DefaultConnectTimeout bounds the upstream dial and the SNI peek of a stream connection.
	DefaultConnectTimeout = timeconv.Duration(10 * time.Second)
	// DefaultUDPIdleTimeout ends a UDP session that has carried no datagram for this long,
	// since a datagram flow has no close of its own.
	DefaultUDPIdleTimeout = timeconv.Duration(60 * time.Second)
)

// ErrNegativeTimeout indicates a stream timeout below zero.
var ErrNegativeTimeout = errors.New("stream timeouts must be zero or positive")

// Options tunes a listener whose protocol is tcp, tls or udp.
type Options struct {
	// ConnectTimeout bounds the dial of an upstream member and, on a tls listener, the wait for
	// the client's ClientHello; zero takes the default.
	ConnectTimeout timeconv.Duration `yaml:"connect_timeout,omitempty"`
	// IdleTimeout closes a connection or UDP session over which no byte has moved for this
	// long; zero applies none to a connection and the default to a UDP session.
	IdleTimeout timeconv.Duration `yaml:"idle_timeout,omitempty"`
}

// New returns stream options with every field at its default.
func New() *Options {
	return &Options{ConnectTimeout: DefaultConnectTimeout}
}

// Clone returns a copy of the options, nil for nil.
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	return &out
}

// Equal reports whether both option sets are identical.
func (o *Options) Equal(other *Options) bool {
	if o == nil || other == nil {
		return o == other
	}
	return *o == *other
}

// Validate rejects a negative timeout.
func (o *Options) Validate() error {
	if o == nil {
		return nil
	}
	if o.ConnectTimeout < 0 || o.IdleTimeout < 0 {
		return ErrNegativeTimeout
	}
	return nil
}

// Connect returns the effective connect timeout, which is never zero.
func (o *Options) Connect() time.Duration {
	if o == nil || o.ConnectTimeout <= 0 {
		return time.Duration(DefaultConnectTimeout)
	}
	return time.Duration(o.ConnectTimeout)
}

// Idle returns the effective idle timeout for a connection, where zero means none.
func (o *Options) Idle() time.Duration {
	if o == nil || o.IdleTimeout <= 0 {
		return 0
	}
	return time.Duration(o.IdleTimeout)
}

// UDPIdle returns the effective idle timeout for a UDP session, which is never zero.
func (o *Options) UDPIdle() time.Duration {
	if d := o.Idle(); d > 0 {
		return d
	}
	return time.Duration(DefaultUDPIdleTimeout)
}
