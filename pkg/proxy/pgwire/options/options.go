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

// Package options provides settings for PostgreSQL wire-protocol origins and listeners.
package options

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

const (
	// TLSModeDisable never negotiates TLS with the origin.
	TLSModeDisable = "disable"
	// TLSModeRequire negotiates TLS with the origin without verifying its certificate.
	TLSModeRequire = "require"
	// TLSModeVerifyCA verifies the origin's certificate chain but not its host name.
	TLSModeVerifyCA = "verify-ca"
	// TLSModeVerifyFull verifies the origin's certificate chain and host name.
	TLSModeVerifyFull = "verify-full"

	// DefaultHandshakeTimeout bounds the complete startup and authentication exchange.
	DefaultHandshakeTimeout = 10 * time.Second
	// DefaultReadTimeout bounds the wait for the rest of a partially received message.
	DefaultReadTimeout = 30 * time.Second
	// DefaultWriteTimeout bounds each relayed write.
	DefaultWriteTimeout = 30 * time.Second
	// DefaultIdleTimeout bounds the time a session may wait with no request in flight.
	DefaultIdleTimeout = 5 * time.Minute
	// MaxProtocolMessageSizeBytes is PostgreSQL's own message ceiling (1 GiB - 1).
	MaxProtocolMessageSizeBytes = 0x3fffffff
	// DefaultMaxMessageSizeBytes is the largest client message relayed to the origin.
	DefaultMaxMessageSizeBytes = MaxProtocolMessageSizeBytes
)

var tlsModes = []string{TLSModeDisable, TLSModeRequire, TLSModeVerifyCA, TLSModeVerifyFull}

// TLSModes returns the accepted upstream_tls_mode values.
func TLSModes() []string { return slices.Clone(tlsModes) }

// Options contains settings for a backend reached over the PostgreSQL wire protocol.
type Options struct {
	// UpstreamTLSMode selects TLS toward the origin, independent of listener TLS.
	UpstreamTLSMode string `yaml:"upstream_tls_mode,omitempty"`
}

// New returns the default backend options.
func New() *Options {
	return &Options{UpstreamTLSMode: TLSModeDisable}
}

// Clone returns an independent copy.
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	return &out
}

// Validate rejects unknown TLS modes.
func (o *Options) Validate() error {
	if o == nil {
		return nil
	}
	if !slices.Contains(tlsModes, o.UpstreamTLSMode) {
		return fmt.Errorf("postgres.upstream_tls_mode must be one of %v", tlsModes)
	}
	return nil
}

// UnmarshalYAML overlays explicitly configured fields onto the defaults.
func (o *Options) UnmarshalYAML(unmarshal func(any) error) error {
	type plain Options
	value := plain(*New())
	if err := unmarshal(&value); err != nil {
		return err
	}
	*o = Options(value)
	return nil
}

// ListenerOptions constrains untrusted downstream PostgreSQL clients.
type ListenerOptions struct {
	HandshakeTimeout    timeconv.Duration `yaml:"handshake_timeout,omitempty"`
	ReadTimeout         timeconv.Duration `yaml:"read_timeout,omitempty"`
	WriteTimeout        timeconv.Duration `yaml:"write_timeout,omitempty"`
	IdleTimeout         timeconv.Duration `yaml:"idle_timeout,omitempty"`
	MaxMessageSizeBytes int               `yaml:"max_message_size_bytes,omitempty"`
	// AllowCleartextWithoutTLS lets Trickster-authenticated clients send a
	// cleartext password over an unencrypted connection.
	AllowCleartextWithoutTLS bool `yaml:"allow_cleartext_without_tls,omitempty"`
	// AllowMD5 offers the deprecated md5 method to users stored as md5 verifiers.
	AllowMD5 bool `yaml:"allow_md5,omitempty"`
}

// NewListener returns the downstream safety defaults.
func NewListener() *ListenerOptions {
	return &ListenerOptions{
		HandshakeTimeout:    timeconv.Duration(DefaultHandshakeTimeout),
		ReadTimeout:         timeconv.Duration(DefaultReadTimeout),
		WriteTimeout:        timeconv.Duration(DefaultWriteTimeout),
		IdleTimeout:         timeconv.Duration(DefaultIdleTimeout),
		MaxMessageSizeBytes: DefaultMaxMessageSizeBytes,
	}
}

// Clone returns an independent copy.
func (o *ListenerOptions) Clone() *ListenerOptions {
	if o == nil {
		return nil
	}
	out := *o
	return &out
}

// Validate rejects disabled or protocol-invalid safety limits.
func (o *ListenerOptions) Validate() error {
	if o == nil {
		return nil
	}
	if o.HandshakeTimeout <= 0 || o.ReadTimeout <= 0 || o.WriteTimeout <= 0 || o.IdleTimeout <= 0 {
		return errors.New("postgres listener timeouts must be greater than zero")
	}
	if o.MaxMessageSizeBytes <= 0 || o.MaxMessageSizeBytes > MaxProtocolMessageSizeBytes {
		return fmt.Errorf("postgres.max_message_size_bytes must be between 1 and %d",
			MaxProtocolMessageSizeBytes)
	}
	return nil
}

// UnmarshalYAML overlays explicitly configured fields onto safety defaults.
func (o *ListenerOptions) UnmarshalYAML(unmarshal func(any) error) error {
	type plain ListenerOptions
	value := plain(*NewListener())
	if err := unmarshal(&value); err != nil {
		return err
	}
	*o = ListenerOptions(value)
	return nil
}
