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

package options

import (
	"errors"
	"fmt"
)

// ProtocolListenerOptions configures a raw TCP listener for a non-HTTP
// protocol such as the ClickHouse native binary protocol or MySQL wire
// protocol.
type ProtocolListenerOptions struct {
	// Name is a human-readable identifier for this listener (e.g. "clickhouse-native").
	Name string `yaml:"name"`
	// Protocol selects the wire protocol handler (e.g. "clickhouse-native", "mysql").
	Protocol string `yaml:"protocol"`
	// ListenAddress is the IP address to bind (default: all interfaces).
	ListenAddress string `yaml:"listen_address,omitempty"`
	// ListenPort is the TCP port to bind.
	ListenPort int `yaml:"listen_port"`
	// Backend is the name of the configured backend this listener routes to.
	Backend string `yaml:"backend"`
	// ConnectionsLimit is the maximum number of concurrent connections.
	ConnectionsLimit int `yaml:"connections_limit,omitempty"`
}

// Validate checks that required fields are present.
func (o *ProtocolListenerOptions) Validate() error {
	if o.Name == "" {
		return errors.New("protocol_listener: name is required")
	}
	if o.Protocol == "" {
		return fmt.Errorf("protocol_listener %q: protocol is required", o.Name)
	}
	if o.ListenPort < 1 {
		return fmt.Errorf("protocol_listener %q: listen_port must be > 0", o.Name)
	}
	if o.Backend == "" {
		return fmt.Errorf("protocol_listener %q: backend is required", o.Name)
	}
	return nil
}
