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

// FrontendConfig is a collection of configurations for the main http frontend for the application
type Options struct {
	// ListenAddress is IP address for the main http listener for the application
	ListenAddress string `yaml:"listen_address,omitempty"`
	// ListenPort is TCP Port for the main http listener for the application
	ListenPort int `yaml:"listen_port,omitempty"`
	// TLSListenAddress is IP address for the tls  http listener for the application
	TLSListenAddress string `yaml:"tls_listen_address,omitempty"`
	// TLSListenPort is the TCP Port for the tls http listener for the application
	TLSListenPort int `yaml:"tls_listen_port,omitempty"`
	// ConnectionsLimit indicates how many concurrent front end connections trickster will handle at any time
	ConnectionsLimit int `yaml:"connections_limit,omitempty"`

	// ServeTLS indicates whether to listen and serve on the TLS port, meaning
	// at least one backend options has a valid certificate and key file configured.
	ServeTLS bool `yaml:"-"`
}

// New returns a new Frontend Options with default values
func New() *Options {
	return &Options{
		ListenPort:       DefaultProxyListenPort,
		ListenAddress:    DefaultProxyListenAddress,
		TLSListenPort:    DefaultTLSProxyListenPort,
		TLSListenAddress: DefaultTLSProxyListenAddress,
	}
}

// Equal returns true if the FrontendConfigs are identical in value.
func (o *Options) Equal(o2 *Options) bool {
	return *o == *o2
}

// Clone returns a clone of the Options
func (o *Options) Clone() *Options {
	return &Options{
		ListenAddress:    o.ListenAddress,
		ListenPort:       o.ListenPort,
		TLSListenAddress: o.TLSListenAddress,
		TLSListenPort:    o.TLSListenPort,
		ConnectionsLimit: o.ConnectionsLimit,
		ServeTLS:         o.ServeTLS,
	}
}
