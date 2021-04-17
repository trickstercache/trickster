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

const (
	// DefaultProxyListenPort is the default port that the HTTP frontend will listen on
	DefaultProxyListenPort = 8480
	// DefaultProxyListenAddress is the default address that the HTTP frontend will listen on
	DefaultProxyListenAddress = ""

	// 8482 is reserved for mockster, allowing the default TLS port to end with 3

	// DefaultTLSProxyListenPort is the default port that the TLS frontend endpoint will listen on
	DefaultTLSProxyListenPort = 8483
	// DefaultTLSProxyListenAddress is the default address that the TLS frontend endpoint will listen on
	DefaultTLSProxyListenAddress = ""
)
