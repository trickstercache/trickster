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

// Options is a collection of Metrics Collection configurations
type Options struct {
	// ListenAddress is IP address from which the Application Metrics are available for pulling at /metrics
	ListenAddress string `yaml:"listen_address,omitempty"`
	// ListenPort is TCP Port from which the Application Metrics are available for pulling at /metrics
	ListenPort int `yaml:"listen_port,omitempty"`
}

// New returns a new Options with default values
func New() *Options {
	return &Options{
		ListenAddress: DefaultMetricsListenAddress,
		ListenPort:    DefaultMetricsListenPort,
	}
}

// Clone returns an exact copy of the Options
func (o *Options) Clone() *Options {
	return &Options{
		ListenAddress: o.ListenAddress,
		ListenPort:    o.ListenPort,
	}
}
