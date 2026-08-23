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

// Package options provides settings that constrain MySQL origin and listener work.
package options

import (
	"errors"
	"fmt"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

const (
	// DefaultMaxResultRows bounds a result before Trickster terminates both sides.
	DefaultMaxResultRows = 100000
	// DefaultMaxResultSizeBytes bounds decoded result data held or streamed by Trickster.
	DefaultMaxResultSizeBytes = 64 * 1024 * 1024
	// DefaultHandshakeTimeout bounds the complete downstream authentication exchange.
	DefaultHandshakeTimeout = 10 * time.Second
	// DefaultReadTimeout bounds an in-progress downstream command read.
	DefaultReadTimeout = 30 * time.Second
	// DefaultWriteTimeout bounds each downstream response write.
	DefaultWriteTimeout = 30 * time.Second
	// DefaultIdleTimeout independently bounds time waiting for the next command.
	DefaultIdleTimeout = 5 * time.Minute
	// DefaultMaxPacketSizeBytes is the protocol-library packet ceiling.
	DefaultMaxPacketSizeBytes = 16*1024*1024 - 1
	// DefaultMaxQuerySizeBytes bounds COM_QUERY and prepared SQL text.
	DefaultMaxQuerySizeBytes = 1024 * 1024
)

// Options contains MySQL-specific backend limits. The generic backend timeout,
// max_concurrent_conns, and max_object_size_bytes options supply the remaining
// origin and cache limits.
type Options struct {
	MaxResultRows      int `yaml:"max_result_rows,omitempty"`
	MaxResultSizeBytes int `yaml:"max_result_size_bytes,omitempty"`
}

// New returns the default MySQL backend options.
func New() *Options {
	return &Options{
		MaxResultRows:      DefaultMaxResultRows,
		MaxResultSizeBytes: DefaultMaxResultSizeBytes,
	}
}

// Clone returns an independent copy.
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	return &out
}

// Validate rejects limits that would disable a required safety boundary.
func (o *Options) Validate() error {
	if o == nil {
		return nil
	}
	if o.MaxResultRows <= 0 {
		return errors.New("mysql.max_result_rows must be greater than zero")
	}
	if o.MaxResultSizeBytes <= 0 {
		return errors.New("mysql.max_result_size_bytes must be greater than zero")
	}
	return nil
}

// UnmarshalYAML overlays explicitly configured fields onto safety defaults.
func (o *Options) UnmarshalYAML(unmarshal func(any) error) error {
	type plain Options
	value := plain(*New())
	if err := unmarshal(&value); err != nil {
		return err
	}
	*o = Options(value)
	return nil
}

// ListenerOptions constrains untrusted downstream MySQL clients.
type ListenerOptions struct {
	HandshakeTimeout   timeconv.Duration `yaml:"handshake_timeout,omitempty"`
	ReadTimeout        timeconv.Duration `yaml:"read_timeout,omitempty"`
	WriteTimeout       timeconv.Duration `yaml:"write_timeout,omitempty"`
	IdleTimeout        timeconv.Duration `yaml:"idle_timeout,omitempty"`
	MaxPacketSizeBytes int               `yaml:"max_packet_size_bytes,omitempty"`
	MaxQuerySizeBytes  int               `yaml:"max_query_size_bytes,omitempty"`
}

// NewListener returns the downstream safety defaults.
func NewListener() *ListenerOptions {
	return &ListenerOptions{
		HandshakeTimeout:   timeconv.Duration(DefaultHandshakeTimeout),
		ReadTimeout:        timeconv.Duration(DefaultReadTimeout),
		WriteTimeout:       timeconv.Duration(DefaultWriteTimeout),
		IdleTimeout:        timeconv.Duration(DefaultIdleTimeout),
		MaxPacketSizeBytes: DefaultMaxPacketSizeBytes,
		MaxQuerySizeBytes:  DefaultMaxQuerySizeBytes,
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
		return errors.New("mysql listener timeouts must be greater than zero")
	}
	// Vitess enforces its protocol ceiling before dispatch, including after an
	// in-band TLS upgrade. Keep this fixed until the library offers a per-listener
	// lower bound; max_query_size_bytes provides the configurable SQL limit.
	if o.MaxPacketSizeBytes != DefaultMaxPacketSizeBytes {
		return fmt.Errorf("mysql.max_packet_size_bytes must be %d", DefaultMaxPacketSizeBytes)
	}
	if o.MaxQuerySizeBytes <= 0 || o.MaxQuerySizeBytes > o.MaxPacketSizeBytes {
		return errors.New("mysql.max_query_size_bytes must be between 1 and max_packet_size_bytes")
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
